package workgraph_test

import (
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Vocabulary freeze: the workgraph State enum is the de facto mission
// vocabulary of WORKS. Any rename/add/remove changes the wire shape,
// CLI output, and the future mapping to mission-state/1.0
// (DRAFT/READY/AUTHORIZED/RUNNING/PAUSED/VERIFYING/VERIFIED/RECOVERING/
// NEEDS_INPUT/FAILED/CANCELLED/REVOKED). The cross-repo mapping itself
// is an owner decision (Wave 4); this test only forbids silent drift.
//
// WHY THIS FILE HAS TWO TESTS.
//
// The original guard built a local map of 12 states, asserted its own
// length, and then validated IsTerminal() only for the entries in that
// local map. It never consulted the real enum, so it was structurally
// incapable of detecting an addition — exactly the drift it claimed to
// forbid. Replacing that list with a 14-entry one would be the same bug
// one level down.
//
// Go cannot enumerate typed string constants at runtime (there is no
// reflect story for a `type State string` const block), so a membership
// check against a hand-written list cannot close the hole on its own:
//
//   - declare a const AND append it to the list -> every membership
//     check passes unless a count is pinned; and
//   - declare a const WITHOUT touching the list -> the test never sees it.
//
// The two tests below close both directions:
//
//	TestStateVocabularyFrozen   pins the registered set by exact
//	                            membership and length, and pins the exact
//	                            terminal set.
//	TestStateConstantsAreRegistered (workgraph_state_ast_test.go, in-package)
//	                            walks this package's own source with
//	                            go/parser and fails if any State-typed
//	                            constant is declared but unregistered.
//
// Adding a state therefore requires editing BOTH the constant block and
// AllStates() — there is no path that adds one without a test turning red.
func TestStateVocabularyFrozen(t *testing.T) {
	// The registered vocabulary, as a set. Count is a tripwire, not a
	// convention: it fails loudly if a state is added or removed.
	const wantCount = 14

	all := workgraph.AllStates()

	if len(all) != wantCount {
		t.Fatalf("AllStates() has %d entries, want exactly %d — a state was added or removed without updating the freeze guard", len(all), wantCount)
	}

	// Exact set, by membership. Built from the same literals the package
	// exports, so a rename shows up here as both a removal and an addition.
	want := map[workgraph.State]bool{
		workgraph.StateCreated:         true,
		workgraph.StatePlanning:        true,
		workgraph.StateQueued:          true,
		workgraph.StateRunning:         true,
		workgraph.StateVerifying:       true,
		workgraph.StateSucceeded:       true,
		workgraph.StateBlocked:         true,
		workgraph.StateFailed:          true,
		workgraph.StateCancelled:       true,
		workgraph.StateWaitingHuman:    true,
		workgraph.StateSuspended:       true,
		workgraph.StateBudgetExhausted: true,
		workgraph.StateTimedOut:        true,
		workgraph.StateUnknown:         true,
	}

	got := map[workgraph.State]bool{}
	for _, s := range all {
		if got[s] {
			t.Errorf("AllStates() lists %q twice — the registered vocabulary must be distinct", s)
		}
		got[s] = true
	}
	for s := range want {
		if !got[s] {
			t.Errorf("AllStates() is missing %q — a frozen state was dropped", s)
		}
	}
	for s := range got {
		if !want[s] {
			t.Errorf("AllStates() contains unregistered state %q — register it deliberately or remove the constant", s)
		}
	}

	// Terminality must be asserted for the WHOLE vocabulary, not only for
	// the entries a local list happens to name.
	wantTerminal := map[workgraph.State]bool{
		workgraph.StateSucceeded: true,
		workgraph.StateFailed:    true,
		workgraph.StateCancelled: true,
		// ADR-0032: TIMED_OUT is terminal — the run is over and will not
		// progress on its own.
		workgraph.StateTimedOut: true,
	}
	for _, s := range all {
		if s.IsTerminal() != wantTerminal[s] {
			t.Errorf("state %q IsTerminal()=%v, want %v", s, s.IsTerminal(), wantTerminal[s])
		}
	}

	// UNKNOWN's non-terminality is the load-bearing half of ADR-0032 and
	// is called out explicitly so a future "tidying up" of IsTerminal()
	// cannot quietly invert it. A terminal UNKNOWN would make the
	// ambiguity permanent, which is the opposite of the intent: recovery
	// must still be able to resolve it.
	if workgraph.StateUnknown.IsTerminal() {
		t.Error("StateUnknown must NOT be terminal — it is indeterminate, not finished (ADR-0032)")
	}
}

// TestAllStatesReturnsDefensiveCopy pins that a caller cannot mutate the
// canonical vocabulary through the returned slice. Without this, a
// consumer doing sort.Slice or an in-place filter would corrupt the set
// every later guard reads.
func TestAllStatesReturnsDefensiveCopy(t *testing.T) {
	first := workgraph.AllStates()
	if len(first) != 14 {
		t.Fatalf("AllStates() has %d entries, want 14", len(first))
	}
	original := first[0]
	first[0] = workgraph.State("MUTATED")

	second := workgraph.AllStates()
	if second[0] != original {
		t.Errorf("AllStates() leaked its backing array: second call returned %q, want %q", second[0], original)
	}
	if len(second) != 14 {
		t.Errorf("AllStates() length changed after mutation: %d, want 14", len(second))
	}
}