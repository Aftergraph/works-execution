// Package audit_test exercises the CloudEvents v1.0 audit emitter
// end-to-end: NewEvent shape, persistence into work_audit_events via
// the store's UpdateState path, and the HTTP read API.
package audit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/audit"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func openStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleWork() *workgraph.Work {
	return &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "echo a"}},
		},
		CorrelationID: "corr-test-001",
	}
}

// TestNewEvent_RequiredFields asserts that the v1.0 mandatory context
// attributes (id, source, specversion, type, time) are populated.
func TestNewEvent_RequiredFields(t *testing.T) {
	ev := audit.NewEvent(audit.TypeWorkStateChanged, "wrk_abc")
	if ev.ID == "" {
		t.Fatal("ID must be set")
	}
	if !strings.HasPrefix(ev.ID, "evt_") {
		t.Errorf("ID should be evt_-prefixed, got %q", ev.ID)
	}
	if ev.Source != audit.Source {
		t.Errorf("source: got %q, want %q", ev.Source, audit.Source)
	}
	if ev.SpecVersion != audit.SpecVersion {
		t.Errorf("specversion: got %q, want %q", ev.SpecVersion, audit.SpecVersion)
	}
	if ev.SpecVersion != "1.0" {
		t.Errorf("specversion: got %q, want 1.0", ev.SpecVersion)
	}
	if ev.Type != audit.TypeWorkStateChanged {
		t.Errorf("type: got %q, want %q", ev.Type, audit.TypeWorkStateChanged)
	}
	if ev.Subject != "wrk_abc" {
		t.Errorf("subject: got %q, want wrk_abc", ev.Subject)
	}
	if ev.Time.IsZero() {
		t.Error("time must be set")
	}
	if time.Since(ev.Time) > 5*time.Second {
		t.Errorf("time should be ~now, got %v", ev.Time)
	}
}

// TestStore_EmitsEventOnStateChange asserts that calling UpdateState on
// the store produces one work.state_changed CloudEvent that the read
// API can return.
func TestStore_EmitsEventOnStateChange(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}

	// CreateWork should also have emitted a work.created event.
	created, err := s.ListAuditEvents(ctx, audit.ListFilter{WorkID: w.ID, Type: audit.TypeWorkCreated})
	if err != nil {
		t.Fatalf("list created: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("work.created count: got %d, want 1", len(created))
	}
	if created[0].SpecVersion != "1.0" {
		t.Errorf("specversion: got %q, want 1.0", created[0].SpecVersion)
	}

	// Now transition to QUEUED and assert one state_changed event.
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatalf("update state: %v", err)
	}
	changes, err := s.ListAuditEvents(ctx, audit.ListFilter{WorkID: w.ID, Type: audit.TypeWorkStateChanged})
	if err != nil {
		t.Fatalf("list state_changed: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("state_changed count: got %d, want 1", len(changes))
	}
	c := changes[0]
	if c.FromState != string(workgraph.StateCreated) {
		t.Errorf("from_state: got %q, want CREATED", c.FromState)
	}
	if c.ToState != string(workgraph.StateQueued) {
		t.Errorf("to_state: got %q, want QUEUED", c.ToState)
	}
	if c.WorkID != w.ID {
		t.Errorf("work_id: got %q, want %q", c.WorkID, w.ID)
	}
	if c.Subject != w.ID {
		t.Errorf("subject: got %q, want %q", c.Subject, w.ID)
	}
	// data must be valid JSON
	var payload map[string]any
	if err := json.Unmarshal(c.Data, &payload); err != nil {
		t.Fatalf("data should be valid JSON: %v (raw=%s)", err, string(c.Data))
	}
	if payload["work_id"] != w.ID {
		t.Errorf("data.work_id: got %v", payload["work_id"])
	}
	if payload["from_state"] != "CREATED" || payload["to_state"] != "QUEUED" {
		t.Errorf("data.from_state/to_state: got %v/%v", payload["from_state"], payload["to_state"])
	}
}

// TestListAuditEvents_TimeAndTypeFilter exercises the query filter —
// only events within [since, until] and of the requested type are
// returned, in deterministic ASC order.
func TestListAuditEvents_TimeAndTypeFilter(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	w1 := sampleWork()
	w2 := sampleWork()
	if err := s.CreateWork(ctx, w1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWork(ctx, w2); err != nil {
		t.Fatal(err)
	}
	// Transition w1 through one extra step so we have a state_changed.
	if _, err := s.UpdateState(ctx, w1.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().UTC().Add(-time.Hour)
	all, err := s.ListAuditEvents(ctx, audit.ListFilter{Since: cutoff, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 events, got %d", len(all))
	}
	// Order must be ASC by occurred_at.
	for i := 1; i < len(all); i++ {
		if all[i].OccurredAt.Before(all[i-1].OccurredAt) {
			t.Fatalf("events not in ASC order at %d: %v before %v", i, all[i].OccurredAt, all[i-1].OccurredAt)
		}
	}

	// Filter by type=created.
	created, err := s.ListAuditEvents(ctx, audit.ListFilter{Since: cutoff, Type: audit.TypeWorkCreated, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Errorf("created count: got %d, want 2", len(created))
	}
	for _, c := range created {
		if c.Type != audit.TypeWorkCreated {
			t.Errorf("type filter leaked: got %s", c.Type)
		}
	}

	// Filter by work_id.
	w1Events, err := s.ListAuditEvents(ctx, audit.ListFilter{Since: cutoff, WorkID: w1.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(w1Events) != 2 {
		t.Errorf("w1 events: got %d, want 2", len(w1Events))
	}

	// until cutoff in the future excludes everything.
	future := time.Now().UTC().Add(time.Hour)
	none, err := s.ListAuditEvents(ctx, audit.ListFilter{Until: future.Add(-2 * time.Hour), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("until-past should return 0, got %d", len(none))
	}
}

// TestAuditEventsHTTPEndpoint mounts the full HTTP router and verifies
// the /v1/audit-events endpoint serializes CloudEvents correctly.
func TestAuditEventsHTTPEndpoint(t *testing.T) {
	s := openStore(t)
	srv := &api.Server{Store: s}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	w := sampleWork()
	if err := s.CreateWork(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(context.Background(), w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}

	// 1. Plain GET — returns at least the 2 events for w (created + state_changed).
	resp, err := http.Get(ts.URL + "/v1/audit-events?work_id=" + w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body struct {
		Events []audit.AuditEvent `json:"events"`
		Count  int                `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 || len(body.Events) != 2 {
		t.Fatalf("count: got %d (len=%d), want 2", body.Count, len(body.Events))
	}
	// Confirm the wire shape includes the CloudEvents v1.0 attributes.
	var first map[string]any
	raw, _ := json.Marshal(body.Events[0])
	_ = json.Unmarshal(raw, &first)
	for _, attr := range []string{"id", "source", "specversion", "type", "time"} {
		if _, ok := first[attr]; !ok {
			t.Errorf("CloudEvents v1.0 attribute %q missing on wire", attr)
		}
	}
	if first["specversion"] != "1.0" {
		t.Errorf("specversion on wire: got %v, want 1.0", first["specversion"])
	}

	// 2. Bad method.
	resp2, err := http.Post(ts.URL+"/v1/audit-events", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST should be 405, got %d", resp2.StatusCode)
	}

	// 3. Bad limit.
	resp3, err := http.Get(ts.URL + "/v1/audit-events?limit=notanumber")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("bad limit should be 400, got %d", resp3.StatusCode)
	}
}
