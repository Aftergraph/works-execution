// Package api_test: k-058 claim-path enforcement of the rab/1.0
// advertisement law (see claim_abi_gate.go for the law and its limits).
//
// Interlock pinned by these tests: the CLAIMING WORKER'S worker_id is
// resolved against the runner registry (worker_id == runner_id
// convention). No stored RAB => legacy pass; a stored RAB with
// RequiresControlToken() => the claim must present a non-empty
// X-RAB-Control-Token header, else 403 "control_token_required" with NO
// lease state transition. Token VALUE verification and token-to-runner
// identity binding are explicitly out of scope for this slice (see case
// (e): a token "belonging to" a second runner passes -- the advertisement
// law only demands presentation). The negotiate endpoint is unchanged and
// its existing tests (runner_abi_test.go) keep pinning it.
//
// The 5-line server/register/post helpers below are copied from
// runner_abi_test.go's rabTestServer pattern per the k-058 design; that
// file is not edited.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// gateTestServer wires the REAL router over a temp store (the
// rabTestServer pattern). AuthEnabled stays false (dev mode), so bearer
// auth is not what these tests exercise -- the RAB claim gate is.
func gateTestServer(t *testing.T) (*httptest.Server, store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "claim-gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := &api.Server{Store: s}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, s
}

// gateRegister posts a minimal schema-valid runner.Identity (rabRegister
// pattern) so the runner exists in the registry.
func gateRegister(t *testing.T, ts *httptest.Server, runnerID string) {
	t.Helper()
	body := `{"runner_id":"` + runnerID + `","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":["pool:gate-test"],"os":["linux"],"arch":["amd64"]}}`
	resp, err := http.Post(ts.URL+"/v1/runners/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s: status %d", runnerID, resp.StatusCode)
	}
}

// gatePostRAB publishes a rab/1.0 advertisement for runnerID.
func gatePostRAB(t *testing.T, ts *httptest.Server, runnerID, rabBody string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1/runners/"+runnerID+"/abi", "application/json", strings.NewReader(rabBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("post rab for %s: status %d body=%s", runnerID, resp.StatusCode, b)
	}
}

const (
	gateRABObserve = `{"abi":"rab/1.0","caps":["observe"]}`
	gateRABControl = `{"abi":"rab/1.0","caps":["control"],"control_token_required":true}`
)

// gateCreateWork creates and queues a single-node work ("a") and returns
// its id (the policy_test createWork shape).
func gateCreateWork(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	type createBody struct {
		workgraph.Work
		Queue bool `json:"queue"`
	}
	w := workgraph.Work{
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{
				"a": {ID: "a", Run: "echo a"},
			},
		},
	}
	b, err := json.Marshal(createBody{Work: w, Queue: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create work: status %d body=%s", resp.StatusCode, body)
	}
	var got workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.ID
}

