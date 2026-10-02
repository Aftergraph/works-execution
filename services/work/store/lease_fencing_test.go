package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// Lease fencing (ADR-0033). Every test here pins one link in the chain
// (executorId, leaseId, leaseEpoch) -> refuse the stale holder.
//
// These tests are deterministic by construction: no sleeps, no reliance on
// wall-clock ordering. Where concurrency is the point, the outcome is
// asserted as a property ("exactly one wins") rather than as a schedule.

// fencingFixture returns a queued single-node Work with one granted lease.
func fencingFixture(t *testing.T) (store.Store, context.Context, *workgraph.Work, *workgraph.Lease) {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"only": {ID: "only", Run: "echo ok"},
		}},
		Requirements: workgraph.Requirements{OS: "linux"},
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := s.GrantLease(ctx, w.ID, "only", "wrkr_1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, w, lease
}

// TestGrantLease_IssuesPositiveMonotonicEpoch pins the origin of the
// scheme: the first grant of a node is epoch 1, never 0. Zero is reserved
// to mean "caller did not present a token", which is what makes an
// omitted epoch fail closed instead of matching epoch-zero rows.
func TestGrantLease_IssuesPositiveMonotonicEpoch(t *testing.T) {
	s, ctx, w, lease := fencingFixture(t)

	if lease.Epoch != 1 {
		t.Fatalf("first grant epoch = %d, want 1", lease.Epoch)
	}

	// Release and re-grant the same node: the epoch must advance, or a
	// holder from the previous generation would still match.
	if err := s.ReleaseLease(ctx, store.LeaseRefFor(lease), "regrant"); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.GrantLease(ctx, w.ID, "only", "wrkr_2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.Epoch <= lease.Epoch {
		t.Errorf("re-grant epoch = %d, want greater than previous %d", next.Epoch, lease.Epoch)
	}
	if next.Epoch != lease.Epoch+1 {
		t.Errorf("re-grant epoch = %d, want exactly %d (strictly monotonic, +1 per grant)", next.Epoch, lease.Epoch+1)
	}
}

// TestFencedVerbs_RefuseZeroEpoch pins the fail-closed direction for a
// caller that never learned the epoch (an old client, or a hand-rolled
// request). It must be refused, not treated as "no fencing requested".
func TestFencedVerbs_RefuseZeroEpoch(t *testing.T) {
	s, ctx, _, lease := fencingFixture(t)

	zero := store.LeaseRef{LeaseID: lease.ID, WorkerID: lease.WorkerID, Epoch: 0}

	if _, err := s.RenewLease(ctx, zero, time.Minute); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("RenewLease with zero epoch: got %v, want ErrLeaseFenced", err)
	}
	if _, err := s.CompleteLease(ctx, zero, 0, nil, nil); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("CompleteLease with zero epoch: got %v, want ErrLeaseFenced", err)
	}
	if err := s.ReleaseLease(ctx, zero, "nope"); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("ReleaseLease with zero epoch: got %v, want ErrLeaseFenced", err)
	}
	if err := s.RevokeLease(ctx, zero, "nope"); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("RevokeLease with zero epoch: got %v, want ErrLeaseFenced", err)
	}

	// The lease must be untouched: a refused call is not a partial write.
	final, err := s.GetLease(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != workgraph.LeaseActive {
		t.Errorf("lease status = %s after refused fenced calls, want ACTIVE", final.Status)
	}
}

// TestFencedVerbs_RefuseWrongExecutor pins the identity half of the
// triple. The epoch alone proves recency, not who is presenting it.
func TestFencedVerbs_RefuseWrongExecutor(t *testing.T) {
	s, ctx, _, lease := fencingFixture(t)

	impostor := store.LeaseRef{LeaseID: lease.ID, WorkerID: "wrkr_attacker", Epoch: lease.Epoch}

	if _, err := s.RenewLease(ctx, impostor, time.Minute); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("RenewLease by impostor: got %v, want ErrLeaseFenced", err)
	}
	if _, err := s.CompleteLease(ctx, impostor, 0, nil, nil); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("CompleteLease by impostor: got %v, want ErrLeaseFenced", err)
	}
	if err := s.ReleaseLease(ctx, impostor, "nope"); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("ReleaseLease by impostor: got %v, want ErrLeaseFenced", err)
	}

	final, err := s.GetLease(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != workgraph.LeaseActive {
		t.Errorf("lease status = %s after impostor attempts, want ACTIVE", final.Status)
	}
}

