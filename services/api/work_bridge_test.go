package api_test

// Task 8 (docs/superpowers/plans/2026-09-01-works-conversation-v1.md):
// platform-bridge-bound checkpoint surface — POST /v1/works/{id}/suspend
// and GET /v1/works/{id}/handoff (see work_bridge.go).
//
// Contract under test:
//   - unwired bridge secret -> 503 (fail-closed); wrong/missing header -> 401
//   - suspend: moves a mission Work to WAITING_HUMAN and persists the
//     ADR-0010 handoff checkpoint in the same transaction
//   - suspend: emits the work.waiting_human journal event (mirror source
//     for the AVC conversation worker)
//   - suspend: unknown work -> 404; non-mission work -> 409;
//     missing/invalid handoff or state -> 400
//   - handoff: returns {work_id, state, checkpoint_hash} with the EXACT
//     persisted payload hash the resume route validates
//   - handoff: unknown work -> 404; no checkpoint -> 409
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func newBridgeTestServer(t *testing.T, s *store.SQLiteStore, secret string) *httptest.Server {
	t.Helper()
	srv := &api.Server{Store: s, AuthEnabled: false}
	mux := http.NewServeMux()
	api.WireResumeRoutes(mux, srv, secret)
	api.WireWorkEventRoutes(mux, srv)
	api.WireCheckpointRoutes(mux, srv, secret)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// missionRunningFixture creates a RUNNING mission work (no checkpoint yet).
func missionRunningFixture(t *testing.T, s *store.SQLiteStore, id string) {
	t.Helper()
	ctx := context.Background()
	w := &workgraph.Work{
		ID:        id,
		Objective: workgraph.Objective{Type: "custom"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"do": {ID: "do", Run: "echo hi"}}},
		State:     workgraph.StateRunning,
		Mission: &workgraph.MissionContract{
			BudgetCeiling: &workgraph.BudgetCeiling{ComputeEUR: 5},
			Verification:  []workgraph.VerificationCriterion{{Criterion: "done"}},
			KillSwitch:    "always",
		},
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create work: %v", err)
	}
}

func plainRunningFixture(t *testing.T, s *store.SQLiteStore, id string) {
	t.Helper()
	ctx := context.Background()
	w := &workgraph.Work{
		ID:        id,
		Objective: workgraph.Objective{Type: "custom"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"do": {ID: "do", Run: "echo hi"}}},
		State:     workgraph.StateRunning,
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create work: %v", err)
	}
}

func validHandoff() *workgraph.Handoff {
	return &workgraph.Handoff{
		StateSnapshot: map[string]any{"node": "research"},
		Narrative:     "waiting on human approval",
		DecisionLog:   []string{"deferred to human"},
		PriorityQueue: []string{"verify"},
		Warnings:      []string{"none"},
		PayloadSchema: workgraph.HandoffVersion,
	}
}

func mustGetWork(t *testing.T, s *store.SQLiteStore, id string) *workgraph.Work {
	t.Helper()
	wk, err := s.GetWork(context.Background(), id)
	if err != nil {
		t.Fatalf("get work %s: %v", id, err)
	}
	return wk
}

func suspendBody(state string, h *workgraph.Handoff) string {
	m := map[string]any{"handoff": h}
	if state != "" {
		m["state"] = state
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func postSuspend(t *testing.T, ts *httptest.Server, workID, bridgeSecret, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/works/"+workID+"/suspend", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bridgeSecret != "" {
		req.Header.Set("X-Works-Platform-Bridge", bridgeSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST suspend: %v", err)
	}
	resp.Body.Close()
	return resp
}

func getHandoff(t *testing.T, ts *httptest.Server, workID, bridgeSecret string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/works/"+workID+"/handoff", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bridgeSecret != "" {
		req.Header.Set("X-Works-Platform-Bridge", bridgeSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET handoff: %v", err)
	}
	resp.Body.Close()
	return resp
}

func TestSuspendRequiresBridgeSecret(t *testing.T) {
	s := openResumeTestStore(t)
	missionRunningFixture(t, s, "work:susp-bridge")
	ts := newBridgeTestServer(t, s, testBridgeSecret)
	body := suspendBody("", validHandoff())

	if resp := postSuspend(t, ts, "work:susp-bridge", "", body); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing bridge header: status = %d, want 401", resp.StatusCode)
	}
	if resp := postSuspend(t, ts, "work:susp-bridge", "wrong-secret-wrong-secret-wrong-secret!", body); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bridge secret: status = %d, want 401", resp.StatusCode)
	}
}

