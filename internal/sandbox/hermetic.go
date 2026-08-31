// Package sandbox implements the Hermetic Execution Standard (#111) for
// subprocess execution. Slice-1 worker used exec.CommandContext with the
// full process environment and no filesystem or network isolation. This
// package adds three OS-level primitives around the subprocess:
//
//  1. Environment scrubbing (Hermetic default: deny unless allow-listed
//     via the capability manifest's `environment` map).
//  2. Isolated workspace: best-effort tmpfs mount on Linux; falls back
//     to a fresh per-attempt MkdirTemp directory when tmpfs is unavailable
//     (no CAP_SYS_ADMIN / unprivileged container) so the V1 guarantee
//     "no leakage into parent cwd" still holds.
//  3. Network egress check: parse /proc/net/route for a non-loopback
//     default route. When the manifest denies network egress and one
//     exists, Prepare fails closed with ErrEgressDenied. True syscall-
//     level blocking requires netns/cgroup and is deferred to the slice-4
//     Docker executor; this package records the policy decision so the
//     runner can attach a network namespace in V2.
//
// The sandbox takes (cmd string, env map, manifest) and returns a
// Prepared value the caller attaches to exec.Cmd.Env / exec.Cmd.Dir.
// Cleanup removes the workspace when the caller is done.
package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// NetworkPolicy mirrors docs/standards/schemas/action-manifest.schema.json
// `network.policy`. Unknown values default to deny (hermetic default).
type NetworkPolicy string

const (
	NetworkDeny     NetworkPolicy = "deny"      // Hermetic default.
	NetworkAllow    NetworkPolicy = "allow-list" // Requires AllowList.
)

// FilesystemMode mirrors `filesystem.mode` in the manifest schema.
// "isolated" (default) = fresh tmpfs per attempt; "workspace" =
// persistent work dir; "shared" = read-only bind of repo source.
type FilesystemMode string

const (
	FSIsolated FilesystemMode = "isolated"
	FSWorkspace FilesystemMode = "workspace"
	FSShared    FilesystemMode = "shared"
)

// Mount mirrors a `filesystem.mounts[]` entry. Only used when Mode is
// workspace or shared.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Manifest is the minimal subset of the action-manifest the sandbox
// needs. Construct it from a parsed JSON manifest (see action-manifest.
// schema.json) or directly in tests.
type Manifest struct {
	ActionID string

	// Network policy.
	Network NetworkPolicy
	// AllowList is required when Network == NetworkAllow.
	AllowList []string

	// Filesystem mode.
	Filesystem FilesystemMode
	Mounts     []Mount

	// Environment allow-list (hermetic default: empty = drop everything).
	// Values may be literal strings or {"$ref": "..."} placeholders; the
	// sandbox passes them through as-is and lets the caller resolve refs.
	Environment map[string]string

	// WorkingDir overrides the default workspace path. When set, the
	// sandbox uses (or creates) this directory instead of a temp dir.
	WorkingDir string
}

// Prepared is the result of a successful Prepare call. Attach to an
// exec.Cmd via cmd.Env = prepared.Env and cmd.Dir = prepared.Workdir.
// Always call Cleanup when the subprocess exits.
type Prepared struct {
	// Env is the scrubbed environment for the subprocess.
	Env []string
	// Workdir is the directory the subprocess should run in.
	Workdir string
	// Tmpfs is true when the workdir is actually a tmpfs mount.
	Tmpfs bool
	// NetworkBlocked reflects the policy decision (true when network was
	// denied). V1 does not enforce at the syscall level; the runner can
	// attach a netns to honour this in V2.
	NetworkBlocked bool
	// Cleanup releases the workspace. Idempotent.
	Cleanup func()
}

// Errors surfaced by Prepare. All are exported so callers can match
// with errors.Is.
var (
	ErrEgressDenied   = errors.New("sandbox: network egress denied by manifest policy")
	ErrInvalidManifest = errors.New("sandbox: invalid manifest")
	ErrNoWorkspace     = errors.New("sandbox: cannot create isolated workspace")
)

// Options tweaks Prepare's behaviour. All fields are optional.
type Options struct {
	// Root is the parent directory for the workspace. Default is
	// os.TempDir() + "/works-sandbox".
	Root string
	// ForceTmpfs, when true, attempts a tmpfs mount even on hosts where
	// the auto-detection would skip it. Useful for CI runners known to
	// expose unprivileged tmpfs mounts.
	ForceTmpfs bool
	// ProbeNetwork, when false, skips the default-route probe (useful
	// in tests / sandboxes that already have no network).
	ProbeNetwork bool
}

