package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/publisher"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// ADR-0033 at the HTTP boundary: the fencing epoch is on the wire, and a
// request that omits or misstates it is refused rather than silently
// treated as unfenced.

// leaseFixture creates a Work with one node, grants a lease through the
// store, and returns an httptest server plus the lease.
func leaseFixture(t *testing.T) (*api.Server, *httptest.Server, store.Store, *workgraph.Work, *workgraph.Lease) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := &api.Server{Store: s}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

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
	return srv, ts, s, w, lease
}

func leasePostJSON(t *testing.T, ts *httptest.Server, path, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestGrantResponseCarriesEpoch pins that the worker can actually obtain
// the fencing token. Without the epoch on the grant response the worker
// has nothing to present, and every subsequent verb would be refused.
func TestGrantResponseCarriesEpoch(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := &api.Server{Store: s}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

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

	body := `{"work_id":"` + w.ID + `","node_id":"only","worker_id":"wrkr_1"}`
	code, out := leasePostJSON(t, ts, "/v1/leases/grant", body)
	if code != http.StatusCreated {
		t.Fatalf("grant status = %d, want 201 (body %v)", code, out)
	}
	lease, ok := out["lease"].(map[string]any)
	if !ok {
		t.Fatalf("grant response has no lease object: %v", out)
	}
	epoch, ok := lease["epoch"].(float64)
	if !ok {
		t.Fatalf("grant lease carries no epoch: %v", lease)
	}
	if epoch < 1 {
		t.Errorf("grant epoch = %v, want >= 1", epoch)
	}
}

// TestHeartbeatWithoutEpochIsRefused pins the fail-closed wire contract. A
// caller that omits the epoch presents the zero value, which matches no
// real lease generation, and is told so.
func TestHeartbeatWithoutEpochIsRefused(t *testing.T) {
	_, ts, _, _, lease := leaseFixture(t)

	code, out := leasePostJSON(t, ts, "/v1/leases/"+lease.ID+"/heartbeat", `{"ttl_seconds":30}`)
	if code != http.StatusConflict {
		t.Fatalf("heartbeat without epoch: status = %d, want 409 (body %v)", code, out)
	}
	if got := out["error"]; got != api.ReasonLeaseFenced {
		t.Errorf("error code = %v, want %q", got, api.ReasonLeaseFenced)
	}
}

// TestHeartbeatWithCurrentEpochSucceeds is the positive control: the same
// request with the epoch the grant issued must go through, otherwise the
// negative test above proves nothing.
func TestHeartbeatWithCurrentEpochSucceeds(t *testing.T) {
	_, ts, _, _, lease := leaseFixture(t)

	body := `{"ttl_seconds":30,"epoch":` + leaseItoa(lease.Epoch) + `}`
	code, out := leasePostJSON(t, ts, "/v1/leases/"+lease.ID+"/heartbeat", body)
	if code != http.StatusOK {
		t.Fatalf("heartbeat with current epoch: status = %d, want 200 (body %v)", code, out)
	}
}

// TestReleaseWithoutEpochIsRefused pins the same contract on a second verb,
// so the epoch requirement cannot be satisfied on one verb and forgotten
// on another.
func TestReleaseWithoutEpochIsRefused(t *testing.T) {
	_, ts, _, _, lease := leaseFixture(t)

	code, out := leasePostJSON(t, ts, "/v1/leases/"+lease.ID+"/release", `{"reason":"giving up"}`)
	if code != http.StatusConflict {
		t.Fatalf("release without epoch: status = %d, want 409 (body %v)", code, out)
	}
	if got := out["error"]; got != api.ReasonLeaseFenced {
		t.Errorf("error code = %v, want %q", got, api.ReasonLeaseFenced)
	}
}

// TestCompleteWithStaleEpochIsRefused pins end-to-end that a zombie worker
// cannot report a result over a lease that has moved on.
//
// The wire code is lease_not_active, not lease_fenced: re-granting creates a
// NEW lease row and leaves the zombie's own row RELEASED, and the store
// classifies status before the token (ADR-0033). Both are 409 and both
// refuse the write; this test pins which one the worker actually sees, so a
// change to the precedence rule cannot pass silently.
func TestCompleteWithStaleEpochIsRefused(t *testing.T) {
	_, ts, s, w, stale := leaseFixture(t)
	ctx := context.Background()

	if err := s.ReleaseLease(ctx, store.LeaseRefFor(stale), "expired"); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := s.GrantLease(ctx, w.ID, "only", "wrkr_2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// A failed completion (non-zero exit) needs no artifact, so the request
	// reaches the lease fence itself. With exit_code 0 and no artifact the
	// handler refuses earlier with 422 artifact_required, which would test
	// the artifact rule rather than the fence.
	body := `{"exit_code":1,"epoch":` + leaseItoa(stale.Epoch) + `}`
	code, out := leasePostJSON(t, ts, "/v1/leases/"+stale.ID+"/complete", body)
	if code != http.StatusConflict {
		t.Fatalf("stale complete: status = %d, want 409 (body %v)", code, out)
	}
	if got := out["error"]; got != "lease_not_active" {
		t.Errorf("error code = %v, want lease_not_active", got)
	}

	// The current holder is unaffected.
	final, err := s.GetLease(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != workgraph.LeaseActive {
		t.Errorf("current holder lease = %s after a refused stale complete, want ACTIVE", final.Status)
	}
}

// NOTE ON THE WRONG-EXECUTOR CASE.
//
// There is deliberately no HTTP-level test here for a wrong-executor
// completion. In dev mode (AuthEnabled=false) leaseExecutorIdentity resolves
// the executor FROM the lease row itself, so an HTTP caller can never present
// an executor that differs from the stored one — the scenario is unreachable
// through this surface without full claim-bearing auth setup.
//
// The invariant is pinned one layer down, where it is the store's job and
// is directly reachable: see store.TestFencedVerbs_RefuseWrongExecutor and
// store.TestFencedVerbs_EpochMismatchOnActiveLeaseIsFenced.


// Outbox delivery through the API dispatcher.

// TestOutboxDispatcher_PublishesTerminalWorkOnce is the migration proof:
// the terminal transition alone produces a durable obligation, and one
// dispatcher drain delivers it exactly once — with no publish call on the
// request path.
func TestOutboxDispatcher_PublishesTerminalWorkOnce(t *testing.T) {
	pub := publisher.NewNoopPublisher("test")
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := &api.Server{Store: s, Publisher: pub}
	ctx := context.Background()

	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "github_push", Repository: "acme/demo", SHA: "abcdef0123456789abcdef0123456789abcdef01"},
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

	// Nothing published just by creating and queueing.
	if got := len(pub.Snapshot()); got != 0 {
		t.Fatalf("publisher called %d times before any terminal transition, want 0", got)
	}

	for _, st := range []workgraph.State{
		workgraph.StateRunning, workgraph.StateVerifying, workgraph.StateSucceeded,
	} {
		if _, err := s.UpdateState(ctx, w.ID, st); err != nil {
			t.Fatalf("UpdateState %s: %v", st, err)
		}
	}

	// Still nothing: delivery is the dispatcher's job, not the mutation's.
	if got := len(pub.Snapshot()); got != 0 {
		t.Fatalf("publisher called %d times at commit time, want 0 — delivery must be deferred", got)
	}

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	cfg := store.OutboxConfig{DispatcherID: "dispatcher-a", Now: func() time.Time { return now }}
	handler := srv.OutboxPublishHandlerForTest()
	if handler == nil {
		t.Fatal("no outbox publish handler exposed")
	}

	n, err := store.DrainOutbox(ctx, s, handler, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("drained %d entries, want 1", n)
	}
	rec := pub.Snapshot()
	if len(rec) != 1 {
		t.Fatalf("publisher called %d times, want exactly 1", len(rec))
	}
	if rec[0].Repository != "acme/demo" {
		t.Errorf("repo = %q, want acme/demo", rec[0].Repository)
	}
	if rec[0].Conclusion != publisher.ConclusionSuccess {
		t.Errorf("conclusion = %q, want success", rec[0].Conclusion)
	}

	// Draining again must not republish: the obligation is settled.
	n, err = store.DrainOutbox(ctx, s, handler, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second drain delivered %d, want 0", n)
	}
	if got := len(pub.Snapshot()); got != 1 {
		t.Errorf("publisher called %d times after a second drain, want 1", got)
	}
}

// TestOutboxDispatcher_RetriesTransientPublisherFailure pins that a
// publisher outage does not lose the status update: the entry stays
// drainable and a later drain succeeds.
func TestOutboxDispatcher_RetriesTransientPublisherFailure(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var failNext bool
	pub := &flakyPublisher{failNext: &failNext}
	srv := &api.Server{Store: s, Publisher: pub}
	ctx := context.Background()

	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "github_push", Repository: "acme/demo", SHA: "abcdef0123456789abcdef0123456789abcdef01"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"only": {ID: "only", Run: "echo ok"},
		}},
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	for _, st := range []workgraph.State{
		workgraph.StateQueued, workgraph.StateRunning, workgraph.StateVerifying, workgraph.StateSucceeded,
	} {
		if _, err := s.UpdateState(ctx, w.ID, st); err != nil {
			t.Fatalf("UpdateState %s: %v", st, err)
		}
	}

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	cfg := store.OutboxConfig{DispatcherID: "dispatcher-a", Now: func() time.Time { return now }}
	handler := srv.OutboxPublishHandlerForTest()

	failNext = true
	n, err := store.DrainOutbox(ctx, s, handler, cfg)
	if err != nil {
		t.Fatalf("drain with a failing publisher returned a fatal error: %v", err)
	}
	if n != 0 {
		t.Errorf("drained %d while the publisher was failing, want 0", n)
	}

	// The obligation survived the failure — this is the whole point of the
	// outbox versus the old fire-and-forget hook.
	failNext = false
	n, err = store.DrainOutbox(ctx, s, handler, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("retry drained %d, want 1", n)
	}
	if pub.calls != 2 {
		t.Errorf("publisher called %d times, want 2 (one failure + one success)", pub.calls)
	}
}

