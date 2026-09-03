package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

// The ADR-0022 laws under test:
//   - only refs cross trust boundaries; a value exists solely in cmd.Env
//   - a value never appears in an error string produced by this package
//   - a nil resolver is byte-for-byte legacy behavior (pass-through)

// leakSentinel is the fake VALUE used by the failure-path sweeps. It is
// placed in the fake env on purpose so that any accidental echo of a
// resolved value into an error or into a Result is caught.
const leakSentinel = "super-secret-xyz"

// benignExecValue is the value used by the single exec-level success test.
// It is not a secret and it is asserted to REACH the child, which is the
// whole point of the wiring.
const benignExecValue = "exec-test-value-123"

// fakeLookup adapts a map to the kernel's LookupEnv shape.
func fakeLookup(m map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func TestResolveEnv_Table(t *testing.T) {
	base := map[string]string{
		"SECRET_ENV_TOKEN":  leakSentinel,
		"SECRET_VAULT_DB_P": "vault-val",
		// Scope suffix form: "__<SCOPE>" (uppercased, ':' -> '_').
		"SECRET_ENV_SCOPED__WORK_ABC": "scoped-val",
		// Present but empty: the kernel treats it as missing, so it must
		// fail closed rather than resolve to "".
		"SECRET_ENV_BLANK": "",
	}
	tests := []struct {
		name    string
		scope   string // resolver default scope
		callS   string // scope passed to ResolveEnv (wins over default)
		env     map[string]string
		want    map[string]string
		wantRef string // if non-empty, expect an error naming this ref
		wantIs  error
	}{
		{
			name:  "single ref resolves",
			env:   map[string]string{"TOKEN": "secret://env/token"},
			want:  map[string]string{"TOKEN": leakSentinel},
			scope: "",
		},
		{
			name: "refs and literals mix, literals untouched",
			env: map[string]string{
				"GOFLAGS": "-mod=readonly",
				"TOKEN":   "secret://env/token",
				"EMPTY":   "",
				"PLAIN":   "not-a-ref",
			},
			want: map[string]string{
				"GOFLAGS": "-mod=readonly",
				"TOKEN":   leakSentinel,
				"EMPTY":   "",
				"PLAIN":   "not-a-ref",
			},
		},
		{
			name: "no refs at all is identity",
			env:  map[string]string{"A": "1", "B": "2"},
			want: map[string]string{"A": "1", "B": "2"},
		},
		{
			name: "empty env",
			env:  map[string]string{},
			want: map[string]string{},
		},
		{
			name:  "call scope wins over default scope",
			scope: "ignored",
			callS: "work:abc",
			env:   map[string]string{"S": "secret://env/scoped"},
			want:  map[string]string{"S": "scoped-val"},
		},
		{
			name:  "default scope used when call scope empty",
			scope: "work:abc",
			env:   map[string]string{"S": "secret://env/scoped"},
			want:  map[string]string{"S": "scoped-val"},
		},
		{
			name:    "missing env var fails closed with ref only",
			env:     map[string]string{"TOKEN": "secret://env/nope"},
			wantRef: "secret://env/nope",
			wantIs:  secrets.ErrSecretNotFound,
		},
		{
			name: "present-but-empty env var fails closed",
			env:  map[string]string{"BLANK": "secret://env/blank"},
			// Distinct from "missing" only in the lookup: SECRET_ENV_BLANK
			// exists with an empty value and must resolve to not-found.
			wantRef: "secret://env/blank",
			wantIs:  secrets.ErrSecretNotFound,
		},
		{
			name:    "malformed ref (uppercase provider) fails closed",
			env:     map[string]string{"TOKEN": "secret://Env/Token"},
			wantRef: "secret://Env/Token",
		},
		{
			name:    "malformed ref (extra segment) fails closed",
			env:     map[string]string{"TOKEN": "secret://env/a/b"},
			wantRef: "secret://env/a/b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewEnvSecretResolver(tc.scope, fakeLookup(base))
			got, err := r.ResolveEnv(context.Background(), tc.callS, tc.env)
			if tc.wantRef != "" {
				if err == nil {
					t.Fatalf("want error naming %q, got map %v", tc.wantRef, got)
				}
				if got != nil {
					t.Errorf("on error the map must be nil, got %v", got)
				}
				if !strings.Contains(err.Error(), tc.wantRef) {
					t.Errorf("error must name the ref %q: %v", tc.wantRef, err)
				}
				if !errors.Is(err, ErrSecretRefUnresolved) {
					t.Errorf("error must wrap ErrSecretRefUnresolved: %v", err)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Errorf("error must wrap %v: %v", tc.wantIs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveEnv: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: got %q want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestResolveEnv_DoesNotMutateInput pins the law that resolution never
// writes a value back into the caller's step env map: the Step belongs to a
// plan that outlives the step and is reachable from evidence paths.
func TestResolveEnv_DoesNotMutateInput(t *testing.T) {
	in := map[string]string{"TOKEN": "secret://env/token"}
	r := NewEnvSecretResolver("", fakeLookup(map[string]string{"SECRET_ENV_TOKEN": leakSentinel}))
	if _, err := r.ResolveEnv(context.Background(), "", in); err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if in["TOKEN"] != "secret://env/token" {
		t.Errorf("input map mutated: TOKEN=%q", in["TOKEN"])
	}
}

// TestResolveEnv_DefaultLookupIsProcessEnv covers the nil-lookup branch.
func TestResolveEnv_DefaultLookupIsProcessEnv(t *testing.T) {
	const key = "SECRET_ENV_K051_PROBE"
	t.Setenv(key, "from-process-env")
	t.Cleanup(func() { _ = os.Unsetenv(key) })
	r := NewEnvSecretResolver("", nil)
	got, err := r.ResolveEnv(context.Background(), "", map[string]string{"P": "secret://env/k051_probe"})
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if got["P"] != "from-process-env" {
		t.Errorf("got %q, want the process env value", got["P"])
	}
}

// TestResolveEnv_NoValueLeaks is the hard-invariant sweep: with the
// sentinel present and resolvable, force every failure path and assert no
// error string ever contains it.
func TestResolveEnv_NoValueLeaks(t *testing.T) {
	fake := map[string]string{
		"SECRET_ENV_TOKEN":     leakSentinel,
		"SECRET_VAULT_DB_PASS": leakSentinel,
		"SECRET_ENV_BLANK":     "", // present but empty
	}
	failingEnvs := []map[string]string{
		// Succeeds on the sentinel key, then fails on a missing one:
		// the error must not echo anything resolved earlier in the call.
		{"OK": "secret://env/token", "BAD": "secret://env/missing"},
		{"OK": "secret://vault/db-pass", "BAD": "secret://env/also-missing"},
		// Present-but-empty: must not distinguish itself from missing.
		{"OK": "secret://env/token", "BLANK": "secret://env/blank"},
		// Malformed refs.
		{"OK": "secret://env/token", "BAD": "secret://ENV/TOKEN"},
		{"OK": "secret://env/token", "BAD": "secret://env/"},
		{"OK": "secret://env/token", "BAD": "secret:///token"},
		{"OK": "secret://env/token", "BAD": "secret://env/tok en"},
		// Order reversed: failure first.
		{"BAD": "secret://env/missing", "OK": "secret://env/token"},
	}
	for i, env := range failingEnvs {
		for _, scope := range []string{"", "work:abc"} {
			r := NewEnvSecretResolver(scope, fakeLookup(fake))
			_, err := r.ResolveEnv(context.Background(), "", env)
			if err == nil {
				t.Fatalf("case %d scope %q: expected failure, env=%v", i, scope, env)
			}
			msg := err.Error()
			if strings.Contains(msg, leakSentinel) {
				t.Errorf("case %d: error leaks the value: %v", i, err)
			}
			// The error must point at a ref (the offending one). Which ref
			// depends on map iteration order, so the honest assertion is
			// "it names one of them".
			named := 0
			for _, v := range env {
				if strings.Contains(msg, v) {
					named++
				}
			}
			if named == 0 {
				t.Errorf("case %d: error names no ref from %v: %v", i, env, err)
			}
		}
	}
}

// TestRun_ResolvesRefIntoChildEnv is the exec-level proof that the wiring
// reaches cmd.Env: printenv (a real subprocess) sees the resolved value.
func TestRun_ResolvesRefIntoChildEnv(t *testing.T) {
	skipIfNoGo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := Run(ctx, Options{
		Workdir: t.TempDir(),
		Stack:   StackGo,
		SecretResolver: NewEnvSecretResolver("", fakeLookup(map[string]string{
			"SECRET_ENV_TOKEN": benignExecValue,
		})),
		PlanOverride: []Step{
			{
				Name:    "printenv",
				Cmd:     "printenv",
				Args:    []string{"TEST_REF"},
				Env:     map[string]string{"TEST_REF": "secret://env/token"},
				Timeout: 5 * time.Second,
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed {
		t.Fatalf("run failed: %s", FormatResult(res))
	}
	if len(res.Steps) != 1 {
		t.Fatalf("steps: %+v", res.Steps)
	}
	if got := strings.TrimSpace(res.Steps[0].Stdout); got != benignExecValue {
		t.Errorf("child env: got %q, want %q", got, benignExecValue)
	}
}

// TestRun_NilResolverIsLegacyPassThrough pins the backward-compat
// interlock: without a resolver nothing changes, the ref string reaches the
// child literally, exactly as before this feature existed.
func TestRun_NilResolverIsLegacyPassThrough(t *testing.T) {
	skipIfNoGo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	want := "secret://env/token"
	res, err := Run(ctx, Options{
		Workdir: t.TempDir(),
		Stack:   StackGo,
		PlanOverride: []Step{
			{
				Name:    "printenv",
				Cmd:     "printenv",
				Args:    []string{"TEST_REF"},
				Env:     map[string]string{"TEST_REF": want},
				Timeout: 5 * time.Second,
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed {
		t.Fatalf("run failed: %s", FormatResult(res))
	}
	if got := strings.TrimSpace(res.Steps[0].Stdout); got != want {
		t.Errorf("legacy pass-through: got %q, want %q", got, want)
	}
}

// TestRun_UnresolvableRefFailsStep: a resolution failure surfaces through
// the existing Run semantics (Failed + FailedStep + break-on-failure) and
// carries the ref, never the value, anywhere in the Result.
func TestRun_UnresolvableRefFailsStep(t *testing.T) {
	skipIfNoGo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ref := "secret://env/missing"
	envWithRef := map[string]string{"TEST_REF": ref}
	res, err := Run(ctx, Options{
		Workdir: t.TempDir(),
		Stack:   StackGo,
		// The sentinel is resolvable; the second ref is not. The failing
		// step's error must not echo the value resolved for its sibling.
		SecretResolver: NewEnvSecretResolver("", fakeLookup(map[string]string{
			"SECRET_ENV_TOKEN": leakSentinel,
		})),
		PlanOverride: []Step{
			{
				Name:    "ok-step",
				Cmd:     "printenv",
				Args:    []string{"TEST_TOKEN"},
				Env:     map[string]string{"TEST_TOKEN": "secret://env/token"},
				Timeout: 5 * time.Second,
			},
			{
				Name:    "bad-step",
				Cmd:     "printenv",
				Args:    []string{"TEST_REF"},
				Env:     envWithRef,
				Timeout: 5 * time.Second,
			},
			{
				Name:    "never-run",
				Cmd:     "printenv",
				Timeout: 5 * time.Second,
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Failed {
		t.Errorf("Failed: want true, got %s", FormatResult(res))
	}
	if res.FailedStep != "bad-step" {
		t.Errorf("FailedStep: want %q, got %q", "bad-step", res.FailedStep)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("break-on-failure must stop the plan, got steps: %+v", res.Steps)
	}
	if res.Steps[0].Name != "ok-step" || res.Steps[0].ExitCode != 0 {
		t.Errorf("earlier step unaffected: %+v", res.Steps[0])
	}
	if res.Steps[0].Stdout != leakSentinel+"\n" {
		t.Errorf("ok-step stdout: %q", res.Steps[0].Stdout)
	}
	bad := res.Steps[1]
	if bad.ExitCode == 0 {
		t.Errorf("bad-step must not exit 0: %+v", bad)
	}
	if !strings.Contains(bad.Stderr, ref) {
		t.Errorf("bad-step stderr must name the ref %q: %q", ref, bad.Stderr)
	}
	if !strings.Contains(bad.Stderr, "not found") {
		t.Errorf("bad-step stderr must say why: %q", bad.Stderr)
	}
	// Hard invariant: no resolved value anywhere in the serialized Result,
	// except the legitimate child-stdout of the step that printed it itself.
	clone := *res
	clone.Steps = append([]StepResult(nil), res.Steps...)
	clone.Steps[0].Stdout = "" // the subprocess echoing its own env
	clone.Steps[0].Stderr = ""
	b, err := json.Marshal(clone)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), leakSentinel) {
		t.Errorf("Result JSON leaks the value: %s", b)
	}
	// And the failing step's plan entry is untouched: still a ref.
	if envWithRef["TEST_REF"] != ref {
		t.Errorf("plan env mutated: %q", envWithRef["TEST_REF"])
	}
}

// fakeFailingResolver proves Run surfaces a resolver error rather than
// swallowing it, without depending on the env kernel for the failure.
type fakeFailingResolver struct{ err error }

func (f fakeFailingResolver) ResolveEnv(context.Context, string, map[string]string) (map[string]string, error) {
	return nil, f.err
}

func TestRun_ResolverErrorSurfaces(t *testing.T) {
	skipIfNoGo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := Run(ctx, Options{
		Workdir:        t.TempDir(),
		Stack:          StackGo,
		SecretScope:    "work:42",
		SecretResolver: fakeFailingResolver{err: errors.New("boom: secret://env/token")},
		PlanOverride: []Step{{
			Name:    "s",
			Cmd:     "printenv",
			Env:     map[string]string{"A": "secret://env/token"},
			Timeout: 5 * time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Failed || res.FailedStep != "s" {
		t.Fatalf("want failed step s, got %s", FormatResult(res))
	}
	if res.Steps[0].ExitCode != -1 {
		t.Errorf("exit code: want -1, got %d", res.Steps[0].ExitCode)
	}
	if !strings.Contains(res.Steps[0].Stderr, "boom") {
		t.Errorf("stderr: %q", res.Steps[0].Stderr)
	}
}

// TestIsSecretRef checks the cheap gate that decides what is inert plan data.
func TestIsSecretRef(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"secret://env/token", true},
		{"secret://", true}, // prefix gate only; ParseRef rejects the shape
		{"", false},
		{"https://example.com", false},
		{"SECRET://env/token", false},
		{" secret://env/token", false},
	}
	for _, tc := range tests {
		if got := IsSecretRef(tc.in); got != tc.want {
			t.Errorf("IsSecretRef(%q): got %v want %v", tc.in, got, tc.want)
		}
	}
}