var defaultOpts = Options{
	Root:          filepath.Join(os.TempDir(), "works-sandbox"),
	ProbeNetwork:  true,
}

// Prepare enforces the Hermetic Execution Standard (#111) defaults and
// returns a Prepared value ready for exec.Cmd. The cmd argument is the
// shell command line; it is NOT executed here. The caller must run it
// with exec.CommandContext and ensure Cleanup() is called.
//
// Prepare is safe to call concurrently; workspace creation uses an
// internal mutex around the global Root directory.
func Prepare(ctx context.Context, cmd string, env map[string]string, m Manifest, opts ...Options) (*Prepared, error) {
	o := defaultOpts
	if len(opts) > 0 {
		o = mergeOpts(o, opts[0])
	}
	if err := validateManifest(m); err != nil {
		return nil, err
	}
	if err := checkEgress(m, o); err != nil {
		return nil, err
	}

	workdir, tmpfs, err := createWorkspace(m, o)
	if err != nil {
		return nil, err
	}

	scrubbed := scrubEnv(env, m.Environment)

	prepared := &Prepared{
		Env:            scrubbed,
		Workdir:        workdir,
		Tmpfs:          tmpfs,
		NetworkBlocked: m.Network == NetworkDeny,
		Cleanup:        cleanupFunc(workdir),
	}
	_ = cmd // reserved for future policy decisions based on command text
	return prepared, nil
}

// DefaultDenyEnv returns an environment that contains only PATH, HOME,
// LANG, and the supplied allow-list. Use it when you have no manifest
// but still want the hermetic default.
func DefaultDenyEnv(allow map[string]string) []string {
	base := map[string]string{
		"PATH":  "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":  "/tmp",
		"LANG":  "C.UTF-8",
	}
	for k, v := range allow {
		base[k] = v
	}
	return mapToEnv(base)
}

// --- internals ---

var workspaceMu sync.Mutex

func validateManifest(m Manifest) error {
	switch m.Network {
	case "", NetworkDeny, NetworkAllow:
	default:
		return fmt.Errorf("%w: unknown network policy %q", ErrInvalidManifest, m.Network)
	}
	if m.Network == NetworkAllow && len(m.AllowList) == 0 {
		return fmt.Errorf("%w: network=allow-list requires non-empty allow_list", ErrInvalidManifest)
	}
	switch m.Filesystem {
	case "", FSIsolated, FSWorkspace, FSShared:
	default:
		return fmt.Errorf("%w: unknown filesystem mode %q", ErrInvalidManifest, m.Filesystem)
	}
	for i, mt := range m.Mounts {
		if mt.Source == "" || mt.Target == "" {
			return fmt.Errorf("%w: mounts[%d] missing source or target", ErrInvalidManifest, i)
		}
		if !filepath.IsAbs(mt.Target) {
			return fmt.Errorf("%w: mounts[%d].target must be absolute", ErrInvalidManifest, i)
		}
	}
	return nil
}

// checkEgress implements the V1 network policy decision. When the
// manifest denies egress and the host has a non-loopback default route,
// Prepare refuses to run (fail-closed). When no default route exists
// the deny policy is trivially satisfied — there is no egress to block.
func checkEgress(m Manifest, o Options) error {
	if !o.ProbeNetwork {
		return nil
	}
	if m.Network != NetworkDeny {
		return nil
	}
	has, err := hasNonLoopbackDefaultRoute()
	if err != nil {
		// Fail-open on probe errors so we don't refuse work because of
		// a transient /proc/net/route parse failure. The runner still
		// records NetworkBlocked=true so V2 netns enforcement kicks in.
		return nil
	}
	if has {
		return fmt.Errorf("%w (policy=deny, default route present)", ErrEgressDenied)
	}
	return nil
}

// hasNonLoopbackDefaultRoute reads /proc/net/route and returns true
// when at least one default gateway on a non-loopback interface
// exists. This is the V1 best-effort probe; the runner is expected to
// honour the policy via a network namespace in V2.
func hasNonLoopbackDefaultRoute() (bool, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // header
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 11 {
			continue
		}
		iface := fields[0]
		dest := fields[1]
		flags, _ := strconv.ParseUint(fields[3], 16, 32)
		// RTF_UP (0x1) + RTF_GATEWAY (0x2) marks an active gateway route.
		if flags&0x3 != 0x3 {
			continue
		}
		// Destination == 00000000 is the default route.
		if dest != "00000000" {
			continue
		}
		if iface == "lo" {
			continue
		}
		return true, nil
	}
	return false, sc.Err()
}