// TestOutboxDispatcher_UnpublishableWorkIsSettledWithoutError pins that a
// CLI Work with no GitHub provenance does not burn its retry budget. It is
// retired quietly; otherwise every local run would end DEAD and the DEAD
// state would mean nothing.
func TestOutboxDispatcher_UnpublishableWorkIsSettledWithoutError(t *testing.T) {
	pub := publisher.NewNoopPublisher("test")
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := &api.Server{Store: s, Publisher: pub}
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
	for _, st := range []workgraph.State{
		workgraph.StateQueued, workgraph.StateRunning, workgraph.StateVerifying, workgraph.StateSucceeded,
	} {
		if _, err := s.UpdateState(ctx, w.ID, st); err != nil {
			t.Fatalf("UpdateState %s: %v", st, err)
		}
	}

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	handler := srv.OutboxPublishHandlerForTest()
	n, err := store.DrainOutbox(ctx, s, handler, store.OutboxConfig{
		DispatcherID: "dispatcher-a", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("drain for an unpublishable work: %v", err)
	}
	if n != 1 {
		t.Errorf("drained %d, want 1 (settled without a side effect)", n)
	}
	if got := len(pub.Snapshot()); got != 0 {
		t.Errorf("publisher called %d times for a CLI work, want 0", got)
	}
	dead, err := s.CountOutboxByStatus(ctx, store.OutboxDead)
	if err != nil {
		t.Fatal(err)
	}
	if dead != 0 {
		t.Errorf("dead entries = %d, want 0 — an unpublishable work is retired, not retried to death", dead)
	}
}

// flakyPublisher fails on demand so the retry path is exercised without a
// real HTTP dependency.
type flakyPublisher struct {
	failNext *bool
	calls    int
}

func (f *flakyPublisher) Publish(_ context.Context, _ publisher.Result) error {
	f.calls++
	if f.failNext != nil && *f.failNext {
		return errors.New("simulated github 503")
	}
	return nil
}

func (f *flakyPublisher) Kind() string { return "flaky" }

func leaseItoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}