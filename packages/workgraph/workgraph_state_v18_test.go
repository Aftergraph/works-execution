package workgraph_test

import (
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// ADR-0032 state additions: TIMED_OUT (terminal) and UNKNOWN (not
// terminal). These tests pin the transition law, not just the vocabulary
// — a vocabulary addition that is reachable-but-unresolvable (or
// resolvable-only-by-the-runtime) would pass the freeze guards and still
// be wrong.

// TestTimedOutMirrorsFailedReachability pins the design rule that
// TIMED_OUT is reachable exactly where FAILED is. A wall-clock kill can
// end a Work at any stage that was still running, so its reachability
// must match FAILED's; the only difference between the two is terminality
// semantics, not where they can be entered from.
func TestTimedOutMirrorsFailedReachability(t *testing.T) {
	// The 12-state vocabulary frozen before ADR-0032, minus the states
	// that are unreachable-from-themselves by construction. Each of these
	// must permit FAILED today, and must permit TIMED_OUT for the same
	// reason.
	failedReachableFrom := []workgraph.State{
		workgraph.StateCreated,
		workgraph.StatePlanning,
		workgraph.StateQueued,
		workgraph.StateRunning,
		workgraph.StateVerifying,
		workgraph.StateWaitingHuman,
		workgraph.StateSuspended,
		workgraph.StateBudgetExhausted,
	}
	for _, from := range failedReachableFrom {
		if !workgraph.CanTransition(from, workgraph.StateFailed) {
			t.Fatalf("precondition broken: %s -> FAILED is not allowed, but the frozen table is supposed to permit it", from)
		}
		if !workgraph.CanTransition(from, workgraph.StateTimedOut) {
			t.Errorf("%s -> TIMED_OUT denied, but %s -> FAILED is allowed — TIMED_OUT must mirror FAILED reachability", from, from)
		}
	}
}

// TestTimedOutIsTerminalAndDeadEnds pins that a timed-out Work never
// progresses on its own: IsTerminal short-circuits CanTransition for
// every target, exactly as SUCCEEDED/FAILED/CANCELLED do.
func TestTimedOutIsTerminalAndDeadEnds(t *testing.T) {
	if !workgraph.StateTimedOut.IsTerminal() {
		t.Fatal("TIMED_OUT must be terminal")
	}
	for _, to := range workgraph.AllStates() {
		if workgraph.CanTransition(workgraph.StateTimedOut, to) {
			t.Errorf("TIMED_OUT -> %s allowed; a terminal state must dead-end", to)
		}
	}
}

// TestUnknownIsReachableFromRunningAndResolvableOutward pins that a lost
// run can be recorded as indeterminate and then decided by a human or the
// recovery supervisor.
func TestUnknownIsReachableFromRunningAndResolvableOutward(t *testing.T) {
	if !workgraph.CanTransition(workgraph.StateRunning, workgraph.StateUnknown) {
		t.Error("RUNNING -> UNKNOWN denied; the recovery supervisor must be able to record that a run was lost")
	}
	for _, to := range []workgraph.State{
		workgraph.StateFailed,
		workgraph.StateCancelled,
		workgraph.StateSuspended,
	} {
		if !workgraph.CanTransition(workgraph.StateUnknown, to) {
			t.Errorf("UNKNOWN -> %s denied; UNKNOWN must be resolvable outward by a human or the recovery supervisor", to)
		}
	}
}

// TestUnknownCannotBeSelfResumed is the anti-self-resume law for ADR-0032.
// The whole point of UNKNOWN is that the runtime does not know whether the
// work already happened. An UNKNOWN -> RUNNING edge would let the runtime
// re-run it blind, which is the failure mode the state exists to prevent.
func TestUnknownCannotBeSelfResumed(t *testing.T) {
	if workgraph.CanTransition(workgraph.StateUnknown, workgraph.StateRunning) {
		t.Error("UNKNOWN -> RUNNING allowed; the runtime must never resume a run whose outcome it could not determine (ADR-0032)")
	}
	// No self-loop either: an idle tick is not a transition.
	if workgraph.CanTransition(workgraph.StateUnknown, workgraph.StateUnknown) {
		t.Error("UNKNOWN -> UNKNOWN allowed; the transition table must not contain self-loops")
	}
}

// TestUnknownDoesNotBlockItsOwnResolution proves the non-terminality is
// load-bearing: CanTransition consults IsTerminal first, so a terminal
// UNKNOWN would make every one of these resolutions impossible and strand
// the Work forever.
func TestUnknownDoesNotBlockItsOwnResolution(t *testing.T) {
	if workgraph.StateUnknown.IsTerminal() {
		t.Fatal("UNKNOWN must not be terminal — terminalizing it makes the ambiguity permanent and strands the Work (ADR-0032)")
	}
	for _, to := range []workgraph.State{
		workgraph.StateFailed,
		workgraph.StateCancelled,
	} {
		if !workgraph.CanTransition(workgraph.StateUnknown, to) {
			t.Errorf("UNKNOWN -> %s denied because UNKNOWN was treated as terminal", to)
		}
	}
}

// TestUnknownIsNotMissionOnly pins that UNKNOWN is a plain CI-level
// vocabulary state: a non-mission Work must be able to record a lost run
// without carrying a mission contract.
func TestUnknownIsNotMissionOnly(t *testing.T) {
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateRunning,
		Mission:   nil,
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"a": {ID: "a", Run: "echo ok"},
		}},
	}
	if err := w.ValidateTransition(workgraph.StateUnknown); err != nil {
		t.Errorf("non-mission Work RUNNING -> UNKNOWN rejected: %v", err)
	}

	w.State = workgraph.StateUnknown
	// UNKNOWN -> SUSPENDED is a mission-only destination, so the
	// pre-existing ADR-0008 freeze law must still refuse it for a CI Work.
	// That is correct and unchanged behaviour, not a gap introduced here.
	if err := w.ValidateTransition(workgraph.StateSuspended); err == nil {
		t.Error("non-mission Work UNKNOWN -> SUSPENDED accepted; the mission-only freeze law must still hold")
	}
	// Resolution to FAILED stays available to a CI Work.
	if err := w.ValidateTransition(workgraph.StateFailed); err != nil {
		t.Errorf("non-mission Work UNKNOWN -> FAILED rejected: %v", err)
	}
}

// TestTimedOutIsNotMissionOnly mirrors the above for TIMED_OUT: a CI Work
// that hits its wall clock must be able to record the timeout without a
// mission contract.
func TestTimedOutIsNotMissionOnly(t *testing.T) {
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateRunning,
		Mission:   nil,
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"a": {ID: "a", Run: "echo ok"},
		}},
	}
	if err := w.ValidateTransition(workgraph.StateTimedOut); err != nil {
		t.Errorf("non-mission Work RUNNING -> TIMED_OUT rejected: %v", err)
	}
}