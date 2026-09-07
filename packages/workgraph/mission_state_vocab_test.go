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
func TestStateVocabularyFrozen(t *testing.T) {
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
	}
	if len(want) != 12 {
		t.Fatalf("expected 12 frozen states, listed %d", len(want))
	}
	// Terminals must be exactly the succeeded/failed/cancelled triple.
	for s := range want {
		terminal := s == workgraph.StateSucceeded ||
			s == workgraph.StateFailed ||
			s == workgraph.StateCancelled
		if s.IsTerminal() != terminal {
			t.Errorf("state %q IsTerminal()=%v, want %v", s, s.IsTerminal(), terminal)
		}
	}
}
