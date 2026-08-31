// Package hermetic_test covers the Hermetic Execution Standard (#111)
// enforcement for the works-execution subprocess sandbox. Slice-4 unit
// tests exercise the sandbox in isolation; no Docker, no network.
package hermetic_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/sandbox"
)

// TestPrepare_ScrubsEnvironment verifies the Hermetic default: when the
// manifest has no `environment` allow-list, the subprocess receives
// only the sandbox-injected PATH/HOME/LANG, regardless of what the
// caller tried to pass through.
func TestPrepare_ScrubsEnvironment(t *testing.T) {
	t.Parallel()
	prep, err := sandbox.Prepare(context.Background(), "true", map[string]string{
		"AWS_SECRET_ACCESS_KEY": "leaky",
		"PATH":                  "/should/be/overridden",
		"MY_CUSTOM_VAR":         "also-leaky",
	}, sandbox.Manifest{
		ActionID:     "scrub_test",
		Network:      sandbox.NetworkDeny,
		Filesystem:   sandbox.FSIsolated,
		Environment:  nil, // hermetic default: allow nothing beyond defaults
	}, sandbox.Options{ProbeNetwork: false})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Cleanup(prep.Cleanup)

	env := envMap(prep.Env)
	for k := range env {
		switch k {
		case "PATH", "HOME", "LANG":
			// allowed
		default:
			t.Errorf("unexpected env var leaked: %s=%s", k, env[k])
		}
	}
	if got := env["AWS_SECRET_ACCESS_KEY"]; got != "" {
		t.Errorf("AWS secret leaked: %q", got)
	}
	if got := env["MY_CUSTOM_VAR"]; got != "" {
		t.Errorf("caller var leaked: %q", got)
	}
	if got := env["PATH"]; got != "/should/be/overridden" {
		t.Errorf("PATH was overridden by caller; want sandbox default, got %q", got)
	}
}

// TestPrepare_AllowListIntersection verifies that when the manifest
// declares an environment allow-list, only those keys (plus sandbox
// defaults) are exposed. A caller cannot smuggle in extra vars even
// via the supplied env map.
func TestPrepare_AllowListIntersection(t *testing.T) {
	t.Parallel()
	prep, err := sandbox.Prepare(context.Background(), "true", map[string]string{
		"GOOS":    "linux",
		"GOARCH":  "amd64",
		"SECRET1": "should-not-leak",
	}, sandbox.Manifest{
		ActionID:   "allow_list_test",
		Network:    sandbox.NetworkDeny,
		Filesystem: sandbox.FSIsolated,
		Environment: map[string]string{
			"GOOS":   "linux",
			"GOARCH": "amd64",
		},
	}, sandbox.Options{ProbeNetwork: false})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Cleanup(prep.Cleanup)

	env := envMap(prep.Env)
	if env["SECRET1"] != "" {
		t.Errorf("SECRET1 leaked despite empty allow-list")
	}
	if env["GOOS"] != "linux" {
		t.Errorf("GOOS missing or wrong: %q", env["GOOS"])
	}
	if env["GOARCH"] != "amd64" {
		t.Errorf("GOARCH missing or wrong: %q", env["GOARCH"])
	}
}