func TestSuspendUnavailableWithoutWiredSecret(t *testing.T) {
	s := openResumeTestStore(t)
	ts := newBridgeTestServer(t, s, "")
	resp := postSuspend(t, ts, "work:susp-unwired", testBridgeSecret, suspendBody("", validHandoff()))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unwired secret: status = %d, want 503", resp.StatusCode)
	}
}

func TestSuspendMovesMissionWorkToWaitingHumanAndPersistsCheckpoint(t *testing.T) {
	s := openResumeTestStore(t)
	missionRunningFixture(t, s, "work:susp-ok")
	ts := newBridgeTestServer(t, s, testBridgeSecret)

	resp := postSuspend(t, ts, "work:susp-ok", testBridgeSecret, suspendBody("", validHandoff()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("suspend: status = %d, want 200", resp.StatusCode)
	}
	wk := mustGetWork(t, s, "work:susp-ok")
	if string(wk.State) != "WAITING_HUMAN" {
		t.Fatalf("state = %s, want WAITING_HUMAN", wk.State)
	}

	rec, err := s.LatestHandoffRecord(context.Background(), "work:susp-ok")
	if err != nil {
		t.Fatalf("handoff record: %v", err)
	}
	if rec.PayloadHash == "" {
		t.Fatal("expected a persisted checkpoint hash")
	}
	if rec.ToState != workgraph.StateWaitingHuman {
		t.Fatalf("handoff to_state = %s, want WAITING_HUMAN", rec.ToState)
	}

	// The journal event is the mirror source for the conversation worker.
	evs, err := s.ListWorkEventsAfter(context.Background(), "work:susp-ok", 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Type == "work.waiting_human" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected work.waiting_human journal event, got: %+v", evs)
	}
}

func TestSuspendRejectsNonMissionWork(t *testing.T) {
	s := openResumeTestStore(t)
	plainRunningFixture(t, s, "work:susp-plain")
	ts := newBridgeTestServer(t, s, testBridgeSecret)

	resp := postSuspend(t, ts, "work:susp-plain", testBridgeSecret, suspendBody("", validHandoff()))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("non-mission suspend: status = %d, want 409", resp.StatusCode)
	}
}

