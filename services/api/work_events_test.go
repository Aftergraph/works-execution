package api_test

// Task 2 (docs/superpowers/plans/2026-09-01-works-conversation-v1.md):
// resumable WORKS SSE endpoint — GET /v1/works/{id}/events.
//
// Contract under test:
//   - snapshot + resume: Last-Event-ID=N streams only sequence > N,
//     each frame as "id: <seq>\nevent: <type>\ndata: <json>\n\n"
//   - SSE headers: Content-Type text/event-stream, X-Accel-Buffering: no,
//     Cache-Control: no-cache
//   - empty poll emits a comment heartbeat (": keepalive")
//   - a cursor older than the oldest retained event emits event: reset
//     with {"reason":"cursor_expired","work_id":...} and closes
//   - nonexistent / other-work IDs get the same 404 (no existence leak)
//   - bearer auth enforced through the server's requireBearer middleware
//
// The SSE handler depends on the minimal WorkEventLister interface, so
// these tests run against the real store without coupling the handler to
// Task 1's concrete journal (which lives in services/work/store).
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func openEventsTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	// SQLite needs a real disk path inside the test's own temp dir.
	s, err := store.Open(filepath.Join(t.TempDir(), "events-sse.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newEventsTestServer(t *testing.T, s *store.SQLiteStore) *httptest.Server {
	t.Helper()
	// The route wraps itself in requireBearer via WireWorkEventRoutes; with
	// AuthEnabled=false that middleware is a no-op (matches api.go).
	srv := &api.Server{
		Store:       s,
		AuthEnabled: false,
	}
	mux := http.NewServeMux()
	api.WireWorkEventRoutes(mux, srv)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func journalWork(t *testing.T, s *store.SQLiteStore, id string, state workgraph.State) *workgraph.Work {
	t.Helper()
	w := &workgraph.Work{
		ID:        id,
		Objective: workgraph.Objective{Type: "custom"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"do": {ID: "do", Run: "echo hi"}}},
		State:     state,
	}
	if err := s.CreateWork(context.Background(), w); err != nil {
		t.Fatalf("create work %s: %v", id, err)
	}
	return w
}

func appendEvent(t *testing.T, s *store.SQLiteStore, workID, id, typ, data string) store.WorkEvent {
	t.Helper()
	ev, err := s.AppendWorkEvent(context.Background(), store.WorkEvent{
		ID:     id,
		WorkID: workID,
		Type:   typ,
		Data:   json.RawMessage(data),
	})
	if err != nil {
		t.Fatalf("append event %s: %v", id, err)
	}
	return ev
}

// readSSE opens the stream and accumulates frames until the response body
// closes (server side) or the deadline passes. SSE is a long-lived stream:
// a single Read races the handler's flush cadence, so we drain with a
// deadline instead.
func readSSE(t *testing.T, ts *httptest.Server, path, lastEventID string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("SSE request: %v", err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 16*1024)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// The client's overall Timeout bounds each Read; when it fires we
		// get an error and stop draining.
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break // timeout or EOF: done draining
		}
	}
	return resp, sb.String()
}

func TestWorkEventsSSEStreamsOnlyAfterCursor(t *testing.T) {
	s := openEventsTestStore(t)
	w := journalWork(t, s, "work:sse-cursor", workgraph.StateRunning)
	first := appendEvent(t, s, w.ID, "evt_c1", "work.created", `{"state":"CREATED"}`)
	second := appendEvent(t, s, w.ID, "evt_c2", "work.state.changed", `{"state":"RUNNING"}`)
	if second.Sequence <= first.Sequence {
		t.Fatalf("sequence did not increase: %d then %d", first.Sequence, second.Sequence)
	}

	ts := newEventsTestServer(t, s)
	resp, body := readSSE(t, ts, fmt.Sprintf("/v1/works/%s/events", w.ID), fmt.Sprintf("%d", first.Sequence))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if !strings.Contains(body, fmt.Sprintf("id: %d\nevent: work.state.changed\ndata: {\"state\":\"RUNNING\"}\n\n", second.Sequence)) {
		t.Fatalf("stream missing resumed frame after cursor %d, got:\n%s", first.Sequence, body)
	}
	if strings.Contains(body, fmt.Sprintf("id: %d\n", first.Sequence)) {
		t.Fatalf("stream replayed event at/under cursor %d:\n%s", first.Sequence, body)
	}
}