// TestPrepare_CreatesIsolatedWorkspace verifies the workdir exists,
// is empty, and is a fresh per-attempt directory.
func TestPrepare_CreatesIsolatedWorkspace(t *testing.T) {
	t.Parallel()
	prep, err := sandbox.Prepare(context.Background(), "true", nil, sandbox.Manifest{
		ActionID:   "workspace_test",
		Network:    sandbox.NetworkDeny,
		Filesystem: sandbox.FSIsolated,
	}, sandbox.Options{ProbeNetwork: false, Root: t.TempDir() + "/ws"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Cleanup(prep.Cleanup)

	info, err := prep.Stat()
	if err != nil {
		t.Fatalf("stat workdir: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("workdir %s is not a directory", prep.Workdir)
	}
	entries, err := os.ReadDir(prep.Workdir)
	if err != nil {
		t.Fatalf("read workdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty workspace, got %d entries", len(entries))
	}
	if !strings.Contains(prep.Workdir, "attempt-") {
		t.Errorf("workdir name does not match attempt-* pattern: %s", prep.Workdir)
	}
}

// TestPrepare_CleanupRemovesWorkspace verifies Cleanup deletes the
// per-attempt dir and is idempotent.
func TestPrepare_CleanupRemovesWorkspace(t *testing.T) {
	t.Parallel()
	prep, err := sandbox.Prepare(context.Background(), "true", nil, sandbox.Manifest{
		ActionID:   "cleanup_test",
		Filesystem: sandbox.FSIsolated,
	}, sandbox.Options{ProbeNetwork: false, Root: t.TempDir() + "/ws"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	wd := prep.Workdir

	prep.Cleanup()
	prep.Cleanup() // second call must not panic

	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Errorf("workdir still present after cleanup: %v", err)
	}
}

// TestPrepare_NetworkDenyWithProbeDisabled verifies the egress probe is
// bypassed when ProbeNetwork=false. This is the path tests /
// fully-isolated CI runners use.
func TestPrepare_NetworkDenyWithProbeDisabled(t *testing.T) {
	t.Parallel()
	_, err := sandbox.Prepare(context.Background(), "true", nil, sandbox.Manifest{
		ActionID: "no_probe",
		Network:  sandbox.NetworkDeny,
	}, sandbox.Options{ProbeNetwork: false})
	if err != nil {
		t.Fatalf("prepare with probe disabled should not error: %v", err)
	}
}

// TestPrepare_RejectsInvalidManifest verifies every documented
// validation failure surface with ErrInvalidManifest.
func TestPrepare_RejectsInvalidManifest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		m    sandbox.Manifest
	}{
		{
			name: "unknown_network_policy",
			m:    sandbox.Manifest{Network: "lol"},
		},
		{
			name: "allow_list_missing_entries",
			m:    sandbox.Manifest{Network: sandbox.NetworkAllow},
		},
		{
			name: "unknown_filesystem_mode",
			m:    sandbox.Manifest{Filesystem: "weird"},
		},
		{
			name: "mount_missing_target",
			m: sandbox.Manifest{
				Filesystem: sandbox.FSWorkspace,
				Mounts:     []sandbox.Mount{{Source: "/x", Target: ""}},
			},
		},
		{
			name: "mount_relative_target",
			m: sandbox.Manifest{
				Filesystem: sandbox.FSWorkspace,
				Mounts:     []sandbox.Mount{{Source: "/x", Target: "rel/path"}},
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := sandbox.Prepare(context.Background(), "true", nil, tc.m, sandbox.Options{ProbeNetwork: false})
			if !errors.Is(err, sandbox.ErrInvalidManifest) {
				t.Errorf("want ErrInvalidManifest, got %v", err)
			}
		})
	}
}

// TestPrepare_AllowListRequiresEntries verifies the V1 guardrail:
// network=allow-list without any hostnames is invalid. This prevents
// a silent "allow all" footgun when a manifest is partially specified.
func TestPrepare_AllowListRequiresEntries(t *testing.T) {
	t.Parallel()
	_, err := sandbox.Prepare(context.Background(), "true", nil, sandbox.Manifest{
		ActionID:  "guard",
		Network:   sandbox.NetworkAllow,
		AllowList: nil,
	}, sandbox.Options{ProbeNetwork: false})
	if !errors.Is(err, sandbox.ErrInvalidManifest) {
		t.Errorf("want ErrInvalidManifest, got %v", err)
	}
}

// TestDefaultDenyEnv_Helper verifies the helper builds an env with only
// the sandbox defaults + supplied allow-list, and includes PATH/HOME/LANG
// plus the Go toolchain env (GOMODCACHE/GOPATH/GOCACHE) that keeps
// `go vet`/`go test` functional under HOME=/tmp (RFC: self-hosted CI).
func TestDefaultDenyEnv_Helper(t *testing.T) {
	t.Parallel()
	env := envMap(sandbox.DefaultDenyEnv(map[string]string{
		"FOO": "bar",
	}))
	for k := range env {
		switch k {
		case "PATH", "HOME", "LANG", "FOO", "GOMODCACHE", "GOPATH", "GOCACHE":
		default:
			t.Errorf("unexpected key %s=%s", k, env[k])
		}
	}
	if env["FOO"] != "bar" {
		t.Errorf("FOO=%q", env["FOO"])
	}
	// Go env must point into the worker-owned state directory so module
	// downloads and build caches persist across nodes.
	for _, k := range []string{"GOMODCACHE", "GOPATH", "GOCACHE"} {
		if !strings.HasPrefix(env[k], "/var/lib/works/") {
			t.Errorf("%s=%q must live under /var/lib/works/", k, env[k])
		}
	}
	if !strings.Contains(env["PATH"], "/usr/local/go/bin") {
		t.Errorf("PATH=%q missing /usr/local/go/bin", env["PATH"])
	}
}

// TestPrepared_StringStable verifies the log-line format is stable for
// dashboards. Locks in the substring "sandbox{" so any future change
// surfaces in code review.
func TestPrepared_StringStable(t *testing.T) {
	t.Parallel()
	prep, err := sandbox.Prepare(context.Background(), "true", nil, sandbox.Manifest{
		ActionID: "log_test",
	}, sandbox.Options{ProbeNetwork: false, Root: t.TempDir()})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Cleanup(prep.Cleanup)

	s := prep.String()
	for _, want := range []string{"sandbox{", "workdir=", "network_blocked="} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q: %s", want, s)
		}
	}
}

