package worker

// k-057 test coverage: ADR-0022 execution-time REF resolution on the
// production worker path (internal/worker, ADR-0022).

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/sandbox"
)

const (
	// sentinel is a fake secret VALUE. ADR-0022 says a value must never
	// appear in an error message; the never-leak sweep asserts this
	// string is absent from every failure path.
	sentinel = "super-secret-xyz"
	// sentinelRef is the inert REF pointing at it (safe to log).
	sentinelRef = "secret://env/token"
)

func TestResolveItemEnv(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		env     map[string]string
		want    map[string]string
		wantErr string // substring; "" means no error expected
	}{
		{
			name:  "ref resolves against process env",
			setup: func(t *testing.T) { t.Setenv("SECRET_ENV_TOKEN", sentinel) },
			env:   map[string]string{"MY_TOKEN": sentinelRef},
			want:  map[string]string{"MY_TOKEN": sentinel},
		},
		{
			name:    "missing backing var fails, naming the ref",
			setup:   func(t *testing.T) { t.Setenv("SECRET_ENV_TOKEN", sentinel) },
			env:     map[string]string{"X": "secret://env/no_such_entry_k057"},
			wantErr: "secret://env/no_such_entry_k057",
		},
		{
			name:    "present-but-empty backing var fails closed",
			setup:   func(t *testing.T) { t.Setenv("SECRET_ENV_EMPTYK057", "") },
			env:     map[string]string{"X": "secret://env/emptyk057"},
			wantErr: "secret://env/emptyk057",
		},
		{
			name:    "malformed ref fails, naming the ref",
			env:     map[string]string{"X": "secret://BAD-Provider/../oops"},
			wantErr: "secret://BAD-Provider/../oops",
		},
		{
			name: "non-refs untouched",
			env:  map[string]string{"A": "plain", "B": "", "C": "https://example.com/x", "D": "prefix-secret://not-a-ref"},
			want: map[string]string{"A": "plain", "B": "", "C": "https://example.com/x", "D": "prefix-secret://not-a-ref"},
		},
		{
			name:  "mixed refs and literals: only refs rewritten",
			setup: func(t *testing.T) { t.Setenv("SECRET_ENV_TOKEN", sentinel) },
			env:   map[string]string{"PLAIN": "keep-me", "MY_TOKEN": sentinelRef},
			want:  map[string]string{"PLAIN": "keep-me", "MY_TOKEN": sentinel},
		},
		{
			name: "empty map passes through",
			env:  map[string]string{},
			want: map[string]string{},
		},
		{
			name: "nil map passes through",
			env:  nil,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}
			orig := map[string]string{}
			for k, v := range tt.env {
				orig[k] = v
			}
			got, err := resolveItemEnv(context.Background(), tt.env)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error naming %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not name ref %q", err.Error(), tt.wantErr)
				}
				if got != nil {
					t.Fatalf("failed resolution must return nil map, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("resolveItemEnv()=%v want %v", got, tt.want)
			}
			// The caller's map is never mutated: ReadyItem (and the
			// evidence paths that can reach it) must keep seeing refs.
			for k, v := range orig {
				if tt.env[k] != v {
					t.Fatalf("input map mutated at key %q: %q -> %q", k, v, tt.env[k])
				}
			}
		})
	}
}

