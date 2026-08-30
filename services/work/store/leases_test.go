package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func TestGrantLease_HappyPath(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	// Force the work into RUNNING (GrantLease requires an executable state).
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, attempt, err := s.GrantLease(ctx, w.ID, "a", "wrkr_1", 5*time.Second)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if lease.Status != workgraph.LeaseActive {
		t.Errorf("status: got %s, want ACTIVE", lease.Status)
	}
	if lease.WorkID != w.ID || lease.NodeID != "a" || lease.WorkerID != "wrkr_1" {
		t.Errorf("lease: %+v", lease)
	}
	if attempt.Status != "running" {
		t.Errorf("attempt status: got %s, want running", attempt.Status)
	}
	if !lease.ExpiresAt.After(lease.GrantedAt) {
		t.Errorf("expires_at should be after granted_at")
	}
}

func TestGrantLease_ConflictOnSecondLease(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_1", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_2", 5*time.Second)
	if err != store.ErrLeaseConflict {
		t.Errorf("got %v, want ErrLeaseConflict", err)
	}
}

func TestRenewLease_ExtendsExpiry(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	l, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_1", 1*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	original := l.ExpiresAt
	time.Sleep(50 * time.Millisecond)
	renewed, err := s.RenewLease(ctx, l.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed.ExpiresAt.After(original) {
		t.Errorf("renewed expiry %v should be after original %v", renewed.ExpiresAt, original)
	}
}

func TestRenewLease_DeniedAfterRelease(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	l, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, l.ID, "test"); err != nil {
		t.Fatal(err)
	}
	_, err = s.RenewLease(ctx, l.ID, 5*time.Second)
	if err != store.ErrLeaseNotActive {
		t.Errorf("got %v, want ErrLeaseNotActive", err)
	}
}

func TestCompleteLease_FinalizesAttempt(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	// Single-node work so completion of that node makes the work SUCCEEDED.
	w := &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Source: workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{
				"only": {ID: "only", Run: "echo ok"},
			},
		},
		Requirements: workgraph.Requirements{OS: "linux"},
		Policy:      workgraph.Policy{},
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	l, _, err := s.GrantLease(ctx, w.ID, "only", "wrkr_1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CompleteLease(ctx, l.ID, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != workgraph.StateSucceeded {
		t.Errorf("state: got %s, want SUCCEEDED", got.State)
	}
	// Lease should be RELEASED.
	final, err := s.GetLease(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != workgraph.LeaseReleased {
		t.Errorf("lease status: got %s, want RELEASED", final.Status)
	}
}

func TestRevokeLease_MarksAttemptCancelled(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	l, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeLease(ctx, l.ID, "test revoke"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range got.Attempts {
		if a.ID == l.AttemptID && a.Status != "cancelled" {
			t.Errorf("attempt status: got %s, want cancelled", a.Status)
		}
	}
	// After revoke, the node is ready again — ActiveLeasesByWorkID should be empty.
	active, err := s.ActiveLeasesByWorkID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Errorf("expected no active leases, got %v", active)
	}
}

func TestListExpiredLeases_ReturnsOnlyExpiredActive(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	// Grant a lease that expires in 100ms.
	l, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_1", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	expired, err := s.ListExpiredLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("got %d expired, want 1", len(expired))
	}
	if expired[0].ID != l.ID {
		t.Errorf("got lease %s, want %s", expired[0].ID, l.ID)
	}
}

func TestActiveLeasesByWorkID(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork() // has nodes "a" and "b" (b depends on a)
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	// Grant on "a" — should appear in active set.
	if _, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_1", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveLeasesByWorkID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !active["a"] {
		t.Errorf("expected a in active leases, got %v", active)
	}
	if active["b"] {
		t.Errorf("did not expect b in active leases, got %v", active)
	}
}