func TestWorkEventsSSESnapshotFromZero(t *testing.T) {
	s := openEventsTestStore(t)
	w := journalWork(t, s, "work:sse-snap", workgraph.StateQueued)
	e1 := appendEvent(t, s, w.ID, "evt_s1", "work.created", `{"a":1}`)
	e2 := appendEvent(t, s, w.ID, "evt_s2", "work.state.changed", `{"a":2}`)

	ts := newEventsTestServer(t, s)
	resp, body := readSSE(t, ts, fmt.Sprintf("/v1/works/%s/events", w.ID), "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Without Last-Event-ID the cursor starts at 0: the full snapshot is
	// streamed in sequence order.
	i1 := strings.Index(body, fmt.Sprintf("id: %d\n", e1.Sequence))
	i2 := strings.Index(body, fmt.Sprintf("id: %d\n", e2.Sequence))
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Fatalf("snapshot frames out of order or missing:\n%s", body)
	}
}

func TestWorkEventsSSEHeartbeatOnEmptyPoll(t *testing.T) {
	s := openEventsTestStore(t)
	w := journalWork(t, s, "work:sse-idle", workgraph.StateRunning)

	ts := newEventsTestServer(t, s)
	_, body := readSSE(t, ts, fmt.Sprintf("/v1/works/%s/events", w.ID), "")

	if !strings.Contains(body, ": keepalive\n\n") {
		t.Fatalf("expected keepalive comment heartbeat, got:\n%s", body)
	}
}

func TestWorkEventsSSEResetOnExpiredCursor(t *testing.T) {
	s := openEventsTestStore(t)
	w := journalWork(t, s, "work:sse-reset", workgraph.StateRunning)
	ev := appendEvent(t, s, w.ID, "evt_r1", "work.created", `{}`)

	ts := newEventsTestServer(t, s)
	// Cursor 1 is below the retained oldest sequence (ev.Sequence >= 1 with
	// AUTOINCREMENT global sequence): the stream must declare the gap.
	resp, body := readSSE(t, ts, fmt.Sprintf("/v1/works/%s/events", w.ID), "0")

	// cursor 0 == fresh snapshot, so use the real oldest seq to build a gap.
	_ = ev
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Now a genuinely expired cursor: older than the oldest retained event.
	resp2, body2 := readSSE(t, ts, fmt.Sprintf("/v1/works/%s/events", w.ID), fmt.Sprintf("%d", ev.Sequence-1))
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (reset is an in-band SSE event)", resp2.StatusCode)
	}
	want := fmt.Sprintf("event: reset\ndata: {\"reason\":\"cursor_expired\",\"work_id\":%q}", w.ID)
	if !strings.Contains(body2, want) {
		t.Fatalf("expected reset frame %q, got:\n%s", want, body2)
	}
	if strings.Contains(body2, "id: ") {
		t.Fatalf("reset stream must not replay events, got:\n%s", body2)
	}
	_ = body
}

func TestWorkEventsSSE404Isolation(t *testing.T) {
	s := openEventsTestStore(t)
	w := journalWork(t, s, "work:sse-own", workgraph.StateRunning)
	appendEvent(t, s, w.ID, "evt_iso", "work.created", `{}`)

	ts := newEventsTestServer(t, s)
	for _, missing := range []string{"work:nope", "work:other-tenant"} {
		resp, body := readSSE(t, ts, fmt.Sprintf("/v1/works/%s/events", missing), "")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET events for %q: status = %d, want 404", missing, resp.StatusCode)
		}
		if strings.Contains(body, "work:") || strings.Contains(body, "event:") {
			t.Fatalf("404 body leaks existence details for %q: %s", missing, body)
		}
	}
}

