// Package sandbox contains two execution backends for a node command:
//
//   - hermetic.go (slice 4): host-subprocess path with env scrub, isolated
//     tmpfs, and default-deny network. Used when Node.Runtime.Image is empty.
//
//   - docker.go (this file, slice 5): OCI container path. Used when
//     Node.Runtime.Image is set. Pulls the image, runs the command in
//     a hermetic container, and returns the same Result shape as
//     runCommand in internal/worker.
//
// The two paths are intentionally separate (not unified behind an
// interface) so slice-4 hermetic execution stays untouched. The worker
// in internal/worker/worker.go selects one based on Node.Runtime.Image.
//
// Implementation note: this file shells out to the `docker` CLI rather
// than using the github.com/moby/moby/client SDK. The SDK's v0.5.1 release
// restructured its types into per-file packages that pulled in
// graph-driver and other heavy dependencies; v28.x is monolithic
// but still leaves the v0.5.1 client missing the legacy `*container.Config`
// type. A small wrapper around the Docker CLI gives us the same
// surface we need (pull, create, start, wait, logs, remove, inspect)
// with one well-tested tool (Docker itself) doing the heavy lifting.
// Trade-off: we shell out per node, which adds ~50ms vs the SDK. For
// the 10s–10min node durations the pack measures, this is in the
// noise.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunOptions configures a Docker Run. Zero values mean "use the
// hermetic default" — do not pass zero for fields you want to relax.
type RunOptions struct {
	// Network is the network name to attach the container to. Empty
	// (the default) means no network access at all. Use "host" to
	// allow full network access, or a custom network name.
	Network string

	// MemoryMiB is the memory limit. 0 = 512 MiB default. Hard cap
	// enforced by the kernel.
	MemoryMiB int64

	// CPUs is the CPU quota. 0 = 2.0 default. 1.0 = one full core.
	CPUs float64

	// User is the UID:GID inside the container. "0:0" by default
	// (root). Use a non-root user when the image provides one
	// (e.g. alpine's "nobody").
	User string

	// Workdir is the working directory inside the container. Empty =
	// "/work" default. The directory is created on demand.
	Workdir string

	// Env is the set of environment variables to inject into the
	// container. Empty map = no extra env (the container inherits
	// only PATH, HOME, etc. from the image's defaults).
	Env map[string]string
}

// Result is the outcome of a single Docker Run.
type Result struct {
	// ExitCode is the container's exit code. -1 if the container was
	// killed by a signal or never started.
	ExitCode int

	// CombinedLog is stdout + stderr, joined by a single newline.
	CombinedLog []byte

	// Duration is wall-clock time from container start to exit.
	Duration time.Duration

	// ImageID is the resolved OCI image digest (sha256:...) of the
	// image that was actually run. Useful for evidence bundles.
	ImageID string

	// ContainerID is the Docker container ID. Used for diagnostics
	// and to make log/artifact retrieval idempotent.
	ContainerID string

	// OOMKilled is true if the container exited with exit code 137 and
	// the OOM killer fired. The caller can use this to surface a
	// "memory_failure" classification in the failure classifier.
	OOMKilled bool
}

// ErrImagePull is returned when `docker pull` (or the equivalent
// create-then-start flow) fails. Wraps the underlying Docker daemon
// error.
type ErrImagePull struct {
	Image string
	Err   error
}

func (e *ErrImagePull) Error() string {
	return fmt.Sprintf("docker: pull image %q: %v", e.Image, e.Err)
}

func (e *ErrImagePull) Unwrap() error { return e.Err }

// ErrContainerCreate is returned when the container cannot be created
// (port conflict, OOM at the daemon level, invalid config, etc.).
type ErrContainerCreate struct {
	Image string
	Err   error
}

func (e *ErrContainerCreate) Error() string {
	return fmt.Sprintf("docker: create container (image %q): %v", e.Image, e.Err)
}

func (e *ErrContainerCreate) Unwrap() error { return e.Err }

// ErrContainerStart is returned when the daemon fails to start the
// container (image disappeared between pull and start, etc.).
type ErrContainerStart struct {
	ContainerID string
	Err         error
}

func (e *ErrContainerStart) Error() string {
	return fmt.Sprintf("docker: start container %s: %v", e.ContainerID, e.Err)
}