// TestEndToEnd_SubprocessSeesScrubbedEnv wires the sandbox into a real
// exec.Cmd and asserts that the child process observes only the
// allow-listed env. This is the test that catches a regression in the
// worker.go wiring (slice-4 contract).
func TestEndToEnd_SubprocessSeesScrubbedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh not available on Windows test host")
	}
	t.Parallel()
	prep, err := sandbox.Prepare(context.Background(), "true", map[string]string{
		"SECRET_LEAK": "should-not-appear",
	}, sandbox.Manifest{
		ActionID:   "e2e",
		Network:    sandbox.NetworkDeny,
		Filesystem: sandbox.FSIsolated,
		Environment: map[string]string{
			"ALLOWED_VAR": "ok",
		},
	}, sandbox.Options{ProbeNetwork: false, Root: t.TempDir() + "/ws"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Cleanup(prep.Cleanup)

	// Sanity: the workdir should be empty when we start.
	runScript := `printf 'SECRET_LEAK=[%s]\nALLOWED_VAR=[%s]\nPATH=[%s]\nHOME=[%s]\n' "$SECRET_LEAK" "$ALLOWED_VAR" "$PATH" "$HOME"`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", runScript)
	cmd.Env = prep.Env
	cmd.Dir = prep.Workdir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("subprocess: %v\n%s", err, out.String())
	}

	s := out.String()
	// SECRET_LEAK must NOT reach the child. We accept either the literal
	// empty value ([SECRET_LEAK=]) or the literal "SECRET_LEAK=" (sh
	// semantics for unset vars differ slightly).
	if strings.Contains(s, "SECRET_LEAK=[should-not-appear]") {
		t.Errorf("SECRET_LEAK reached the subprocess: %s", s)
	}
	if strings.Contains(s, "SECRET_LEAK=should-not-appear") {
		t.Errorf("SECRET_LEAK reached the subprocess: %s", s)
	}
	if !strings.Contains(s, "ALLOWED_VAR=[ok]") {
		t.Errorf("ALLOWED_VAR missing in subprocess output: %s", s)
	}
	if !strings.Contains(s, "PATH=[") || strings.Contains(s, "PATH=[]") {
		t.Errorf("PATH missing or empty in subprocess output: %s", s)
	}
	if !strings.Contains(s, "HOME=[") || strings.Contains(s, "HOME=[]") {
		t.Errorf("HOME missing or empty in subprocess output: %s", s)
	}
}

// TestEndToEnd_CwdIsIsolated verifies the subprocess's pwd is the
// sandbox workdir, not the parent's cwd. This is the filesystem-
// isolation half of the Hermetic contract.
func TestEndToEnd_CwdIsIsolated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pwd not portable to Windows test host")
	}
	t.Parallel()
	parentCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	prep, err := sandbox.Prepare(context.Background(), "true", nil, sandbox.Manifest{
		ActionID:   "cwd",
		Filesystem: sandbox.FSIsolated,
	}, sandbox.Options{ProbeNetwork: false, Root: t.TempDir() + "/ws"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Cleanup(prep.Cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", "pwd")
	cmd.Env = prep.Env
	cmd.Dir = prep.Workdir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != prep.Workdir {
		t.Errorf("subprocess pwd = %q, want %q (parent cwd was %q)", got, prep.Workdir, parentCwd)
	}
}

// TestEndToEnd_WorkdirIsWiped verifies the workspace is fresh: a file
// dropped into the parent Root does not appear inside the per-attempt
// directory.
func TestEndToEnd_WorkdirIsWiped(t *testing.T) {
	t.Parallel()
	root := t.TempDir() + "/ws"
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Stale file from a previous attempt.
	if err := os.WriteFile(filepath.Join(root, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	prep, err := sandbox.Prepare(context.Background(), "true", nil, sandbox.Manifest{
		ActionID:   "fresh",
		Filesystem: sandbox.FSIsolated,
	}, sandbox.Options{ProbeNetwork: false, Root: root})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	t.Cleanup(prep.Cleanup)

	entries, err := os.ReadDir(prep.Workdir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "stale.txt" {
			t.Errorf("stale.txt leaked into fresh attempt workdir %s", prep.Workdir)
		}
	}
}

// --- helpers ---

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}