func TestWorkEventsSSERequiresBearer(t *testing.T) {
	s := openEventsTestStore(t)
	w := journalWork(t, s, "work:sse-auth", workgraph.StateRunning)

	// The wrapping server has AuthEnabled=true: the endpoint must sit
	// behind requireBearer like every other authenticated surface.
	authSrv := &api.Server{Store: s, AuthEnabled: true}
	mux := http.NewServeMux()
	api.WireWorkEventRoutes(mux, authSrv)
	ts2 := httptest.NewServer(mux)
	defer ts2.Close()

	resp, err := http.Get(ts2.URL + fmt.Sprintf("/v1/works/%s/events", w.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without bearer = %d, want 401", resp.StatusCode)
	}
}

// TestWorkEventsRESTJournalListing — Task 8 cross-repo mirror loop: the
// AVC conversation worker polls the journal as REST JSON
// (GET /v1/works/{id}/events?after=&limit=) in the WorksEventEnvelope
// wire shape. The SSE stream (no query cursors) must be unaffected.
func TestWorkEventsRESTJournalListing(t *testing.T) {
	s := openEventsTestStore(t)
	w := journalWork(t, s, "work:rest-journal", workgraph.StateRunning)
	first := appendEvent(t, s, w.ID, "evt_r1", "work.created", `{"state":"CREATED"}`)
	second := appendEvent(t, s, w.ID, "evt_r2", "work.state.changed", `{"state":"RUNNING"}`)

	ts := newEventsTestServer(t, s)

	// Full listing from cursor 0, camelCase envelope shape.
	resp, err := http.Get(ts.URL + fmt.Sprintf("/v1/works/%s/events?after=0&limit=10", w.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got struct {
		WorkID string `json:"work_id"`
		Events []struct {
			ID         string          `json:"id"`
			WorkID     string          `json:"workId"`
			Type       string          `json:"type"`
			ObservedAt string          `json:"observedAt"`
			Sequence   int64           `json:"sequence"`
			Data       json.RawMessage `json:"data"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if got.WorkID != w.ID {
		t.Fatalf("work_id = %q, want %q", got.WorkID, w.ID)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(got.Events))
	}
	if got.Events[0].ID != "evt_r1" || got.Events[1].ID != "evt_r2" {
		t.Fatalf("event order wrong: %v", got.Events)
	}
	if got.Events[1].WorkID != w.ID || got.Events[1].Type != "work.state.changed" || got.Events[1].Sequence != second.Sequence {
		t.Fatalf("envelope fields wrong: %+v", got.Events[1])
	}
	if _, err := time.Parse(time.RFC3339Nano, got.Events[0].ObservedAt); err != nil {
		t.Fatalf("observedAt not RFC3339Nano: %q (%v)", got.Events[0].ObservedAt, err)
	}
	if string(got.Events[1].Data) != `{"state":"RUNNING"}` {
		t.Fatalf("data passthrough = %s", got.Events[1].Data)
	}

	// Cursor semantics: after=first.Sequence returns only later events.
	resp2, err := http.Get(ts.URL + fmt.Sprintf("/v1/works/%s/events?after=%d", w.ID, first.Sequence))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var only struct {
		Events []struct {
			ID       string `json:"id"`
			Sequence int64  `json:"sequence"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&only); err != nil {
		t.Fatalf("decode cursor listing: %v", err)
	}
	if len(only.Events) != 1 || only.Events[0].ID != "evt_r2" {
		t.Fatalf("after=%d must return only later events, got %+v", first.Sequence, only.Events)
	}

	// Limit clamp: limit=1 returns the single oldest eligible row.
	resp3, err := http.Get(ts.URL + fmt.Sprintf("/v1/works/%s/events?after=0&limit=1", w.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	var clamped struct {
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&clamped); err != nil {
		t.Fatalf("decode clamped listing: %v", err)
	}
	if len(clamped.Events) != 1 || clamped.Events[0].ID != "evt_r1" {
		t.Fatalf("limit=1 must clamp to one row, got %+v", clamped.Events)
	}

	// Validation and isolation.
	for _, bad := range []string{"after=-1", "after=abc", "limit=0", "limit=-5"} {
		resp4, err := http.Get(ts.URL + fmt.Sprintf("/v1/works/%s/events?%s", w.ID, bad))
		if err != nil {
			t.Fatal(err)
		}
		resp4.Body.Close()
		if resp4.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET events?%s = %d, want 400", bad, resp4.StatusCode)
		}
	}
	resp5, err := http.Get(ts.URL + "/v1/works/work:nope/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	resp5.Body.Close()
	if resp5.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown work listing = %d, want 404", resp5.StatusCode)
	}
}