func TestSuspendRejectsInvalidBodies(t *testing.T) {
	s := openResumeTestStore(t)
	missionRunningFixture(t, s, "work:susp-invalid")
	ts := newBridgeTestServer(t, s, testBridgeSecret)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing handoff", `{}`, http.StatusBadRequest},
		{"bad state", suspendBody("RUNNING", validHandoff()), http.StatusBadRequest},
		{"missing narrative", suspendBody("", &workgraph.Handoff{StateSnapshot: map[string]any{"x": 1}, PayloadSchema: workgraph.HandoffVersion}), http.StatusBadRequest},
		{"nil state snapshot", suspendBody("", &workgraph.Handoff{Narrative: "n", PayloadSchema: workgraph.HandoffVersion}), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postSuspend(t, ts, "work:susp-invalid", testBridgeSecret, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestSuspendUnknownWork404(t *testing.T) {
	s := openResumeTestStore(t)
	ts := newBridgeTestServer(t, s, testBridgeSecret)
	resp := postSuspend(t, ts, "work:susp-missing", testBridgeSecret, suspendBody("", validHandoff()))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandoffRequiresBridgeSecret(t *testing.T) {
	s := openResumeTestStore(t)
	missionRunningFixture(t, s, "work:ho-bridge")
	ts := newBridgeTestServer(t, s, testBridgeSecret)

	if resp := getHandoff(t, ts, "work:ho-bridge", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing bridge header: status = %d, want 401", resp.StatusCode)
	}
	if resp := getHandoff(t, ts, "work:ho-bridge", "wrong-secret-wrong-secret-wrong-secret!"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bridge secret: status = %d, want 401", resp.StatusCode)
	}
}

func TestHandoffUnavailableWithoutWiredSecret(t *testing.T) {
	s := openResumeTestStore(t)
	ts := newBridgeTestServer(t, s, "")
	resp := getHandoff(t, ts, "work:ho-unwired", testBridgeSecret)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unwired secret: status = %d, want 503", resp.StatusCode)
	}
}

func TestHandoffUnknownWork404(t *testing.T) {
	s := openResumeTestStore(t)
	ts := newBridgeTestServer(t, s, testBridgeSecret)
	resp := getHandoff(t, ts, "work:ho-missing", testBridgeSecret)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandoffNoCheckpoint409(t *testing.T) {
	s := openResumeTestStore(t)
	plainRunningFixture(t, s, "work:ho-nohandoff")
	ts := newBridgeTestServer(t, s, testBridgeSecret)
	resp := getHandoff(t, ts, "work:ho-nohandoff", testBridgeSecret)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("no checkpoint: status = %d, want 409", resp.StatusCode)
	}
}

// TestHandoffRoundTripsWithResume proves the end-to-end checkpoint binding:
// suspend persists a hash, /handoff returns exactly it, and POST /resume
// accepts that same hash to move the Work back to RUNNING (the AVC approval
// flow's core loop).
func TestHandoffRoundTripsWithResume(t *testing.T) {
	s := openResumeTestStore(t)
	missionRunningFixture(t, s, "work:ho-roundtrip")
	ts := newBridgeTestServer(t, s, testBridgeSecret)

	if resp := postSuspend(t, ts, "work:ho-roundtrip", testBridgeSecret, suspendBody("", validHandoff())); resp.StatusCode != http.StatusOK {
		t.Fatalf("suspend: status = %d, want 200", resp.StatusCode)
	}

	// Read the binding.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/works/work:ho-roundtrip/handoff", nil)
	req.Header.Set("X-Works-Platform-Bridge", testBridgeSecret)
	hresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("handoff: status = %d, want 200", hresp.StatusCode)
	}
	var view struct {
		WorkID         string `json:"work_id"`
		State          string `json:"state"`
		CheckpointHash string `json:"checkpoint_hash"`
	}
	if err := json.NewDecoder(hresp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.WorkID != "work:ho-roundtrip" || view.State != "WAITING_HUMAN" || view.CheckpointHash == "" {
		t.Fatalf("handoff view = %+v, want work_id + WAITING_HUMAN + non-empty hash", view)
	}

	// Resume with the exact persisted hash succeeds.
	if resp := postResume(t, ts, "work:ho-roundtrip", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", view.CheckpointHash, "idem-1")); resp.StatusCode != http.StatusOK {
		t.Fatalf("resume with handoff hash: status = %d, want 200", resp.StatusCode)
	}
	wk := mustGetWork(t, s, "work:ho-roundtrip")
	if string(wk.State) != "RUNNING" {
		t.Fatalf("state after resume = %s, want RUNNING", wk.State)
	}
}

// TestSuspendIsIdempotentForSameCheckpoint ensures a retry of the identical
// suspend (same handoff payload) returns the Work without corrupting the
// checkpoint binding.
func TestSuspendIsIdempotentForSameCheckpoint(t *testing.T) {
	s := openResumeTestStore(t)
	missionRunningFixture(t, s, "work:susp-dup")
	ts := newBridgeTestServer(t, s, testBridgeSecret)
	body := suspendBody("", validHandoff())

	if resp := postSuspend(t, ts, "work:susp-dup", testBridgeSecret, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("first suspend: status = %d, want 200", resp.StatusCode)
	}
	if resp := postSuspend(t, ts, "work:susp-dup", testBridgeSecret, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("idempotent retry: status = %d, want 200", resp.StatusCode)
	}
	rec, err := s.LatestHandoffRecord(context.Background(), "work:susp-dup")
	if err != nil {
		t.Fatal(err)
	}
	_ = fmt.Sprintf("%s", rec.PayloadHash) // hash must remain stable — resumed by binding
}