func (e *ErrContainerStart) Unwrap() error { return e.Err }

// ErrDockerUnavailable is returned when no Docker daemon is reachable.
// Callers (e.g. the worker) should treat this as a configuration
// problem, not a transient failure.
var ErrDockerUnavailable = errors.New("docker: daemon unreachable (set DOCKER_HOST or start dockerd)")

// defaults applied when RunOptions has zero values. Hermetic.
const (
	defaultMemoryMiB int64   = 512
	defaultCPUs      float64 = 2.0
	defaultUser             = "0:0"
	defaultWorkdir          = "/work"
	pidsLimit               = 256
)

// dockerPath is the path to the `docker` CLI. We look it up on first
// use and cache the result; tests can override via dockerPathOverride.
var (
	dockerPath        string
	dockerPathOnce    bool
	dockerPathOverride string
)

func resolveDockerPath() (string, error) {
	if dockerPathOverride != "" {
		return dockerPathOverride, nil
	}
	if dockerPathOnce {
		return dockerPath, nil
	}
	dockerPathOnce = true
	p, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("%w: docker CLI not on PATH: %v", ErrDockerUnavailable, err)
	}
	dockerPath = p
	return p, nil
}

// Run pulls `image` (idempotent), creates a hermetic container, runs
// `command` inside it, waits for completion, and returns the result.
// The container is removed before Run returns.
func Run(ctx context.Context, imageName, command string, opts RunOptions) (*Result, error) {
	if imageName == "" {
		return nil, errors.New("docker: empty image")
	}
	if command == "" {
		return nil, errors.New("docker: empty command")
	}
	applyDefaults(&opts)

	docker, err := resolveDockerPath()
	if err != nil {
		return nil, err
	}

	// Ping the daemon to fail fast if it's not running.
	if err := dockerPing(ctx, docker); err != nil {
		return nil, fmt.Errorf("%w: ping: %v", ErrDockerUnavailable, err)
	}

	// Pull the image. `docker pull` is idempotent.
	pullCmd := exec.CommandContext(ctx, docker, "pull", imageName)
	if out, err := pullCmd.CombinedOutput(); err != nil {
		return nil, &ErrImagePull{Image: imageName, Err: fmt.Errorf("%w: %s", err, string(out))}
	}

	// Resolve the image ID (digest) for evidence.
	imageID := imageName
	if id, err := dockerInspectImage(ctx, docker, imageName); err == nil {
		imageID = id
	}

	// Build env slice in KEY=VALUE form.
	envSlice := make([]string, 0, len(opts.Env))
	for k, v := range opts.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	// Build the docker create args with strict hermetic defaults.
	// We deliberately do NOT pass --rm here; we call docker rm -f
	// ourselves after reading the logs. --rm auto-removes the
	// container the moment it exits, which races with `docker logs`.
	args := []string{
		"create",
		"--read-only",                              // hermetic: root FS read-only
		"--cap-drop=ALL",                           // hermetic: drop all Linux capabilities
		"--security-opt=no-new-privileges",          // hermetic: no privilege escalation
		fmt.Sprintf("--pids-limit=%d", pidsLimit),   // hermetic: limit process count
		fmt.Sprintf("--memory=%dm", opts.MemoryMiB), // memory cap
		fmt.Sprintf("--cpus=%.1f", opts.CPUs),       // CPU quota
		"--user", opts.User,
		"-w", opts.Workdir,
	}
	if opts.Network == "" {
		args = append(args, "--network=none") // default: no network
	} else {
		args = append(args, "--network="+opts.Network)
	}
	for _, e := range envSlice {
		args = append(args, "-e", e)
	}
	args = append(args, imageName, "/bin/sh", "-c", command)

	createCmd := exec.CommandContext(ctx, docker, args...)
	createOut, err := createCmd.CombinedOutput()
	if err != nil {
		return nil, &ErrContainerCreate{Image: imageName, Err: fmt.Errorf("%w: %s", err, string(createOut))}
	}
	containerID := strings.TrimSpace(string(createOut))
	if containerID == "" {
		return nil, &ErrContainerCreate{Image: imageName, Err: errors.New("empty container id from docker create")}
	}

	// Start the container.
	startCmd := exec.CommandContext(ctx, docker, "start", containerID)
	if out, err := startCmd.CombinedOutput(); err != nil {
		_ = dockerRemove(ctx, docker, containerID)
		return nil, &ErrContainerStart{ContainerID: containerID, Err: fmt.Errorf("%w: %s", err, string(out))}
	}

	// Stream logs in a goroutine (reserved for future streaming
	// support; for now we read after exit).
	start := time.Now()
	var logBuf bytes.Buffer
	logDone := make(chan struct{})
	go func() {
		close(logDone)
	}()

	// Wait for the container to exit.
	waitCmd := exec.CommandContext(ctx, docker, "wait", containerID)
	waitOut, err := waitCmd.CombinedOutput()
	if err != nil {
		<-logDone
		_ = dockerRemove(ctx, docker, containerID)
		return nil, fmt.Errorf("docker: wait: %w: %s", err, string(waitOut))
	}
	exitCode, _ := parseExitCode(string(waitOut))

	// Read the logs now that the container is done.
	logsCmd := exec.CommandContext(ctx, docker, "logs", containerID)
	logsOut, _ := logsCmd.CombinedOutput()
	logBuf.Write(logsOut)

	<-logDone

	// OOM detection.
	oomKilled := false
	if exitCode == 137 {
		// Inspect the container's OOMKilled flag.
		if state, err := dockerInspectContainer(ctx, docker, containerID); err == nil {
			if state != nil && state.OOMKilled {
				oomKilled = true
			}
		}
	}

	// AutoRemove (--rm) was not set above so we can read logs first.
	// Remove the container now that we have everything we need.
	_ = dockerRemove(ctx, docker, containerID)

	return &Result{
		ExitCode:    exitCode,
		CombinedLog: logBuf.Bytes(),
		Duration:    time.Since(start),
		ImageID:     imageID,
		ContainerID: containerID,
		OOMKilled:   oomKilled,
	}, nil
}

