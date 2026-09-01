package api_test

// Task 2 (docs/superpowers/plans/2026-09-01-works-conversation-v1.md):
// platform-bridge-bound resume endpoint — POST /v1/works/{id}/resume.
//
// Contract under test:
//   - bridge header X-Works-Platform-Bridge must equal the wired secret;
//     an unwired (empty) secret fails closed with 503
//   - wrong/missing bridge secret -> 401
//   - missing required body fields -> 400
//   - non-WAITING_HUMAN work -> 409
//   - checkpoint_hash not matching the persisted handoff payload hash -> 409
//   - same idempotency_key + same payload -> same successful result (replay)
//   - same idempotency_key + changed payload -> 409
//   - valid bound request -> calls ResumeFromCheckpoint, returns RUNNING
//
// The handler depends on narrow local interfaces (WorkGetter, WorkResumer,
// HandoffRecordReader) rather than the concrete store, and persists
// receipts in its own work_resume_receipts table via a file-local
// migration helper (CREATE TABLE IF NOT EXISTS) so Task 1's store.go
// stays untouched.
import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

const testBridgeSecret = "bridge-secret-0123456789abcdef0123456789abcdef" // >= 32 bytes

func openResumeTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newResumeTestServer(t *testing.T, s *store.SQLiteStore, secret string) *httptest.Server {
	t.Helper()
	srv := &api.Server{Store: s, AuthEnabled: false}
	mux := http.NewServeMux()
	api.WireResumeRoutes(mux, srv, secret)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// resumeFixture creates a RUNNING mission work, suspends it to
// WAITING_HUMAN with a handoff, and returns the persisted payload hash.
func resumeFixture(t *testing.T, s *store.SQLiteStore, id string) (payloadHash string) {
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
	if _, err := s.SuspendWork(ctx, id, workgraph.StateWaitingHuman, &workgraph.Handoff{
		StateSnapshot: map[string]any{"node": "research"},
		Narrative:     "waiting on human approval",
	}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	h, _, err := s.LatestHandoff(ctx, id)
	if err != nil {
		t.Fatalf("latest handoff: %v", err)
	}
	rec, err := s.LatestHandoffRecord(ctx, id)
	if err != nil {
		t.Fatalf("latest handoff record: %v", err)
	}
	_ = h
	return rec.PayloadHash
}

func postResume(t *testing.T, ts *httptest.Server, workID, bridgeSecret, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/works/"+workID+"/resume", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bridgeSecret != "" {
		req.Header.Set("X-Works-Platform-Bridge", bridgeSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST resume: %v", err)
	}
	resp.Body.Close()
	return resp
}

func resumeBody(receipt, principal, tenant, hash, idem string) string {
	return fmt.Sprintf(`{"approval_receipt_id":%q,"principal_id":%q,"tenant_id":%q,"checkpoint_hash":%q,"idempotency_key":%q}`,
		receipt, principal, tenant, hash, idem)
}

const validResumeBodyFields = `"appr-1","prin-1","ten-1"`

func TestResumeRequiresBridgeSecret(t *testing.T) {
	s := openResumeTestStore(t)
	hash := resumeFixture(t, s, "work:res-bridge")
	ts := newResumeTestServer(t, s, testBridgeSecret)

	if resp := postResume(t, ts, "work:res-bridge", "", resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-1")); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing bridge header: status = %d, want 401", resp.StatusCode)
	}
	if resp := postResume(t, ts, "work:res-bridge", "wrong-secret-wrong-secret-wrong-secret!", resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-1")); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bridge secret: status = %d, want 401", resp.StatusCode)
	}
}

func TestResumeUnavailableWithoutWiredSecret(t *testing.T) {
	s := openResumeTestStore(t)
	ts := newResumeTestServer(t, s, "") // empty secret: route unavailable

	resp := postResume(t, ts, "work:whatever", testBridgeSecret, resumeBody("appr-1", "prin-1", "ten-1", "hash", "idem"))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unwired secret: status = %d, want 503", resp.StatusCode)
	}
}

