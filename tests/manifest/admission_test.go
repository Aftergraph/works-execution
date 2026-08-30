// Package manifest_test covers admission control for action-manifest fields.
//
// Tests run against the pure-Go ValidateAndEnrich path; they do not touch
// HTTP, the store, or workers. The package import is intentionally an
// external test (package manifest_test) so we exercise the public API.
package manifest_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/manifest"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// nodeWork builds a minimal work with one node the test can mutate. It is
// the canonical starting point; each test customises n before calling
// manifest.ValidateAndEnrich.
func nodeWork(n workgraph.Node) *workgraph.Work {
	return &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{n.ID: n},
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func TestValidateAndEnrich_FillsDefaults(t *testing.T) {
	// Caller omits all capability fields. Admission must fill them.
	w := nodeWork(workgraph.Node{ID: "build", Run: "go build ./..."})

	if err := manifest.ValidateAndEnrich(w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := w.Graph.Nodes["build"]

	if got.TimeoutS != manifest.DefaultTimeoutSeconds {
		t.Errorf("timeout: got %d, want %d", got.TimeoutS, manifest.DefaultTimeoutSeconds)
	}
	if got.Retries == nil {
		t.Fatal("retries: expected default to be filled, got nil")
	}
	if got.Retries.MaxAttempts != manifest.DefaultRetryMaxAttempts {
		t.Errorf("retries.max_attempts: got %d, want %d",
			got.Retries.MaxAttempts, manifest.DefaultRetryMaxAttempts)
	}
	if got.Retries.Backoff != manifest.DefaultBackoff {
		t.Errorf("retries.backoff: got %q, want %q",
			got.Retries.Backoff, manifest.DefaultBackoff)
	}
	if got.CacheSpec == nil {
		t.Fatal("cache_spec: expected default to be filled, got nil")
	}
	if got.CacheSpec.Enabled {
		t.Error("cache.enabled: got true, want false")
	}
	if got.CacheSpec.Scope != manifest.DefaultCacheScope {
		t.Errorf("cache.scope: got %q, want %q",
			got.CacheSpec.Scope, manifest.DefaultCacheScope)
	}
	if len(got.Permissions) != 1 || got.Permissions[0] != "read" {
		t.Errorf("permissions default: got %v, want [read]", got.Permissions)
	}
}

func TestValidateAndEnrich_RejectsUndeclaredSideEffect(t *testing.T) {
	// "rm_rf" is not in the schema enum; admission must reject.
	w := nodeWork(workgraph.Node{
		ID:          "destroy",
		Run:         "echo boom",
		SideEffects: []string{"rm_rf"},
	})
	err := manifest.ValidateAndEnrich(w)
	if err == nil {
		t.Fatal("expected rejection for undeclared side effect, got nil")
	}
	if !errors.Is(err, manifest.ErrUndeclaredSideEffect) {
		t.Errorf("error sentinel: got %v, want ErrUndeclaredSideEffect", err)
	}
	if !strings.Contains(err.Error(), "destroy") {
		t.Errorf("error should name the offending node, got %q", err.Error())
	}
	// Work must not have been mutated on rejection.
	if w.Graph.Nodes["destroy"].SideEffects[0] != "rm_rf" {
		t.Error("rejected work should keep original SideEffects slice")
	}
}

func TestValidateAndEnrich_RejectsUndeclaredPermission(t *testing.T) {
	// "all_caps" is not in the schema enum; admission must reject.
	w := nodeWork(workgraph.Node{
		ID:          "deploy",
		Run:         "echo deploy",
		Permissions: []string{"all_caps", "read"},
	})
	err := manifest.ValidateAndEnrich(w)
	if err == nil {
		t.Fatal("expected rejection for undeclared permission, got nil")
	}
	if !errors.Is(err, manifest.ErrUndeclaredPermission) {
		t.Errorf("error sentinel: got %v, want ErrUndeclaredPermission", err)
	}
	if !strings.Contains(err.Error(), "all_caps") {
		t.Errorf("error should name the offending permission, got %q", err.Error())
	}
}

func TestValidateAndEnrich_AcceptsAllAllowedSideEffects(t *testing.T) {
	// Every entry in AllowedSideEffects must be accepted. This guards
	// against accidental drift between the allow-list and the schema.
	for _, se := range manifest.AllowedSideEffects {
		t.Run(se, func(t *testing.T) {
			w := nodeWork(workgraph.Node{
				ID:          "n",
				Run:         "true",
				SideEffects: []string{se},
			})
			if err := manifest.ValidateAndEnrich(w); err != nil {
				t.Fatalf("allowed side_effect %q rejected: %v", se, err)
			}
		})
	}
}

func TestValidateAndEnrich_RejectsBadRetries(t *testing.T) {
	cases := []struct {
		name string
		ret  workgraph.RetrySpec
		want bool // true if we expect rejection
	}{
		{"max_attempts_six", workgraph.RetrySpec{MaxAttempts: 6}, true},
		{"max_attempts_zero", workgraph.RetrySpec{MaxAttempts: 0}, true},
		{"max_attempts_one_ok", workgraph.RetrySpec{MaxAttempts: 1, Backoff: "exponential"}, false},
		{"max_attempts_five_ok", workgraph.RetrySpec{MaxAttempts: 5, Backoff: "linear"}, false},
		{"bad_backoff", workgraph.RetrySpec{MaxAttempts: 2, Backoff: "ludicrous"}, true},
		{"empty_backoff_filled", workgraph.RetrySpec{MaxAttempts: 2, Backoff: ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := nodeWork(workgraph.Node{
				ID:      "n",
				Run:     "true",
				Retries: &c.ret,
			})
			err := manifest.ValidateAndEnrich(w)
			if c.want && err == nil {
				t.Fatalf("expected rejection for %+v, got nil", c.ret)
			}
			if !c.want && err != nil {
				t.Fatalf("unexpected error for %+v: %v", c.ret, err)
			}
			if c.want && err != nil && !errors.Is(err, manifest.ErrInvalidCapability) {
				t.Errorf("expected ErrInvalidCapability, got %v", err)
			}
		})
	}
}

func TestValidateAndEnrich_PreservesExplicitTimeout(t *testing.T) {
	// Caller declares timeout_s=120; admission must NOT overwrite it.
	w := nodeWork(workgraph.Node{
		ID:       "quick",
		Run:      "true",
		TimeoutS: 120,
	})
	if err := manifest.ValidateAndEnrich(w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := w.Graph.Nodes["quick"].TimeoutS; got != 120 {
		t.Errorf("explicit timeout overwritten: got %d, want 120", got)
	}
}

func TestValidateAndEnrich_NilWork(t *testing.T) {
	// Defensive: nil input must not panic.
	if err := manifest.ValidateAndEnrich(nil); err == nil {
		t.Fatal("expected error for nil work, got nil")
	}
}

func TestFormatError_StripsPrefix(t *testing.T) {
	// FormatError must produce clean API-facing messages.
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			"permission",
			manifest.ErrUndeclaredPermission,
			"undeclared permission: undeclared permission",
		},
		{
			"side_effect",
			manifest.ErrUndeclaredSideEffect,
			"undeclared side effect: undeclared side effect",
		},
		{
			"capability",
			manifest.ErrInvalidCapability,
			"invalid capability: invalid capability",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := manifest.FormatError(c.err)
			if !strings.HasPrefix(got, c.want[:len(c.want)/2]) {
				t.Errorf("FormatError(%v) = %q, want prefix %q", c.err, got, c.want)
			}
		})
	}
	if got := manifest.FormatError(nil); got != "" {
		t.Errorf("FormatError(nil) = %q, want empty", got)
	}
}
