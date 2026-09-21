package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

const dispatchV2PlatformToken = "works-platform-api-dispatch-v2-test-0123456789abcdef"
const dispatchV2BridgeSecret = "works-platform-bridge-dispatch-v2-test-0123456789"

func setupDispatchV2(t *testing.T) (*http.Server, string) {
	t.Helper()
	return nil, ""
}

func dispatchV2Fixture(leaseID string) string {
	return `{"schema":"dispatch.acceptance/2.0","organization_id":"org_11111111111111111111111111111111","tenant_id":"ten_22222222222222222222222222222222","principal_id":"prn_33333333333333333333333333333333","mission_id":"mis_example","authority_lease_id":"auth_44444444444444444444444444444444","worker_lease_id":"` + leaseID + `","admission_decision_id":"pdr_55555555555555555555555555555555","runtime_dispatch_id":"rdisp/1","attempt_id":"attempt/1","effect_id":"effect/1","idempotency_key":"idem/v2/1","budget_ref":"budget/1","budget_ceiling":100,"checkpoint_id":"checkpoint/1","evidence_root":"evidence/1","verification_subject":"git:Aftergraph/STEWARD-by-Aftergraph@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","causal_id":"causal/1"}`
}

func postDispatchV2(t *testing.T, base, workID, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v2/works/"+workID+"/accept", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dispatchV2PlatformToken)
	req.Header.Set("X-Works-Platform-Bridge", dispatchV2BridgeSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func setupDispatchV2Work(t *testing.T) (base, workID, leaseID string) {
	t.Helper()
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", dispatchV2BridgeSecret)
	srv, ts, st := newTestServer(t)
	srv.PlatformAPIToken = []byte(dispatchV2PlatformToken)
	w := &workgraph.Work{
		ID: workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Source: workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := st.CreateWork(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateState(t.Context(), w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := st.GrantLease(
		t.Context(),
		w.ID,
		"a",
		"wrkr_77777777777777777777777777777777",
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	return ts.URL, w.ID, lease.ID
}

func TestDispatchV2MaterializesResolvableExecutionContext(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	resp := postDispatchV2(t, base, workID, dispatchV2Fixture(leaseID))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want=200", resp.StatusCode)
	}
	var out struct {
		Schema           string                   `json:"schema"`
		WorkID           string                   `json:"work_id"`
		WorksExecutionID string                   `json:"works_execution_id"`
		ExecutionContext executioncontext.Context `json:"execution_context"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Schema != "dispatch.acceptance/2.0" || out.WorkID != workID || out.WorksExecutionID == "" {
		t.Fatalf("bad acceptance: %+v", out)
	}
	if out.ExecutionContext.WorkID != workID || out.ExecutionContext.WorkerLeaseID != leaseID ||
		out.ExecutionContext.AuthorityLeaseID != "auth_44444444444444444444444444444444" {
		t.Fatalf("bad execution context: %+v", out.ExecutionContext)
	}

	got, err := http.Get(base + "/v1/execution-contexts/" + out.ExecutionContext.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("materialized context GET status=%d want=200", got.StatusCode)
	}
	var persisted executioncontext.Context
	if err := json.NewDecoder(got.Body).Decode(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != out.ExecutionContext {
		t.Fatalf("persisted context mismatch: got=%+v want=%+v", persisted, out.ExecutionContext)
	}
}

func TestDispatchV2ReplayReturnsSameCorrelation(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	post := func() executioncontext.Context {
		resp := postDispatchV2(t, base, workID, dispatchV2Fixture(leaseID))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
		var out struct {
			ExecutionContext executioncontext.Context `json:"execution_context"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.ExecutionContext
	}
	first := post()
	second := post()
	if first.ID != second.ID || first.TraceID != second.TraceID {
		t.Fatalf("replay reminted correlation: first=%+v second=%+v", first, second)
	}
}

func TestDispatchV2RejectsLegacyAuthorityEpochAndClientCorrelation(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	for _, injected := range []string{
		`,"authority_epoch":7}`,
		`,"execution_context_id":"ctx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		`,"trace_id":"trc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
	} {
		body := strings.TrimSuffix(dispatchV2Fixture(leaseID), "}") + injected
		resp := postDispatchV2(t, base, workID, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("injection %s status=%d want=400", injected, resp.StatusCode)
		}
	}
}

func TestDispatchV2RejectsForeignWorkerLease(t *testing.T) {
	base1, _, leaseID := setupDispatchV2Work(t)
	_ = base1
	base2, workID2, _ := setupDispatchV2Work(t)
	resp := postDispatchV2(t, base2, workID2, dispatchV2Fixture(leaseID))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want=409", resp.StatusCode)
	}
}

func TestDispatchV2CausalMismatchFailsClosed(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	first := postDispatchV2(t, base, workID, dispatchV2Fixture(leaseID))
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status=%d", first.StatusCode)
	}
	conflict := strings.Replace(dispatchV2Fixture(leaseID), `"causal_id":"causal/1"`, `"causal_id":"causal/other"`, 1)
	resp := postDispatchV2(t, base, workID, conflict)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want=409", resp.StatusCode)
	}
}
