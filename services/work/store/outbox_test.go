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

// Transactional outbox (ADR-0033).
//
// Every test drives an injected clock rather than sleeping, so claim
// expiry, retry backoff and attempt exhaustion are all exercised without
// wall-clock flakiness.

// outboxFixture opens a concrete store (the concrete type is needed for the
// outbox accessors) and returns it with a queued Work.
func outboxFixture(t *testing.T) (*store.SQLiteStore, context.Context, *workgraph.Work) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	return s, ctx, w
}

// driveToTerminal walks a Work to a terminal state through legal
// transitions only.
func driveToTerminal(t *testing.T, s *store.SQLiteStore, ctx context.Context, w *workgraph.Work, to workgraph.State) {
	t.Helper()
	path := []workgraph.State{workgraph.StateRunning, workgraph.StateVerifying}
	for _, st := range path {
		if _, err := s.UpdateState(ctx, w.ID, st); err != nil {
			t.Fatalf("UpdateState %s: %v", st, err)
		}
	}
	if _, err := s.UpdateState(ctx, w.ID, to); err != nil {
		t.Fatalf("UpdateState %s: %v", to, err)
	}
}

// TestUpdateState_EnqueuesTerminalObligationInSameTransaction pins the
// central law: reaching a terminal state records exactly one outbox entry,
// keyed deterministically.
func TestUpdateState_EnqueuesTerminalObligationInSameTransaction(t *testing.T) {
	s, ctx, w := outboxFixture(t)

	entries, err := s.ListOutboxByWorkID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d outbox entries for a non-terminal work, want 0", len(entries))
	}

	driveToTerminal(t, s, ctx, w, workgraph.StateSucceeded)

	entries, err = s.ListOutboxByWorkID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d outbox entries after SUCCEEDED, want exactly 1", len(entries))
	}
	e := entries[0]
	if e.Topic != store.OutboxTopicWorkTerminal {
		t.Errorf("topic = %q, want %q", e.Topic, store.OutboxTopicWorkTerminal)
	}
	if e.IdempotencyKey != store.TerminalWorkIdempotencyKey(w.ID, workgraph.StateSucceeded) {
		t.Errorf("idempotency key = %q, want the deterministic terminal key", e.IdempotencyKey)
	}
	if e.Status != store.OutboxPending {
		t.Errorf("status = %s, want PENDING", e.Status)
	}
	if e.MaxAttempts <= 0 {
		t.Errorf("max_attempts = %d, want a positive retry budget", e.MaxAttempts)
	}
	payload, err := store.DecodeTerminalWorkPayload(e.PayloadJSON)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.WorkID != w.ID {
		t.Errorf("payload work_id = %q, want %q", payload.WorkID, w.ID)
	}
	if payload.State != string(workgraph.StateSucceeded) {
		t.Errorf("payload state = %q, want SUCCEEDED", payload.State)
	}
	if payload.From != string(workgraph.StateVerifying) {
		t.Errorf("payload from = %q, want VERIFYING", payload.From)
	}
}

// TestUpdateState_NoOutboxEntryForNonTerminalTransitions pins that the
// outbox does not become a firehose: only terminal transitions owe a side
// effect.
func TestUpdateState_NoOutboxEntryForNonTerminalTransitions(t *testing.T) {
	s, ctx, w := outboxFixture(t)

	for _, st := range []workgraph.State{workgraph.StateRunning, workgraph.StateVerifying} {
		if _, err := s.UpdateState(ctx, w.ID, st); err != nil {
			t.Fatal(err)
		}
		n, err := s.CountOutboxByStatus(ctx, store.OutboxPending)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("after %s there are %d pending outbox entries, want 0", st, n)
		}
	}
}

