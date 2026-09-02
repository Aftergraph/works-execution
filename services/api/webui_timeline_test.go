package api_test

// k-038: WebUI work-detail journal timeline tests.
//
// Uses a real SQLite store (store.Open on a temp dir), drives state
// transitions through the journal-owned mutation wrappers so work_events
// rows exist, then GETs the detail page via httptest and asserts:
//   - the Journal section renders (header + SEQ/TIME/TYPE/SUMMARY)
//   - state transitions show the compact FROM -> TO summary
//   - hostile data_json (<script>) renders ESCAPED (&lt;script&gt;), never raw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// newJournalTestServer builds a Server on a real store with the WebUI
// mounted (public mode — auth is not under test here; requireBearer
// wraps it identically).
func newJournalTestServer(t *testing.T) (*httptest.Server, *store.SQLiteStore) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "timeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := &api.Server{Store: s, WebUI: &api.WebUIConfig{Public: true}}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, s
}

// newTimelineWork mints a minimal valid Work (server-style id).
func newTimelineWork(id string) *workgraph.Work {
	return &workgraph.Work{
		ID:        "wrk_" + id,
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "echo a"}},
		},
	}
}

// TestWebUIWorkDetailJournalTimeline drives CREATED -> QUEUED -> RUNNING
// through the Eventful wrappers, then asserts the detail page renders the
// journal section with the FROM -> TO summaries.
func TestWebUIWorkDetailJournalTimeline(t *testing.T) {
	ts, s := newJournalTestServer(t)
	ctx := context.Background()

	w := newTimelineWork("timeline01")
	if err := s.CreateWorkEventful(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.UpdateStateEventful(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatalf("to queued: %v", err)
	}
	if _, err := s.UpdateStateEventful(ctx, w.ID, workgraph.StateRunning); err != nil {
		t.Fatalf("to running: %v", err)
	}

	resp, err := http.Get(ts.URL + "/v1/ui/works/" + w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	body := readAllBody(t, resp)

	// Section header + columns present.
	for _, want := range []string{"<h2>Journal</h2>", "<th>Seq</th>", "<th>Time</th>", "<th>Type</th>", "<th>Summary</th>"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing journal markup %q", want)
		}
	}
	// Journal row types present.
	if !strings.Contains(body, "work.created") || !strings.Contains(body, "work.state.changed") {
		t.Errorf("detail page missing journal event types; body:\n%s", body)
	}
	// Compact FROM -> TO summaries for the state transitions.
	if !strings.Contains(body, "CREATED -&gt; QUEUED") {
		t.Errorf("missing CREATED -> QUEUED summary (escaped); body:\n%s", body)
	}
	if !strings.Contains(body, "QUEUED -&gt; RUNNING") {
		t.Errorf("missing QUEUED -> RUNNING summary (escaped); body:\n%s", body)
	}
}

// TestWebUIJournalEscapesHostileDataJSON inserts a journal row whose
// data_json carries a script tag directly (ids are server-minted, so the
// only injection surface is the payload) and asserts the page escapes it.
func TestWebUIJournalEscapesHostileDataJSON(t *testing.T) {
	ts, s := newJournalTestServer(t)
	ctx := context.Background()

	w := newTimelineWork("xss")
	if err := s.CreateWorkEventful(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Forge a hostile journal row via the durable journal API.
	if _, err := s.AppendWorkEvent(ctx, store.WorkEvent{
		ID:     "evt_hostile_xss",
		WorkID: w.ID,
		Type:   "work.state.changed",
		Data:   []byte(`{"work_id":"` + w.ID + `","from":"<script>alert(1)</script>","state":"<img src=x onerror=alert(2)>"}`),
	}); err != nil {
		t.Fatalf("append hostile event: %v", err)
	}

	resp, err := http.Get(ts.URL + "/v1/ui/works/" + w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAllBody(t, resp)

	// The payload must be escaped, not raw.
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("hostile <script> not escaped (missing &lt;script&gt;); body:\n%s", body)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("RAW <script> tag leaked into page output; body:\n%s", body)
	}
	if strings.Contains(body, "<img src=x onerror=") {
		t.Errorf("RAW <img onerror> payload leaked into page output; body:\n%s", body)
	}
	if !strings.Contains(body, "&lt;img src=x onerror=alert(2)&gt;") {
		t.Errorf("img payload not escaped; body:\n%s", body)
	}
}

// TestWebUIJournalLast50capsAndTail verifies the timeline shows the last
// 50 events (tail of the journal), not the head.
func TestWebUIJournalLast50capsAndTail(t *testing.T) {
	ts, s := newJournalTestServer(t)
	ctx := context.Background()

	w := newTimelineWork("tail")
	if err := s.CreateWorkEventful(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Drive 60+ transitions: CREATED -> QUEUED is the first hop; RUNNING
	// alternation keeps the state machine happy (RUNNING -> VERIFYING is
	// one-way, so bounce VERIFI... no: use CREATED->QUEUED->RUNNING then
	// synthetic non-state events via AppendWorkEvent for volume).
	for i := 0; i < 60; i++ {
		if _, err := s.AppendWorkEvent(ctx, store.WorkEvent{
			ID:     workgraph.NewID("evt"),
			WorkID: w.ID,
			Type:   "work.waiting_human",
			Data:   []byte(`{"work_id":"` + w.ID + `","note":"bulk-` + itoa(i) + `"}`),
		}); err != nil {
			t.Fatalf("append bulk %d: %v", i, err)
		}
	}

	resp, err := http.Get(ts.URL + "/v1/ui/works/" + w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAllBody(t, resp)

	// Timeline is capped at 50 rows: bulk-0 (head region) is pushed out.
	if !strings.Contains(body, "bulk-59") {
		t.Errorf("tail event bulk-59 missing; body:\n%s", body)
	}
	if strings.Contains(body, "bulk-9<") || strings.Contains(body, ">bulk-9<") {
		t.Errorf("head event bulk-9 should have scrolled off the 50-row tail; body:\n%s", body)
	}
	if got := strings.Count(body, "<td class=\"mono\">work.waiting_human</td>"); got > 50 {
		t.Errorf("journal rows = %d, want <= 50", got)
	}
}

// TestWebUIJournalEmptyState renders the empty-state row for a work with
// no journal rows.
func TestWebUIJournalEmptyState(t *testing.T) {
	ts, s := newJournalTestServer(t)
	ctx := context.Background()

	w := newTimelineWork("empty")
	if err := s.CreateWork(ctx, w); err != nil { // plain Create: no journal rows
		t.Fatalf("create: %v", err)
	}
	resp, err := http.Get(ts.URL + "/v1/ui/works/" + w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAllBody(t, resp)
	if !strings.Contains(body, "no journal events yet") {
		t.Errorf("missing empty-state journal row; body:\n%s", body)
	}
}

// helpers -----------------------------------------------------------------

func readAllBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := &strings.Builder{}
	b := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	return buf.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