func applyDefaults(opts *RunOptions) {
	if opts.MemoryMiB == 0 {
		opts.MemoryMiB = defaultMemoryMiB
	}
	if opts.CPUs == 0 {
		opts.CPUs = defaultCPUs
	}
	if opts.User == "" {
		opts.User = defaultUser
	}
	if opts.Workdir == "" {
		opts.Workdir = defaultWorkdir
	}
}

// dockerPing runs `docker info` (a cheap daemon round-trip) and
// returns ErrDockerUnavailable if it fails. We use `info` rather than
// `version` because `version` doesn't require a live connection in
// some configurations; `info` always does.
func dockerPing(ctx context.Context, docker string) error {
	cmd := exec.CommandContext(ctx, docker, "info")
	return cmd.Run()
}

// dockerInspectImage returns the image's sha256:... digest by parsing
// `docker inspect --format '{{.Id}}'`. Returns an error if the image
// is not present (which is fine — we'll just fall back to the input
// image name in that case).
func dockerInspectImage(ctx context.Context, docker, imageName string) (string, error) {
	cmd := exec.CommandContext(ctx, docker, "inspect", "--format", "{{.Id}}", imageName)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("empty image id")
	}
	return id, nil
}

// inspectState is the subset of `docker inspect` output we read.
type inspectState struct {
	OOMKilled bool `json:"OOMKilled"`
}

// dockerInspectContainer returns the container's OOMKilled flag.
func dockerInspectContainer(ctx context.Context, docker, containerID string) (*inspectState, error) {
	cmd := exec.CommandContext(ctx, docker, "inspect", "--format", "{{.State}}", containerID)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	// `.State` is a nested object; we re-marshal the JSON to get a
	// clean struct. `docker inspect --format {{.State}}` on a single
	// container returns the JSON-encoded state object.
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, errors.New("empty state from docker inspect")
	}
	var s inspectState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("parse state JSON %q: %w", raw, err)
	}
	return &s, nil
}

// dockerRemove calls `docker rm -f` to force-remove a container.
// Errors are ignored — this is best-effort cleanup.
func dockerRemove(ctx context.Context, docker, containerID string) error {
	cmd := exec.CommandContext(ctx, docker, "rm", "-f", containerID)
	return cmd.Run()
}

// parseExitCode parses the output of `docker wait <id>`, which is
// simply a decimal number followed by a newline.
func parseExitCode(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1, errors.New("empty exit code")
	}
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1, fmt.Errorf("non-numeric exit code %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}