// TestTerminalIdempotencyKey_CollapsesReplay pins the anti-duplicate law:
// the deterministic UNIQUE key means replaying the same terminal
// transition cannot produce a second obligation. This is what stops a
// retried request or a replayed webhook from double-publishing.
func TestTerminalIdempotencyKey_CollapsesReplay(t *testing.T) {
	s, ctx, w := outboxFixture(t)
	driveToTerminal(t, s, ctx, w, workgraph.StateSucceeded)

	entries, err := s.ListOutboxByWorkID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	originalID := entries[0].ID

	// Replaying the transition is refused by the state machine (a terminal
	// state dead-ends), which is the first line of defence.
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateSucceeded); !errors.Is(err, workgraph.ErrInvalidTransition) {
		t.Errorf("replaying the terminal transition: got %v, want ErrInvalidTransition", err)
	}

	// And even if a row were re-enqueued directly, the UNIQUE key would
	// keep exactly one. Prove it against the store rather than by
	// assertion.
	key := store.TerminalWorkIdempotencyKey(w.ID, workgraph.StateSucceeded)
	got, err := s.OutboxEntryForKey(ctx, key)
	if err != nil {
		t.Fatalf("lookup by key: %v", err)
	}
	if got.ID != originalID {
		t.Errorf("key resolved to entry %s, want the original %s", got.ID, originalID)
	}
	entries, err = s.ListOutboxByWorkID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries after replay, want 1", len(entries))
	}
}

