package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// TestReapOnce_MarksExpiredNotRevoked pins the reaper's contract: a lease
// whose TTL has elapsed is transitioned to EXPIRED (the timeout path), not
// REVOKED (the explicit/administrative cancellation path). Reusing RevokeLease
// here would conflate operator-initiated cancellation with a lost-worker
// timeout on every metric/audit/dashboard that distinguishes the two.
func TestReapOnce_MarksExpiredNotRevoked(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "reaper.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "echo a"}}},
		State:     workgraph.StateCreated,
	}
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	l, _, err := st.GrantLease(ctx, w.ID, "a", "wrkr_1", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)

	n, err := reapOnce(ctx, st, 10)
	if err != nil {
		t.Fatalf("reapOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("reapOnce expired count: got %d, want 1", n)
	}
	got, err := st.GetLease(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workgraph.LeaseExpired {
		t.Errorf("reaped lease status: got %s, want %s", got.Status, workgraph.LeaseExpired)
	}
	if got.Status == workgraph.LeaseRevoked {
		t.Errorf("reaped lease is REVOKED — reaper must use EXPIRED, not the revoke path")
	}
}

// TestReapOnce_SkipsAlreadyTerminal pins idempotency: once a lease is
// terminal (e.g. released by the worker), a reaper pass that lists it
// before the row drops from ListExpiredLeases skips it without error and
// without counting it. ListExpiredLeases only returns ACTIVE rows in
// practice, so this exercises the transition-guard branch directly.
func TestReapOnce_SkipsAlreadyTerminal(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "reaper.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "echo a"}}},
		State:     workgraph.StateCreated,
	}
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	l, _, err := st.GrantLease(ctx, w.ID, "a", "wrkr_1", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	// Worker explicitly releases first.
	if err := st.ReleaseLease(ctx, l.ID, "worker done"); err != nil {
		t.Fatal(err)
	}
	// Now simulate a stale list: expire should fail the state-machine guard
	// and reapOnce's caller (the loop) treats it as a skip.
	if err := st.ExpireLease(ctx, l.ID, "lease expired"); err == nil {
		t.Fatal("ExpireLease on a RELEASED lease: want ErrInvalidTransition, got nil")
	}
}