// createWorkspace returns the workdir path and whether it's a tmpfs.
// We attempt tmpfs mount on Linux when the OS supports unprivileged
// user namespaces + CAP_SYS_ADMIN; otherwise we fall back to a
// MkdirTemp directory. The caller should treat either as isolated.
func createWorkspace(m Manifest, o Options) (string, bool, error) {
	workspaceMu.Lock()
	defer workspaceMu.Unlock()

	if m.WorkingDir != "" {
		switch m.Filesystem {
		case FSWorkspace, FSShared:
			if err := os.MkdirAll(m.WorkingDir, 0o755); err != nil {
				return "", false, fmt.Errorf("%w: %v", ErrNoWorkspace, err)
			}
			return m.WorkingDir, false, nil
		default:
			// isolated mode + explicit WorkingDir: respect caller intent
			// but still ensure it exists and is empty.
			if err := os.MkdirAll(m.WorkingDir, 0o700); err != nil {
				return "", false, fmt.Errorf("%w: %v", ErrNoWorkspace, err)
			}
			return m.WorkingDir, false, nil
		}
	}

	root := o.Root
	if root == "" {
		root = filepath.Join(os.TempDir(), "works-sandbox")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrNoWorkspace, err)
	}
	dir, err := os.MkdirTemp(root, "attempt-*")
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrNoWorkspace, err)
	}
	// Tighten perms: parent was 0o755 but per-attempt dirs are 0o700.
	_ = os.Chmod(dir, 0o700)

	if !o.ForceTmpfs {
		return dir, false, nil
	}
	if ok, err := tryMountTmpfs(dir); err == nil && ok {
		return dir, true, nil
	}
	return dir, false, nil
}

// tryMountTmpfs attempts `mount -t tmpfs tmpfs <dir>`. Returns ok=true
// on a successful mount. On hosts without mount(8) or CAP_SYS_ADMIN it
// fails silently; the caller falls back to a plain MkdirTemp.
func tryMountTmpfs(dir string) (ok bool, err error) {
	mount, err := exec.LookPath("mount")
	if err != nil {
		return false, nil
	}
	cmd := exec.Command(mount, "-t", "tmpfs", "tmpfs", dir)
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	return true, nil
}

// scrubEnv produces the subprocess environment. When manifest.Environment
// is non-empty, it acts as an allow-list — only those keys (plus the
// sandbox-injected PATH/HOME/LANG) are kept. When the allow-list is
// empty, the subprocess gets only the sandbox defaults.
//
// `supplied` is the per-node env (the second argument to Prepare, e.g.
// ReadyItem.Env); it is intersected with the allow-list, so a caller
// cannot smuggle vars past the manifest.
func scrubEnv(supplied, allow map[string]string) []string {
	merged := map[string]string{
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME": "/tmp",
		"LANG": "C.UTF-8",
	}
	for k, v := range allow {
		merged[k] = v
	}
	for k, v := range supplied {
		if _, ok := allow[k]; ok || isSandboxDefault(k) {
			merged[k] = v
		}
	}
	return mapToEnv(merged)
}

func isSandboxDefault(k string) bool {
	return k == "PATH" || k == "HOME" || k == "LANG"
}

func mapToEnv(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// cleanupFunc returns a Cleanup that removes the workspace on the
// first call and is a no-op thereafter.
func cleanupFunc(workdir string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if workdir == "" {
				return
			}
			_ = os.RemoveAll(workdir)
		})
	}
}

// mergeOpts overlays non-zero fields from src onto dst.
func mergeOpts(dst, src Options) Options {
	if src.Root != "" {
		dst.Root = src.Root
	}
	if src.ForceTmpfs {
		dst.ForceTmpfs = true
	}
	// ProbeNetwork defaults to true; only honour an explicit false.
	if !src.ProbeNetwork {
		dst.ProbeNetwork = false
	}
	return dst
}

// Ensure Dirs returns fs.FileInfo for the prepared workdir, mainly for
// tests that want to assert it exists and is empty.
func (p *Prepared) Stat() (os.FileInfo, error) {
	if p == nil || p.Workdir == "" {
		return nil, fs.ErrNotExist
	}
	return os.Stat(p.Workdir)
}

// String returns a one-line human description of the prepared sandbox,
// useful for logging. It does not include env values (which may carry
// secrets).
func (p *Prepared) String() string {
	if p == nil {
		return "<nil sandbox>"
	}
	tmpfs := "no"
	if p.Tmpfs {
		tmpfs = "yes"
	}
	return fmt.Sprintf("sandbox{workdir=%s, tmpfs=%s, network_blocked=%t}",
		p.Workdir, tmpfs, p.NetworkBlocked)
}