// TestResolveItemEnv_FastPathReturnsSameMap pins the backward-compat
// interlock: with no refs present the pure helper returns the identical
// map header (no copy, no allocation), so downstream cmd.Env assembly is
// byte-for-byte the pre-k057 code path.
func TestResolveItemEnv_FastPathReturnsSameMap(t *testing.T) {
	env := map[string]string{"FOO": "bar", "EMPTY": "", "URL": "secretless"}
	got, err := resolveItemEnv(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(env).Pointer() {
		t.Fatal("no-ref fast path must return the same map, not a copy")
	}
}

// TestResolveItemEnv_NeverLeaksSentinel sweeps every failure path and
// asserts no error string contains the sentinel VALUE while a real value
// is present in the process env. Errors may name the REF (inert); a VALUE
// crossing into an error message violates ADR-0022.
func TestResolveItemEnv_NeverLeaksSentinel(t *testing.T) {
	t.Setenv("SECRET_ENV_TOKEN", sentinel)
	t.Setenv("SECRET_ENV_EMPTYK057", "")

	failingEnvs := []map[string]string{
		// Backing var missing entirely.
		{"X": "secret://env/no_such_entry_k057"},
		// Backing var present but empty (treated as not found).
		{"X": "secret://env/emptyk057"},
		// Malformed refs of several shapes.
		{"X": "secret://"},
		{"X": "secret://env"},
		{"X": "secret://ENV/TOKEN"},
		{"X": "secret://env/token/extra"},
		{"X": "secret://env/"},
		// Valid ref mixed with failures.
		{"PLAIN": "value", "A": "secret://env/no_such_entry_k057", "B": sentinelRef, "C": "secret://env/emptyk057"},
	}
	for _, env := range failingEnvs {
		resolved, err := resolveItemEnv(context.Background(), env)
		if err == nil {
			t.Fatalf("expected failure for env %v, got %v", env, resolved)
		}
		if resolved != nil {
			t.Fatalf("failure must return nil map, got %v", resolved)
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("ADR-0022 violation: error leaks the sentinel value: %v", err)
		}
	}

	// Sanity: the success path itself resolves and still never errors,
	// so there is no error string that could carry the value.
	got, err := resolveItemEnv(context.Background(), map[string]string{"MY_TOKEN": sentinelRef})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["MY_TOKEN"] != sentinel {
		t.Fatalf("expected sentinel to resolve, got %q", got["MY_TOKEN"])
	}
}

// TestRunCommand_ResolvesSecretRefEndToEnd proves the wiring reaches
// cmd.Env in the REAL worker exec path: a ReadyItem-style env carrying a
// ref, executed through runCommand's legacy path, must hand the child the
// resolved value.
func TestRunCommand_ResolvesSecretRefEndToEnd(t *testing.T) {
	t.Setenv("SECRET_ENV_TOKEN", sentinel)
	res := runCommand(context.Background(), "printenv MY_TOKEN",
		map[string]string{"MY_TOKEN": sentinelRef}, 10*time.Second, nil, "", nil)
	if res.Status != "succeeded" || res.ExitCode != 0 {
		t.Fatalf("status=%s exit=%d log=%q", res.Status, res.ExitCode, res.CombinedLog)
	}
	if got := strings.TrimSpace(string(res.CombinedLog)); got != sentinel {
		t.Fatalf("child saw %q, want resolved sentinel", got)
	}
}

// TestRunCommand_ResolvesSecretRefSandboxPath pins that the
// manifest!=nil (hermetic sandbox) branch also sees resolved values: the
// resolution happens once at the top of runCommand, before the
// sandbox/legacy branch, so prepared.Env carries the value (allow-listed
// by the manifest).
func TestRunCommand_ResolvesSecretRefSandboxPath(t *testing.T) {
	t.Setenv("SECRET_ENV_TOKEN", sentinel)
	m := &sandbox.Manifest{
		ActionID:    "k057-test",
		Filesystem:  sandbox.FSIsolated,
		Environment: map[string]string{"MY_TOKEN": ""},
	}
	res := runCommand(context.Background(), "printenv MY_TOKEN",
		map[string]string{"MY_TOKEN": sentinelRef}, 10*time.Second, nil, "", m)
	if res.Status != "succeeded" || res.ExitCode != 0 {
		t.Fatalf("status=%s exit=%d log=%q", res.Status, res.ExitCode, res.CombinedLog)
	}
	if got := strings.TrimSpace(string(res.CombinedLog)); got != sentinel {
		t.Fatalf("sandboxed child saw %q, want resolved sentinel", got)
	}
}

// TestRunCommand_UnresolvedRefFailsWithoutExec proves fail-closed: a ref
// that does not resolve must produce the 'secret resolution failed'
// failure result (naming the REF) and must NOT start the subprocess.
func TestRunCommand_UnresolvedRefFailsWithoutExec(t *testing.T) {
	t.Setenv("SECRET_ENV_TOKEN", sentinel) // unrelated value in env
	dir := t.TempDir()
	marker := dir + "/executed"
	env := map[string]string{"X": "secret://env/no_such_entry_k057"}
	res := runCommand(context.Background(), "touch "+marker, env, 10*time.Second, nil, dir, nil)
	if res.Status != "failed" || res.ExitCode != -1 {
		t.Fatalf("status=%s exit=%d, want failed/-1 (log=%q)", res.Status, res.ExitCode, res.CombinedLog)
	}
	log := string(res.CombinedLog)
	if !strings.Contains(log, "secret://env/no_such_entry_k057") {
		t.Fatalf("failure log must name the REF, got %q", log)
	}
	if strings.Contains(log, sentinel) {
		t.Fatalf("failure log leaks a value: %q", log)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("subprocess executed despite unresolved ref")
	}
}

// TestRunCommand_LiteralEnvZeroRegression checks the common case through
// the real exec function: an env with only literals still produces the
// legacy cmd.Env shape (full process env + caller overrides) with values
// passed through untouched.
func TestRunCommand_LiteralEnvZeroRegression(t *testing.T) {
	t.Setenv("K057_PARENT_ONLY", "inherited")
	env := map[string]string{"K057_A": "one", "K057_B": "two"}
	res := runCommand(context.Background(),
		`printf '%s\n' "K057_A=$K057_A" "K057_B=$K057_B" "K057_PARENT_ONLY=$K057_PARENT_ONLY" "PATH_SET=$([ -n "$PATH" ] && echo yes || echo no)"`,
		env, 10*time.Second, nil, "", nil)
	if res.Status != "succeeded" || res.ExitCode != 0 {
		t.Fatalf("status=%s exit=%d log=%q", res.Status, res.ExitCode, res.CombinedLog)
	}
	log := string(res.CombinedLog)
	for _, want := range []string{"K057_A=one", "K057_B=two", "K057_PARENT_ONLY=inherited", "PATH_SET=yes"} {
		if !strings.Contains(log, want) {
			t.Fatalf("child env missing %q; got %q", want, log)
		}
	}
	// PATH_SET=yes proves the full os.Environ() base is preserved.
}