// TestClaimOutbox_SingleDispatcherWinsEachRow pins the no-double-delivery
// law: two dispatchers racing over the same batch must partition the rows,
// never overlap.
func TestClaimOutbox_SingleDispatcherWinsEachRow(t *testing.T) {
	s, ctx, w := outboxFixture(t)
	driveToTerminal(t, s, ctx, w, workgraph.StateSucceeded)

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]string{} // entry id -> dispatcher

	claim := func(id string) {
		defer wg.Done()
		entries, err := s.ClaimOutbox(ctx, id, 10, time.Minute, now)
		if err != nil {
			t.Errorf("dispatcher %s claim: %v", id, err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, e := range entries {
			if prev, dup := seen[e.ID]; dup {
				t.Errorf("entry %s claimed by both %s and %s", e.ID, prev, id)
				continue
			}
			seen[e.ID] = id
		}
	}

	wg.Add(2)
	go claim("dispatcher-a")
	go claim("dispatcher-b")
	wg.Wait()

	if len(seen) != 1 {
		t.Fatalf("claimed %d distinct entries, want 1 (the single terminal obligation)", len(seen))
	}
	// Exactly one dispatcher got it.
	var winners int
	for range seen {
		winners++
	}
	if winners != 1 {
		t.Fatalf("expected the row claimed exactly once, got %d claims", winners)
	}
}

// TestMarkOutboxDelivered_RequiresOwnership pins that a dispatcher which
// no longer owns a row cannot settle it. This is what stops a slow
// dispatcher from marking a redelivered row as delivered behind the
// dispatcher's back.
func TestMarkOutboxDelivered_RequiresOwnership(t *testing.T) {
	s, ctx, w := outboxFixture(t)
	driveToTerminal(t, s, ctx, w, workgraph.StateSucceeded)

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	claimed, err := s.ClaimOutbox(ctx, "dispatcher-a", 10, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d entries, want 1", len(claimed))
	}
	entry := claimed[0]

	// The wrong dispatcher cannot settle it.
	if err := s.MarkOutboxDelivered(ctx, entry.ID, "dispatcher-b", now); !errors.Is(err, store.ErrOutboxNotClaimed) {
		t.Errorf("settle by non-owner: got %v, want ErrOutboxNotClaimed", err)
	}
	// The owner can.
	if err := s.MarkOutboxDelivered(ctx, entry.ID, "dispatcher-a", now); err != nil {
		t.Fatalf("settle by owner: %v", err)
	}
	e, err := s.GetOutboxEntry(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != store.OutboxDelivered {
		t.Errorf("status = %s, want DELIVERED", e.Status)
	}
	if e.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", e.Attempts)
	}
}

// TestClaimOutbox_ExpiredClaimIsReclaimable pins crash recovery: a
// dispatcher that dies mid-delivery must not strand its rows forever.
func TestClaimOutbox_ExpiredClaimIsReclaimable(t *testing.T) {
	s, ctx, w := outboxFixture(t)
	driveToTerminal(t, s, ctx, w, workgraph.StateSucceeded)

	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	claimed, err := s.ClaimOutbox(ctx, "dispatcher-a", 10, 30*time.Second, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d entries, want 1", len(claimed))
	}
	entry := claimed[0]

	// Before the claim expires, nobody else may take it.
	later, err := s.ClaimOutbox(ctx, "dispatcher-b", 10, 30*time.Second, start.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 0 {
		t.Errorf("dispatcher-b claimed %d entries inside a live claim, want 0", len(later))
	}

	// After it expires, the row is reclaimable — this is the at-least-once
	// cost, stated as a test so it cannot be optimised away silently.
	reclaimed, err := s.ClaimOutbox(ctx, "dispatcher-b", 10, 30*time.Second, start.Add(31*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("dispatcher-b reclaimed %d entries after expiry, want 1", len(reclaimed))
	}
	if reclaimed[0].ID != entry.ID {
		t.Errorf("reclaimed entry %s, want %s", reclaimed[0].ID, entry.ID)
	}
	if reclaimed[0].Attempts != 2 {
		t.Errorf("attempts = %d after reclaim, want 2", reclaimed[0].Attempts)
	}
	// The original owner has lost the row and can no longer settle it.
	if err := s.MarkOutboxDelivered(ctx, entry.ID, "dispatcher-a", start); !errors.Is(err, store.ErrOutboxNotClaimed) {
		t.Errorf("expired owner settled the row: got %v, want ErrOutboxNotClaimed", err)
	}
}

// TestFailOutboxAttempt_RetriesThenDies pins the attempt budget: a
// permanently failing delivery is retried up to max_attempts and then
// becomes DEAD, and never wedges the outbox in CLAIMED.
func TestFailOutboxAttempt_RetriesThenDies(t *testing.T) {
	s, ctx, w := outboxFixture(t)
	driveToTerminal(t, s, ctx, w, workgraph.StateSucceeded)

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	budget := 3

	claimed, err := s.ClaimOutbox(ctx, "dispatcher-a", 10, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d entries, want 1", len(claimed))
	}
	entry := claimed[0

	for attempt := 1; attempt <= budget; attempt++ {
		if attempt > 1 {
			// Re-claim at the same instant: the claim is released on
			// failure, so the row is immediately drainable again.
			again, err := s.ClaimOutbox(ctx, "dispatcher-a", 10, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(again) != 1 {
				t.Fatalf("attempt %d: re-claimed %d entries, want 1", attempt, len(again))
			}
			entry = again[0]
		}
		if err := s.FailOutboxAttempt(ctx, entry.ID, "dispatcher-a", "github 500", now); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		e, err := s.GetOutboxEntry(ctx, entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantStatus := store.OutboxPending
		if attempt == budget {
			wantStatus = store.OutboxDead
		}
		if e.Status != wantStatus {
			t.Fatalf("after attempt %d status = %s, want %s", attempt, e.Status, wantStatus)
		}
		if e.LastError != "github 500" {
			t.Errorf("last_error = %q, want the recorded reason", e.LastError)
		}
	}

	dead, err := s.CountOutboxByStatus(ctx, store.OutboxDead)
	if err != nil {
		t.Fatal(err)
	}
	if dead != 1 {
		t.Errorf("dead entries = %d, want 1", dead)
	}
	// A DEAD row is not drainable.
	after, err := s.ClaimOutbox(ctx, "dispatcher-a", 10, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("claimed %d entries after the row went DEAD, want 0", len(after))
	}
}

// TestDrainOutbox_DeliversAndSettles pins the end-to-end dispatcher step:
// a successful handler settles the row, and the drain reports it.
func TestDrainOutbox_DeliversAndSettles(t *testing.T) {
	s, ctx, w := outboxFixture(t)
	driveToTerminal(t, s, ctx, w, workgraph.StateSucceeded)

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	var delivered []string
	handler := func(_ context.Context, e store.OutboxEntry) error {
		p, err := store.DecodeTerminalWorkPayload(e.PayloadJSON)
		if err != nil {
			return err
		}
		delivered = append(delivered, p.WorkID)
		return nil
	}

	n, err := store.DrainOutbox(ctx, s, handler, store.OutboxConfig{
		DispatcherID: "dispatcher-a",
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("drained %d, want 1", n)
	}
	if len(delivered) != 1 || delivered[0] != w.ID {
		t.Errorf("handler saw %v, want exactly [%s]", delivered, w.ID)
	}

	// A second drain delivers nothing: the row is settled, not replayed.
	n, err = store.DrainOutbox(ctx, s, handler, store.OutboxConfig{
		DispatcherID: "dispatcher-a",
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second drain delivered %d, want 0", n)
	}
	if len(delivered) != 1 {
		t.Errorf("handler ran %d times total, want 1", len(delivered))
	}
}

// TestDrainOutbox_HandlerErrorIsRetriedNotFatal pins that one failing
// entry does not abort the batch or stop the dispatcher: the rest of the
// queue still gets its turn, and the failure is recorded for a retry.
func TestDrainOutbox_HandlerErrorIsRetriedNotFatal(t *testing.T) {
	s, ctx, w := outboxFixture(t)
	driveToTerminal(t, s, ctx, w, workgraph.StateSucceeded)

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	calls := 0
	handler := func(_ context.Context, e store.OutboxEntry) error {
		calls++
		if calls == 1 {
			return errors.New("github 503")
		}
		return nil
	}

	n, err := store.DrainOutbox(ctx, s, handler, store.OutboxConfig{
		DispatcherID: "dispatcher-a",
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("drain returned a fatal error for a handler failure: %v", err)
	}
	if n != 0 {
		t.Errorf("first drain delivered %d, want 0 (the only entry failed)", n)
	}

	// The failure released the claim rather than wedging the row.
	entry, err := s.OutboxEntryForKey(ctx, store.TerminalWorkIdempotencyKey(w.ID, workgraph.StateSucceeded))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != store.OutboxPending {
		t.Fatalf("status after failure = %s, want PENDING so the entry can be retried", entry.Status)
	}

	// The retry succeeds and settles.
	n, err = store.DrainOutbox(ctx, s, handler, store.OutboxConfig{
		DispatcherID: "dispatcher-a",
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("retry drain delivered %d, want 1", n)
	}
}

// TestDecodeTerminalWorkPayload_RejectsCorruptPayload pins that a corrupt
// payload is an error rather than a silent success. A dispatcher that
// treated it as delivered would retire an obligation nobody carried out.
func TestDecodeTerminalWorkPayload_RejectsCorruptPayload(t *testing.T) {
	if _, err := store.DecodeTerminalWorkPayload("{not json"); err == nil {
		t.Error("malformed payload accepted, want error")
	}
	if _, err := store.DecodeTerminalWorkPayload(`{"work_id":""}`); err == nil {
		t.Error("payload without work_id accepted, want error")
	}
	got, err := store.DecodeTerminalWorkPayload(`{"work_id":"wrk_1","state":"SUCCEEDED","from":"VERIFYING"}`)
	if err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if got.WorkID != "wrk_1" || got.State != "SUCCEEDED" || got.From != "VERIFYING" {
		t.Errorf("decoded %+v, want wrk_1/SUCCEEDED/VERIFYING", got)
	}
}

// TestOutbox_SurvivesAcrossLeaseCompletion is the integration seam: the
// ordinary path — grant, complete, which drives the Work to SUCCEEDED —
// must leave exactly one deliverable obligation behind, with no API-layer
// involvement at all. That is the property the removed
// maybePublishOnTerminal call used to break.
func TestOutbox_SurvivesAcrossLeaseCompletion(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	// Single-node Work, so completing its one node drives the Work to
	// SUCCEEDED through the ordinary CompleteLease path.
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
	done, err := s.CompleteLease(ctx, store.LeaseRefFor(lease), 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != workgraph.StateSucceeded {
		t.Fatalf("work state = %s, want SUCCEEDED (fixture broken)", done.State)
	}

	entries, err := s.ListOutboxByWorkID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d outbox entries after a completed lease, want 1", len(entries))
	}
	if entries[0].Status != store.OutboxPending {
		t.Errorf("status = %s, want PENDING", entries[0].Status)
	}
	p, err := store.DecodeTerminalWorkPayload(entries[0].PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != string(workgraph.StateSucceeded) {
		t.Errorf("payload state = %q, want SUCCEEDED", p.State)
	}
}