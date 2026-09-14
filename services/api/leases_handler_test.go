package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// grantLeaseForHandlerTest creates a queued work item + active lease via the
// store and returns the lease id. The handler tests run against a dev-mode
// server (no bearer auth), so gateLeaseOwner passes through.
func grantLeaseForHandlerTest(t *testing.T, st store.Store) string {
	t.Helper()
	ctx := context.Background()
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "echo a"}}},
		State:     workgraph.StateCreated,
	}
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	l, _, err := st.GrantLease(ctx, w.ID, "a", "wrkr_1", 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return l.ID
}

func postLeaseVerb(t *testing.T, ts *httptest.Server, leaseID, action, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(
		ts.URL+"/v1/leases/"+leaseID+"/"+action,
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func errCodeOf(t *testing.T, body string) string {
	t.Helper()
	var m struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("unmarshal error body %q: %v", body, err)
	}
	return m.Error
}

// TestReleaseLease_TerminalLeaseReturns409Not500 pins that releasing a lease
// that is already terminal yields 409 lease_not_active (a real conflict),
// not a 500 release_failed. Before the fix the default branch mapped every
// non-404 error — including ErrInvalidTransition — to 409 with the wrong
// reason code, and a genuine store error would have been mislabeled as 409.
func TestReleaseLease_TerminalLeaseReturns409Not500(t *testing.T) {
	_, ts, st := newTestServer(t)
	leaseID := grantLeaseForHandlerTest(t, st)

	if code, _ := postLeaseVerb(t, ts, leaseID, "release", `{"reason":"done"}`); code != http.StatusOK {
		t.Fatalf("first release: want 200, got %d", code)
	}
	code, body := postLeaseVerb(t, ts, leaseID, "release", `{"reason":"again"}`)
	if code != http.StatusConflict {
		t.Fatalf("second release: want 409, got %d (%s)", code, body)
	}
	if got := errCodeOf(t, body); got != "lease_not_active" {
		t.Fatalf("second release error code: got %q, want \"lease_not_active\"", got)
	}
}

// TestRevokeLease_TerminalLeaseReturns409Not500 mirrors the release test for
// the revoke verb: revoking an already-terminal lease is a 409 conflict, not
// a 500 revoke_failed.
func TestRevokeLease_TerminalLeaseReturns409Not500(t *testing.T) {
	_, ts, st := newTestServer(t)
	leaseID := grantLeaseForHandlerTest(t, st)

	if code, _ := postLeaseVerb(t, ts, leaseID, "revoke", `{"reason":"cancel"}`); code != http.StatusOK {
		t.Fatalf("first revoke: want 200, got %d", code)
	}
	code, body := postLeaseVerb(t, ts, leaseID, "revoke", `{"reason":"again"}`)
	if code != http.StatusConflict {
		t.Fatalf("second revoke: want 409, got %d (%s)", code, body)
	}
	if got := errCodeOf(t, body); got != "lease_not_active" {
		t.Fatalf("second revoke error code: got %q, want \"lease_not_active\"", got)
	}
}

// TestHeartbeat_MalformedJSONReturns400 pins that a syntactically invalid
// heartbeat body is rejected with 400 invalid_json rather than silently
// falling back to TTLSeconds=0 (which would silently change renewal TTL).
func TestHeartbeat_MalformedJSONReturns400(t *testing.T) {
	_, ts, st := newTestServer(t)
	leaseID := grantLeaseForHandlerTest(t, st)

	code, body := postLeaseVerb(t, ts, leaseID, "heartbeat", `{not json`)
	if code != http.StatusBadRequest {
		t.Fatalf("malformed heartbeat: want 400, got %d (%s)", code, body)
	}
	if got := errCodeOf(t, body); got != "invalid_json" {
		t.Fatalf("malformed heartbeat error code: got %q, want \"invalid_json\"", got)
	}
}

// TestHeartbeat_EmptyBodyAllowed confirms the optional-body contract: an
// empty body renews with the default TTL and returns 200.
func TestHeartbeat_EmptyBodyAllowed(t *testing.T) {
	_, ts, st := newTestServer(t)
	leaseID := grantLeaseForHandlerTest(t, st)

	if code, _ := postLeaseVerb(t, ts, leaseID, "heartbeat", ""); code != http.StatusOK {
		t.Fatalf("empty heartbeat: want 200, got %d", code)
	}
}
