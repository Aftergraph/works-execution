package store_test

// Task 1 (docs/superpowers/plans/2026-09-01-works-conversation-v1.md):
// durable per-work event journal.
//
// Contract under test:
//   - sequences are monotonic per append and scoped per work
//   - appends are idempotent by event ID (retries keep the original sequence)
//   - events survive a store close + reopen (durable cursor)
//   - list limits clamp to 1..1000
//   - canonical mutations emit journal records after the canonical
//     transaction succeeds, and emission failures surface as errors.
import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func openJournalStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// journalWork creates a real Work with the given ID so work_events rows
// satisfy the works(id) foreign key.
func journalWork(t *testing.T, s *store.SQLiteStore, id string) {
	t.Helper()
	w := sampleWork()
	w.ID = id
	if err := s.CreateWork(context.Background(), w); err != nil {
		t.Fatalf("create work %s: %v", id, err)
	}
}

func TestWorkEventsAreMonotonicAndWorkScoped(t *testing.T) {
	s := openJournalStore(t)
	ctx := context.Background()
	journalWork(t, s, "work:a")
	journalWork(t, s, "work:b")

	first, err := s.AppendWorkEvent(ctx, store.WorkEvent{ID: "evt_a", WorkID: "work:a", Type: "work.created", Data: json.RawMessage(`{"state":"CREATED"}`)})
	if err != nil {
		t.Fatalf("append evt_a: %v", err)
	}
	second, err := s.AppendWorkEvent(ctx, store.WorkEvent{ID: "evt_b", WorkID: "work:a", Type: "work.state.changed", Data: json.RawMessage(`{"state":"QUEUED"}`)})
	if err != nil {
		t.Fatalf("append evt_b: %v", err)
	}
	if second.Sequence <= first.Sequence {
		t.Fatalf("sequence did not increase: first=%d second=%d", first.Sequence, second.Sequence)
	}
	// Event for a different work must not interleave into work:a's stream.
	if _, err := s.AppendWorkEvent(ctx, store.WorkEvent{ID: "evt_c", WorkID: "work:b", Type: "work.created", Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("append evt_c: %v", err)
	}

	rows, err := s.ListWorkEventsAfter(ctx, "work:a", first.Sequence, 100)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "evt_b" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
	// Full history for work:a is ascending by sequence and never leaks work:b.
	all, err := s.ListWorkEventsAfter(ctx, "work:a", 0, 100)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 events for work:a, got %d: %#v", len(all), all)
	}
	for i, ev := range all {
		if ev.WorkID != "work:a" {
			t.Errorf("row %d leaked work %q", i, ev.WorkID)
		}
		if i > 0 && ev.Sequence <= all[i-1].Sequence {
			t.Errorf("rows not ascending by sequence at index %d", i)
		}
	}
	if all[0].ID != "evt_a" || all[1].ID != "evt_b" {
		t.Fatalf("unexpected ordering: %#v", all)
	}
}

func TestWorkEventIdempotencyByID(t *testing.T) {
	s := openJournalStore(t)
	ctx := context.Background()
	journalWork(t, s, "work:a")

	input := store.WorkEvent{ID: "evt_same", WorkID: "work:a", Type: "evidence.recorded", Data: json.RawMessage(`{"id":"e1"}`)}
	a, err := s.AppendWorkEvent(ctx, input)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	b, err := s.AppendWorkEvent(ctx, input)
	if err != nil {
		t.Fatalf("retry append: %v", err)
	}
	if a.Sequence != b.Sequence {
		t.Fatalf("duplicate event allocated new sequence: a=%d b=%d", a.Sequence, b.Sequence)
	}
	if b.ID != a.ID || b.Type != a.Type || string(b.Data) != string(a.Data) {
		t.Fatalf("retry returned different row: a=%+v b=%+v", a, b)
	}
	// Exactly one row must exist for that ID.
	rows, err := s.ListWorkEventsAfter(ctx, "work:a", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("duplicate rows persisted: %#v", rows)
	}
}

func TestWorkEventsPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.db")
	ctx := context.Background()

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	w := sampleWork()
	w.ID = "work:reopen"
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := s.AppendWorkEvent(ctx, store.WorkEvent{ID: "evt_r1", WorkID: "work:reopen", Type: "work.created", Data: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	second, err := s.AppendWorkEvent(ctx, store.WorkEvent{ID: "evt_r2", WorkID: "work:reopen", Type: "work.state.changed", Data: json.RawMessage(`{"state":"QUEUED"}`)})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen the same database: the cursor and rows must survive.
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	latest, err := s2.LatestWorkEventSequence(ctx, "work:reopen")
	if err != nil {
		t.Fatalf("latest after reopen: %v", err)
	}
	if latest != second.Sequence {
		t.Fatalf("cursor lost on reopen: latest=%d want=%d", latest, second.Sequence)
	}
	rows, err := s2.ListWorkEventsAfter(ctx, "work:reopen", 0, 100)
	if err != nil {
		t.Fatalf("list after reopen: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "evt_r1" || rows[1].ID != "evt_r2" {
		t.Fatalf("rows lost or reordered on reopen: %#v", rows)
	}
	if rows[0].Sequence != first.Sequence {
		t.Fatalf("sequence changed on reopen: got %d want %d", rows[0].Sequence, first.Sequence)
	}

	oldest, err := s2.OldestWorkEventSequence(ctx, "work:reopen")
	if err != nil {
		t.Fatalf("oldest after reopen: %v", err)
	}
	if oldest != first.Sequence {
		t.Fatalf("oldest=%d want=%d", oldest, first.Sequence)
	}
}

func TestListWorkEventsAfterClampsLimit(t *testing.T) {
	s := openJournalStore(t)
	ctx := context.Background()
	journalWork(t, s, "work:a")
	for _, id := range []string{"evt_1", "evt_2", "evt_3"} {
		if _, err := s.AppendWorkEvent(ctx, store.WorkEvent{ID: id, WorkID: "work:a", Type: "work.created", Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}
	// limit <= 0 clamps to 1.
	rows, err := s.ListWorkEventsAfter(ctx, "work:a", 0, 0)
	if err != nil {
		t.Fatalf("list zero: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("limit 0: want exactly 1 row, got %d", len(rows))
	}
	// Negative limit also clamps to 1.
	rows, err = s.ListWorkEventsAfter(ctx, "work:a", 0, -5)
	if err != nil {
		t.Fatalf("list negative: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("limit -5: want exactly 1 row, got %d", len(rows))
	}
}

func TestCanonicalMutationsEmitJournalEvents(t *testing.T) {
	s := openJournalStore(t)
	ctx := context.Background()
	w := sampleWork()
	w.ID = "work:emit"
	if err := s.CreateWorkEventful(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.UpdateStateEventful(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatalf("update state: %v", err)
	}
	if _, err := s.AppendAttemptEventful(ctx, w.ID, workgraph.Attempt{ID: "att_1", NodeID: "a", Status: "running"}); err != nil {
		t.Fatalf("append attempt: %v", err)
	}
	if _, err := s.AppendEvidenceEventful(ctx, w.ID, workgraph.Evidence{ID: "evd_1", NodeID: "a", AttemptID: "att_1", Type: "log", Result: "ok", RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("append evidence: %v", err)
	}
	if _, err := s.AppendArtifactEventful(ctx, w.ID, workgraph.Artifact{ID: "art_1", NodeID: "a", MimeType: "text/plain", Size: 3, Path: "/tmp/a.txt"}); err != nil {
		t.Fatalf("append artifact: %v", err)
	}
	lease, _, err := s.GrantLeaseEventful(ctx, w.ID, "a", "worker_1", 30*time.Second)
	if err != nil {
		t.Fatalf("grant lease: %v", err)
	}
	if _, err := s.RenewLeaseEventful(ctx, lease.ID, 30*time.Second); err != nil {
		t.Fatalf("renew lease: %v", err)
	}
	if _, err := s.CompleteLeaseEventful(ctx, lease.ID, 0, nil, nil); err != nil {
		t.Fatalf("complete lease: %v", err)
	}

	want := map[string]bool{
		"work.created":           false,
		"work.state.changed":     false,
		"activity.attempt.changed": false,
		"evidence.recorded":      false,
		"artifact.created":       false,
		"worker.lease.granted":   false,
		"worker.lease.renewed":   false,
		"worker.lease.completed": false,
	}
	rows, err := s.ListWorkEventsAfter(ctx, w.ID, 0, 1000)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, ev := range rows {
		if _, ok := want[ev.Type]; ok {
			want[ev.Type] = true
		}
	}
	for typ, seen := range want {
		if !seen {
			t.Errorf("mutation journal missing event type %s (got %#v types)", typ, eventTypes(rows))
		}
	}
	// Sequences strictly ascending, scoped to this work only.
	for i, ev := range rows {
		if ev.WorkID != w.ID {
			t.Errorf("event %d leaked work %q", ev.Sequence, ev.WorkID)
		}
		if i > 0 && ev.Sequence <= rows[i-1].Sequence {
			t.Errorf("journal not ascending at index %d", i)
		}
	}
}

func TestSuspendAndResumeEmitJournalEvents(t *testing.T) {
	s := openJournalStore(t)
	ctx := context.Background()
	w := newMissionWork("j")
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	// QUEUED -> RUNNING so suspend is reachable.
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateRunning); err != nil {
		t.Fatalf("to running: %v", err)
	}
	if _, err := s.SuspendWorkEventful(ctx, w.ID, workgraph.StateSuspended, missionHandoff("journal pause")); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, _, err := s.ResumeFromCheckpointEventful(ctx, w.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	rows, err := s.ListWorkEventsAfter(ctx, w.ID, 0, 1000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	types := map[string]bool{}
	for _, ev := range rows {
		types[ev.Type] = true
	}
	if !types["work.suspended"] {
		t.Errorf("missing work.suspended event; got %v", types)
	}
	if !types["work.resumed"] {
		t.Errorf("missing work.resumed event; got %v", types)
	}
	// SuspendWork to WAITING_HUMAN must journal work.waiting_human, not work.suspended.
	s2 := openJournalStore(t)
	w2 := newMissionWork("k")
	if err := s2.CreateWork(ctx, w2); err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if _, err := s2.UpdateState(ctx, w2.ID, workgraph.StateRunning); err != nil {
		t.Fatalf("to running 2: %v", err)
	}
	if _, err := s2.SuspendWorkEventful(ctx, w2.ID, workgraph.StateWaitingHuman, missionHandoff("need human")); err != nil {
		t.Fatalf("suspend 2: %v", err)
	}
	rows2, err := s2.ListWorkEventsAfter(ctx, w2.ID, 0, 1000)
	if err != nil {
		t.Fatalf("list 2: %v", err)
	}
	gotWaiting := false
	for _, ev := range rows2 {
		if ev.Type == "work.waiting_human" {
			gotWaiting = true
		}
		if ev.Type == "work.suspended" {
			t.Errorf("WAITING_HUMAN suspend journaled as work.suspended")
		}
	}
	if !gotWaiting {
		t.Errorf("missing work.waiting_human event; got %v", eventTypes(rows2))
	}
}

func eventTypes(rows []store.WorkEvent) []string {
	out := make([]string, 0, len(rows))
	for _, ev := range rows {
		out = append(out, ev.Type)
	}
	return out
}