// gateClaim is one lease claim (POST /v1/leases/grant). When token is nil
// the X-RAB-Control-Token header is absent entirely; when it points at a
// string the header is set verbatim (including the empty string, to pin
// that empty-but-present is denied too).
func gateClaim(t *testing.T, ts *httptest.Server, workerID, workID string, token *string) (*http.Response, map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"work_id":     workID,
		"node_id":     "a",
		"worker_id":   workerID,
		"ttl_seconds": 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/leases/grant", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != nil {
		req.Header.Set("X-RAB-Control-Token", *token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// gateActiveLeaseNodes returns the set of node ids with an ACTIVE lease
// for workID -- the store-side truth for "the lease state machine did
// (not) move".
func gateActiveLeaseNodes(t *testing.T, st store.Store, workID string) map[string]bool {
	t.Helper()
	active, err := st.ActiveLeasesByWorkID(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func strptr(s string) *string { return &s }

// Case (a): legacy interlock. A runner registered through the k-053
// surface with NO RAB posted claims exactly as before (201, ACTIVE
// lease); an unregistered worker id claims as before too. Pre-k-053
// runners and self-claiming workers are untouched by the gate.
func TestClaimGateLegacyNoRABClaimsUnaffected(t *testing.T) {
	ts, st := gateTestServer(t)
	gateRegister(t, ts, "wrkr_gate_legacy")

	w1 := gateCreateWork(t, ts)
	resp, out := gateClaim(t, ts, "wrkr_gate_legacy", w1, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("legacy claim (RAB-less runner) must succeed as before: got %d %v", resp.StatusCode, out)
	}
	if !gateActiveLeaseNodes(t, st, w1)["a"] {
		t.Fatal("legacy claim must have created an ACTIVE lease on node a")
	}

	w2 := gateCreateWork(t, ts)
	resp, out = gateClaim(t, ts, "wrkr_never_registered", w2, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claim by an unregistered worker must be legacy-pass: got %d %v", resp.StatusCode, out)
	}
}

// Case (b): observe-only RAB (no control cap) => claim proceeds with no
// header. The control-only law from packages/abi: passive tiers are
// token-free and the gate must not over-gate.
func TestClaimGateObserveOnlyNeedsNoToken(t *testing.T) {
	ts, st := gateTestServer(t)
	gateRegister(t, ts, "wrkr_gate_obs")
	gatePostRAB(t, ts, "wrkr_gate_obs", gateRABObserve)

	w := gateCreateWork(t, ts)
	resp, out := gateClaim(t, ts, "wrkr_gate_obs", w, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("observe-only RAB claim without token must succeed: got %d %v", resp.StatusCode, out)
	}
	if !gateActiveLeaseNodes(t, st, w)["a"] {
		t.Fatal("observe-only claim must have created an ACTIVE lease")
	}
}

// Case (c): control+required RAB, claim WITHOUT the header => 403
// control_token_required, and the lease is NOT transitioned. Asserting
// zero active leases (and the still-queued work state) after the denial
// is the deterministic proof the gate precedes the state change: had the
// check run after Store.GrantLease, the lease would exist and/or the
// work would be RUNNING.
func TestClaimGateControlRequiredMissingTokenDeniesBeforeTransition(t *testing.T) {
	ts, st := gateTestServer(t)
	gateRegister(t, ts, "wrkr_gate_ctl")
	gatePostRAB(t, ts, "wrkr_gate_ctl", gateRABControl)

	w := gateCreateWork(t, ts)
	before, err := st.GetWork(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}

	resp, out := gateClaim(t, ts, "wrkr_gate_ctl", w, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing token at claim: got %d %v, want 403", resp.StatusCode, out)
	}
	if code, _ := out["error"].(string); code != "control_token_required" {
		t.Fatalf("error code: got %v, want control_token_required", out["error"])
	}
	if active := gateActiveLeaseNodes(t, st, w); len(active) != 0 {
		t.Fatalf("denied claim must NOT transition lease state: active=%v", active)
	}
	after, err := st.GetWork(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State {
		t.Fatalf("denied claim must NOT move work state: %s -> %s", before.State, after.State)
	}

	// Empty-but-present header is missing per the law (non-empty
	// required). Same denial, same untouched state.
	resp, out = gateClaim(t, ts, "wrkr_gate_ctl", w, strptr(""))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("empty token header: got %d %v, want 403", resp.StatusCode, out)
	}
	if len(gateActiveLeaseNodes(t, st, w)) != 0 {
		t.Fatal("empty-token denial must NOT transition lease state")
	}
}

// Case (d): control+required RAB, claim WITH X-RAB-Control-Token (any
// non-empty value) => the claim proceeds exactly like a token-free
// runtime's.
func TestClaimGateControlRequiredWithTokenProceeds(t *testing.T) {
	ts, st := gateTestServer(t)
	gateRegister(t, ts, "wrkr_gate_tok")
	gatePostRAB(t, ts, "wrkr_gate_tok", gateRABControl)

	w := gateCreateWork(t, ts)
	resp, out := gateClaim(t, ts, "wrkr_gate_tok", w, strptr("whatever-the-authority-issued"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("presented token must clear the advertisement gate: got %d %v", resp.StatusCode, out)
	}
	if !gateActiveLeaseNodes(t, st, w)["a"] {
		t.Fatal("accepted claim must have created an ACTIVE lease")
	}
}

// Case (e): identity binding is NOT this slice. Pinning current
// behavior: runner A (control RAB) presents a token VALUE that "belongs
// to" runner B (another control-registered runner) => still 201. There is
// no issuing authority wired in here; presentation is the whole law.
// Token-to-runner binding belongs to the future per-action-authz slice.
func TestClaimGateTokenIdentityNotBoundYet(t *testing.T) {
	ts, st := gateTestServer(t)
	gateRegister(t, ts, "wrkr_gate_a")
	gateRegister(t, ts, "wrkr_gate_b")
	gatePostRAB(t, ts, "wrkr_gate_a", gateRABControl)
	gatePostRAB(t, ts, "wrkr_gate_b", gateRABControl)

	w := gateCreateWork(t, ts)
	resp, out := gateClaim(t, ts, "wrkr_gate_a", w, strptr("token-issued-to-wrkr_gate_b"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("cross-runner token value must still pass presentation-only law: got %d %v", resp.StatusCode, out)
	}
	if !gateActiveLeaseNodes(t, st, w)["a"] {
		t.Fatal("accepted claim must have created an ACTIVE lease")
	}
}
