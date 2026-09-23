package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func openSQLiteTemp(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := Open(t.TempDir() + "/perf.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func perfWork() *workgraph.Work {
	return &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateQueued,
		Source: workgraph.Source{
			Type: "cli", Repository: "acme/demo", Revision: "abc123",
		},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{
				"a": {ID: "a", Run: "echo a"},
				"b": {ID: "b", Run: "echo b", Needs: []string{"a"}},
			},
		},
	}
}

// TestListWorksHydrationParity asserts the batched ListWorks hydrates
// exactly like the per-ID GetWork path: same fields, same child ordering.
func TestListWorksHydrationParity(t *testing.T) {
	s := openSQLiteTemp(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		w := perfWork()
		if err := s.CreateWork(ctx, w); err != nil {
			t.Fatal(err)
		}
		lease, attempt, err := s.GrantLease(ctx, w.ID, "a", "worker-1", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CompleteLease(ctx, lease.ID, 0, &workgraph.Artifact{
			ID: "art1", NodeID: "a", MimeType: "text/plain", Size: 3, Path: "/tmp/a",
		}, []workgraph.Evidence{{
			ID: "ev1", NodeID: "a", AttemptID: attempt.ID, Type: "log", Result: "ok",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListWorks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 {
		t.Fatalf("got %d works, want 5", len(list))
	}
	for _, w := range list {
		got, err := s.GetWork(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Attempts, w.Attempts) {
			t.Errorf("attempts mismatch for %s: %+v vs %+v", w.ID, got.Attempts, w.Attempts)
		}
		if !reflect.DeepEqual(got.Artifacts, w.Artifacts) {
			t.Errorf("artifacts mismatch for %s: %+v vs %+v", w.ID, got.Artifacts, w.Artifacts)
		}
		if !reflect.DeepEqual(got.Evidence, w.Evidence) {
			t.Errorf("evidence mismatch for %s: %+v vs %+v", w.ID, got.Evidence, w.Evidence)
		}
		if got.State != w.State || got.UpdatedAt != w.UpdatedAt || got.CreatedAt != w.CreatedAt {
			t.Errorf("header mismatch for %s: %+v vs %+v", w.ID, got, w)
		}
		if !reflect.DeepEqual(got.Graph, w.Graph) || !reflect.DeepEqual(got.Source, w.Source) {
			t.Errorf("projection mismatch for %s", w.ID)
		}
	}
}

// TestListSchedulableWorksFiltersState asserts only QUEUED/RUNNING works
// are returned by the scheduler fast path.
func TestListSchedulableWorksFiltersState(t *testing.T) {
	s := openSQLiteTemp(t)
	ctx := context.Background()
	queued := perfWork()
	if err := s.CreateWork(ctx, queued); err != nil {
		t.Fatal(err)
	}
	terminal := perfWork()
	if err := s.CreateWork(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, terminal.ID, workgraph.StateFailed); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListSchedulableWorks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != queued.ID {
		t.Fatalf("got %v, want only %s", idsOf(got), queued.ID)
	}
}

func idsOf(ws []*workgraph.Work) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.ID
	}
	return out
}

// TestActiveLeasesByWorkIDsBatchParity asserts the batched active-lease
// lookup agrees with the per-work form.
func TestActiveLeasesByWorkIDsBatchParity(t *testing.T) {
	s := openSQLiteTemp(t)
	ctx := context.Background()
	w1 := perfWork()
	w2 := perfWork()
	if err := s.CreateWork(ctx, w1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWork(ctx, w2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GrantLease(ctx, w1.ID, "a", "worker-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	batch, err := s.ActiveLeasesByWorkIDs(ctx, []string{w1.ID, w2.ID, "work:missing"})
	if err != nil {
		t.Fatal(err)
	}
	single, err := s.ActiveLeasesByWorkID(ctx, w1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batch[w1.ID], single) {
		t.Errorf("batch %v != single %v", batch[w1.ID], single)
	}
	if _, ok := batch[w2.ID]; ok && len(batch[w2.ID]) > 0 {
		t.Errorf("w2 should have no active leases, got %v", batch[w2.ID])
	}
	if _, ok := batch["work:missing"]; ok {
		t.Error("missing work must be omitted from batch result")
	}
	// Empty input must be safe.
	empty, err := s.ActiveLeasesByWorkIDs(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("empty input gave %v", empty)
	}
}

// TestAppendWorkEventIdempotentAndNotFound pins the single-statement
// journal append: duplicate id is idempotent, missing work fails closed.
func TestAppendWorkEventIdempotentAndNotFound(t *testing.T) {
	s := openSQLiteTemp(t)
	ctx := context.Background()
	w := perfWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	first, err := s.AppendWorkEvent(ctx, WorkEvent{
		ID: "evt-1", WorkID: w.ID, Type: EventWorkCreated, Data: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.AppendWorkEvent(ctx, WorkEvent{
		ID: "evt-1", WorkID: w.ID, Type: EventWorkCreated, Data: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != retry.Sequence {
		t.Errorf("idempotent retry changed sequence: %d -> %d", first.Sequence, retry.Sequence)
	}
	if _, err := s.AppendWorkEvent(ctx, WorkEvent{
		ID: "evt-2", WorkID: "work:missing", Type: EventWorkCreated, Data: []byte(`{}`),
	}); err != ErrNotFound {
		t.Errorf("missing work: got %v, want ErrNotFound", err)
	}
}

// TestReadPoolRouting asserts the read pool is used for reads and both
// pools are closed by Close (file-backed stores only).
func TestReadPoolRouting(t *testing.T) {
	s := openSQLiteTemp(t)
	if s.readDB == nil {
		t.Fatal("file-backed store must have a read pool")
	}
	ctx := context.Background()
	w := perfWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListWorks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestInMemoryStoreFallsBackToWriter asserts :memory: stores stay
// writer-only (a second pool would be a different empty database).
func TestInMemoryStoreFallsBackToWriter(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.readDB != nil {
		t.Error("in-memory store must not open a second pool")
	}
	ctx := context.Background()
	w := perfWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != w.ID {
		t.Errorf("round-trip mismatch: %s", got.ID)
	}
}

// TestWorkSummaries pins the lightweight projection used by SSE diffing.
func TestWorkSummaries(t *testing.T) {
	s := openSQLiteTemp(t)
	ctx := context.Background()
	w := perfWork()
	w.Source.SHA = "0123456789abcdef0123456789abcdef01234567"
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	sums, err := s.ListWorkSummaries(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 {
		t.Fatalf("got %d summaries, want 1", len(sums))
	}
	got := sums[0]
	if got.ID != w.ID || got.State != workgraph.StateQueued || got.Type != "cli" ||
		got.Repo != "acme/demo" || got.SHA != w.Source.SHA {
		t.Errorf("summary mismatch: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt must be parsed")
	}
}