// TestCompleteLease_RefusesStaleHolderAfterRegrant is the canonical
// zombie-worker scenario: worker A's lease expires, the node is re-granted
// to worker B, and A wakes up and tries to report its (stale) result.
// A must not be able to overwrite B's lease.
//
// The expected sentinel is ErrLeaseNotActive, NOT ErrLeaseFenced, and the
// distinction is load-bearing rather than cosmetic. A re-grant creates a
// NEW lease row, so A's own row is left RELEASED. loadLeaseForFence checks
// status before the token, so the honest answer for A is "that lease is
// finished" — there is no epoch on a finished row worth comparing. Either
// way the write is refused; what the precedence rule buys is that the error
// shape cannot be used to probe the token. See
// TestFencedVerbs_EpochMismatchOnActiveLeaseIsFenced for the case where the
// epoch guard actually fires.
func TestCompleteLease_RefusesStaleHolderAfterRegrant(t *testing.T) {
	s, ctx, w, stale := fencingFixture(t)

	if err := s.ReleaseLease(ctx, store.LeaseRefFor(stale), "expired"); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := s.GrantLease(ctx, w.ID, "only", "wrkr_2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Epoch <= stale.Epoch {
		t.Fatalf("fixture broken: fresh epoch %d must exceed stale %d", fresh.Epoch, stale.Epoch)
	}

	// The zombie reports success. Refused.
	if _, err := s.CompleteLease(ctx, store.LeaseRefFor(stale), 0, nil, nil); !errors.Is(err, store.ErrLeaseNotActive) {
		t.Fatalf("stale CompleteLease: got %v, want ErrLeaseNotActive", err)
	}

	// The real holder's lease is untouched and still completable.
	if _, err := s.CompleteLease(ctx, store.LeaseRefFor(fresh), 0, nil, nil); err != nil {
		t.Fatalf("current holder CompleteLease: %v", err)
	}
	final, err := s.GetLease(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != workgraph.LeaseReleased {
		t.Errorf("current holder lease status = %s, want RELEASED", final.Status)
	}
	// And the zombie's stale epoch must not have been able to touch the new
	// holder's row under any argument.
	newest, err := s.GetLease(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newest.Epoch != fresh.Epoch {
		t.Errorf("current holder epoch drifted to %d (was %d)", newest.Epoch, fresh.Epoch)
	}
}

// TestFencedVerbs_EpochMismatchOnActiveLeaseIsFenced pins the epoch guard
// on the case where it actually fires: a live ACTIVE lease whose stored
// epoch no longer matches the presented one.
//
// The epoch is advanced directly on the row, standing in for any future
// path that re-issues a token for an existing lease. It is deterministic
// and does not depend on a re-grant, which creates a new row and therefore
// never exercises this branch.
func TestFencedVerbs_EpochMismatchOnActiveLeaseIsFenced(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"only": {ID: "only", Run: "echo ok"},
		}},
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := s.GrantLease(ctx, w.ID, "only", "wrkr_1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Advance the row's epoch while leaving it ACTIVE and same-worker.
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE work_leases SET epoch = epoch + 1 WHERE id = ?`, lease.ID); err != nil {
		t.Fatal(err)
	}

	stale := store.LeaseRefFor(lease) // correct id, correct worker, superseded epoch
	if _, err := s.RenewLease(ctx, stale, time.Minute); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("RenewLease with a superseded epoch: got %v, want ErrLeaseFenced", err)
	}
	if _, err := s.CompleteLease(ctx, stale, 0, nil, nil); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("CompleteLease with a superseded epoch: got %v, want ErrLeaseFenced", err)
	}
	if err := s.ReleaseLease(ctx, stale, "nope"); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("ReleaseLease with a superseded epoch: got %v, want ErrLeaseFenced", err)
	}
	if err := s.RevokeLease(ctx, stale, "nope"); !errors.Is(err, store.ErrLeaseFenced) {
		t.Errorf("RevokeLease with a superseded epoch: got %v, want ErrLeaseFenced", err)
	}

	// The lease is untouched.
	final, err := s.GetLease(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != workgraph.LeaseActive {
		t.Errorf("lease status = %s after fenced attempts, want ACTIVE", final.Status)
	}
}

// TestFencedVerbs_ReportNotActiveBeforeFenced pins the error precedence.
// A finished lease must report "not active", never "fenced": the caller
// learns nothing about the token, and a re-grant is the correct response
// to either answer, but only one of them is true.
func TestFencedVerbs_ReportNotActiveBeforeFenced(t *testing.T) {
	s, ctx, _, lease := fencingFixture(t)

	if err := s.ReleaseLease(ctx, store.LeaseRefFor(lease), "done"); err != nil {
		t.Fatal(err)
	}

	// Correct executor, stale-by-expiry epoch: the lease is RELEASED, so
	// the honest answer is ErrLeaseNotActive.
	if _, err := s.RenewLease(ctx, store.LeaseRefFor(lease), time.Minute); !errors.Is(err, store.ErrLeaseNotActive) {
		t.Errorf("RenewLease on released lease: got %v, want ErrLeaseNotActive", err)
	}
	// Wrong executor AND released: still NotActive, because status is
	// checked first. This is the anti-oracle property.
	wrong := store.LeaseRef{LeaseID: lease.ID, WorkerID: "wrkr_attacker", Epoch: lease.Epoch}
	if err := s.ReleaseLease(ctx, wrong, "nope"); !errors.Is(err, store.ErrLeaseNotActive) {
		t.Errorf("ReleaseLease on released lease by impostor: got %v, want ErrLeaseNotActive", err)
	}
}

// TestFencedVerbs_UnknownLeaseIsNotFound keeps the anti-oracle posture
// end to end: a lease id that does not exist is "not found", never
// "fenced", so the error never distinguishes a real-but-stale lease from a
// fabricated id.
func TestFencedVerbs_UnknownLeaseIsNotFound(t *testing.T) {
	s, ctx, _, _ := fencingFixture(t)

	missing := store.LeaseRef{LeaseID: "lse_does_not_exist", WorkerID: "wrkr_1", Epoch: 1}
	if _, err := s.RenewLease(ctx, missing, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RenewLease on unknown lease: got %v, want ErrNotFound", err)
	}
	if err := s.RevokeLease(ctx, missing, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RevokeLease on unknown lease: got %v, want ErrNotFound", err)
	}
}

// TestConcurrentCompleteLease_ExactlyOneWinner is the race the fencing
// design exists to make safe: two callers hold the SAME, CURRENT fencing
// triple and both try to complete the same lease.
//
// Both pre-reads legitimately observe ACTIVE + matching epoch. Neither
// pre-read can detect the other. What separates them is the
// compare-and-swap: the second writer's predicate no longer matches the
// row the first moved to RELEASED, affects zero rows, and is reported as
// ErrLeaseNotActive.
//
// Asserted as a property over N rounds rather than a fixed schedule, so
// the test is deterministic in what it demands and merely racy in how it
// exercises it.
func TestConcurrentCompleteLease_ExactlyOneWinner(t *testing.T) {
	const rounds = 25
	for round := 0; round < rounds; round++ {
		s, ctx, _, lease := fencingFixture(t)

		var wg sync.WaitGroup
		results := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.CompleteLease(ctx, store.LeaseRefFor(lease), 0, nil, nil)
			results[0] = err
		}()
		go func() {
			defer wg.Done()
			_, err := s.CompleteLease(ctx, store.LeaseRefFor(lease), 0, nil, nil)
			results[1] = err
		}()
		wg.Wait()

		wins, conflicts := 0, 0
		for _, err := range results {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, store.ErrLeaseNotActive), errors.Is(err, store.ErrLeaseFenced):
				conflicts++
			default:
				t.Fatalf("round %d: unexpected error %v", round, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d winners, want exactly 1 (results: %v)", round, wins, results)
		}
		if conflicts != 1 {
			t.Fatalf("round %d: %d conflicts, want exactly 1", round, conflicts)
		}

		// The loser must not have finalized the attempt a second time. The
		// attempt row is written by the CAS winner only.
		final, err := s.GetLease(ctx, lease.ID)
		if err != nil {
			t.Fatal(err)
		}
		if final.Status != workgraph.LeaseReleased {
			t.Fatalf("round %d: lease status = %s, want RELEASED", round, final.Status)
		}
	}
}

// TestConcurrentCompleteLease_StaleHolderNeverWins is the same race with
// the attacker holding the OLD lease generation. No amount of scheduling
// may let it win.
//
// The stale holder is refused with ErrLeaseNotActive rather than
// ErrLeaseFenced: a re-grant creates a new lease row and leaves the old one
// RELEASED, and status is classified before the token. What matters here
// is the invariant, which is the same under every interleaving: exactly one
// of the two callers succeeds, and it is always the current holder.
func TestConcurrentCompleteLease_StaleHolderNeverWins(t *testing.T) {
	const rounds = 25
	for round := 0; round < rounds; round++ {
		s, ctx, w, stale := fencingFixture(t)
		if err := s.ReleaseLease(ctx, store.LeaseRefFor(stale), "expired"); err != nil {
			t.Fatal(err)
		}
		fresh, _, err := s.GrantLease(ctx, w.ID, "only", "wrkr_2", time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		staleErr := make(chan error, 1)
		freshErr := make(chan error, 1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.CompleteLease(ctx, store.LeaseRefFor(stale), 0, nil, nil)
			staleErr <- err
		}()
		go func() {
			defer wg.Done()
			_, err := s.CompleteLease(ctx, store.LeaseRefFor(fresh), 0, nil, nil)
			freshErr <- err
		}()
		wg.Wait()

		if err := <-staleErr; !errors.Is(err, store.ErrLeaseNotActive) {
			t.Fatalf("round %d: stale holder got %v, want ErrLeaseNotActive in every interleaving", round, err)
		}
		if err := <-freshErr; err != nil {
			t.Fatalf("round %d: current holder got %v, want success", round, err)
		}
	}
}

// TestReaperRevoke_PresentsObservedEpoch pins that the reaper path is
// fenced too. ListExpiredLeases must return the epoch so the revoke
// presents the token it actually observed; otherwise a reaper racing a
// re-grant would cancel the new holder's live lease.
//
// The clock is not faked: the lease's expiry is forced into the past with
// a direct UPDATE, so the test is deterministic without sleeping.
func TestReaperRevoke_PresentsObservedEpoch(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"only": {ID: "only", Run: "echo ok"},
		}},
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := s.GrantLease(ctx, w.ID, "only", "wrkr_1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Force expiry rather than waiting for the TTL.
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE work_leases SET expires_at = ? WHERE id = ?`,
		time.Unix(0, 0).UTC().Format(time.RFC3339Nano), lease.ID); err != nil {
		t.Fatal(err)
	}

	scanned, err := s.ListExpiredLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) != 1 {
		t.Fatalf("got %d expired leases, want 1", len(scanned))
	}
	if scanned[0].ID != lease.ID {
		t.Fatalf("scanned lease %s, want %s", scanned[0].ID, lease.ID)
	}
	if scanned[0].Epoch != lease.Epoch {
		t.Errorf("ListExpiredLeases returned epoch %d, want %d — the reaper cannot fence without it",
			scanned[0].Epoch, lease.Epoch)
	}

	// The revoke presents exactly what the scan observed, and succeeds.
	if err := s.RevokeLease(ctx, scanned[0].Ref(), "lease expired"); err != nil {
		t.Fatalf("reaper revoke with the scanned epoch: %v", err)
	}

	// Presenting the same epoch a SECOND time is refused: the reaper's
	// revoke is not idempotent-through-fencing, it is simply refused once
	// the lease is no longer ACTIVE. That is the pre-existing reaper
	// behaviour (reapOnce skips the error) and it is unchanged.
	if err := s.RevokeLease(ctx, scanned[0].Ref(), "lease expired"); !errors.Is(err, store.ErrLeaseNotActive) {
		t.Errorf("second reaper revoke: got %v, want ErrLeaseNotActive", err)
	}
}

// TestGrantLease_RefusesUnknownWork is the ADR-0032 anti-blind-retry law:
// an UNKNOWN Work is indeterminate, not paused, and must not be handed
// back to the runtime for a blind re-run.
func TestGrantLease_RefusesUnknownWork(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateUnknown); err != nil {
		t.Fatal(err)
	}

	_, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_1", time.Minute)
	if !errors.Is(err, store.ErrLeaseConflict) {
		t.Errorf("GrantLease on UNKNOWN work: got %v, want ErrLeaseConflict", err)
	}

	// And it IS resolvable outward, by the recovery supervisor.
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateFailed); err != nil {
		t.Errorf("UNKNOWN -> FAILED rejected: %v", err)
	}
}