func TestResumeRejectsMissingBodyFields(t *testing.T) {
	s := openResumeTestStore(t)
	ts := newResumeTestServer(t, s, testBridgeSecret)

	for _, body := range []string{
		`{"principal_id":"p","tenant_id":"t","checkpoint_hash":"h","idempotency_key":"k"}`,           // no receipt
		`{"approval_receipt_id":"r","tenant_id":"t","checkpoint_hash":"h","idempotency_key":"k"}`,    // no principal
		`{"approval_receipt_id":"r","principal_id":"p","checkpoint_hash":"h","idempotency_key":"k"}`, // no tenant
		`{"approval_receipt_id":"r","principal_id":"p","tenant_id":"t","idempotency_key":"k"}`,       // no hash
		`{"approval_receipt_id":"r","principal_id":"p","tenant_id":"t","checkpoint_hash":"h"}`,       // no idem key
		`not json`,
	} {
		resp := postResume(t, ts, "work:x", testBridgeSecret, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestResumeRejectsNonWaitingHumanWork(t *testing.T) {
	s := openResumeTestStore(t)
	journalWork(t, s, "work:res-running", workgraph.StateRunning)
	ts := newResumeTestServer(t, s, testBridgeSecret)

	resp := postResume(t, ts, "work:res-running", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", "any-hash", "idem-running"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("non-WAITING_HUMAN: status = %d, want 409", resp.StatusCode)
	}
}

func TestResumeRejectsWrongCheckpointHash(t *testing.T) {
	s := openResumeTestStore(t)
	hash := resumeFixture(t, s, "work:res-hash")
	ts := newResumeTestServer(t, s, testBridgeSecret)

	resp := postResume(t, ts, "work:res-hash", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", "deadbeef"+hash[8:], "idem-hash"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("wrong checkpoint hash: status = %d, want 409", resp.StatusCode)
	}

	// Sanity: the correct hash passes the binding and resumes the work.
	got, err := s.GetWork(context.Background(), "work:res-hash")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != workgraph.StateWaitingHuman {
		t.Fatalf("precondition broken: work already resumed by the 409 request (state=%s)", got.State)
	}
	ok := postResume(t, ts, "work:res-hash", testBridgeSecret, resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-hash-ok"))
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("valid hash rejected: status = %d", ok.StatusCode)
	}
	got, err = s.GetWork(context.Background(), "work:res-hash")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != workgraph.StateRunning {
		t.Fatalf("state after valid resume = %s, want RUNNING", got.State)
	}
}

func TestResumeIdempotentReplayAndConflict(t *testing.T) {
	s := openResumeTestStore(t)
	hash := resumeFixture(t, s, "work:res-idem")
	ts := newResumeTestServer(t, s, testBridgeSecret)

	first := postResume(t, ts, "work:res-idem", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-same"))
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first resume: status = %d", first.StatusCode)
	}

	// Same key + same payload after success: replay the stored receipt with
	// the same successful result (the work is already RUNNING — that must
	// not surface as 409).
	replay := postResume(t, ts, "work:res-idem", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-same"))
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("idempotent replay: status = %d, want 200", replay.StatusCode)
	}

	// Same key + changed payload: conflict.
	changed := postResume(t, ts, "work:res-idem", testBridgeSecret,
		resumeBody("appr-CHANGED", "prin-1", "ten-1", hash, "idem-same"))
	if changed.StatusCode != http.StatusConflict {
		t.Fatalf("same key changed payload: status = %d, want 409", changed.StatusCode)
	}
}

func TestResumeValidBoundRequestReturnsRunning(t *testing.T) {
	s := openResumeTestStore(t)
	hash := resumeFixture(t, s, "work:res-valid")
	ts := newResumeTestServer(t, s, testBridgeSecret)

	resp := postResume(t, ts, "work:res-valid", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-valid"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid resume: status = %d", resp.StatusCode)
	}
	w, err := s.GetWork(context.Background(), "work:res-valid")
	if err != nil {
		t.Fatal(err)
	}
	if w.State != workgraph.StateRunning {
		t.Fatalf("state = %s, want RUNNING", w.State)
	}

	// Receipt persisted, keyed by idempotency key with work + principal +
	// tenant + resulting state.
	var workID, principal, tenant, resultState string
	err = s.DB().QueryRow(`SELECT work_id, principal_id, tenant_id, resulting_state
        FROM work_resume_receipts WHERE idempotency_key = ?`, "idem-valid").
		Scan(&workID, &principal, &tenant, &resultState)
	if err != nil {
		t.Fatalf("receipt row missing: %v", err)
	}
	if workID != "work:res-valid" || principal != "prin-1" || tenant != "ten-1" || resultState != "RUNNING" {
		t.Fatalf("receipt = (%s,%s,%s,%s)", workID, principal, tenant, resultState)
	}
}

func TestResume404Isolation(t *testing.T) {
	s := openResumeTestStore(t)
	ts := newResumeTestServer(t, s, testBridgeSecret)

	resp := postResume(t, ts, "work:nope", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", "hash", "idem-404"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing work: status = %d, want 404", resp.StatusCode)
	}
}

func TestResumeSecretMinLength(t *testing.T) {
	s := openResumeTestStore(t)
	// A secret shorter than 32 bytes must fail closed: the route is
	// unavailable even though a value was wired.
	ts := newResumeTestServer(t, s, "short")
	resp := postResume(t, ts, "work:x", "short", resumeBody("a", "p", "t", "h", "k"))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("short secret: status = %d, want 503", resp.StatusCode)
	}
	if !strings.Contains("x", "") {
		t.Fatal("unreachable")
	}
}
