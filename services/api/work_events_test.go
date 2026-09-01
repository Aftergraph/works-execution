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
