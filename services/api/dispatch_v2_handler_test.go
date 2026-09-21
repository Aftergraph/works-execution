package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

const dispatchV2PlatformToken = "works-platform-api-dispatch-v2-test-0123456789abcdef"
const dispatchV2BridgeSecret = "works-platform-bridge-dispatch-v2-test-0123456789"
const dispatchV2Subject = "git:Aftergraph/STEWARD-by-Aftergraph@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func dispatchV2Fixture(leaseID string) string {
	return `{"schema":"dispatch.acceptance/2.0","organization_id":"org_11111111111111111111111111111111","tenant_id":"ten_22222222222222222222222222222222","principal_id":"prn_33333333333333333333333333333333","mission_id":"mis_example","authority_lease_id":"auth_44444444444444444444444444444444","worker_lease_id":"` + leaseID + `","admission_decision_id":"pdr_55555555555555555555555555555555","runtime_dispatch_id":"rdisp/1","attempt_id":"attempt/1","effect_id":"effect/1","idempotency_key":"idem/v2/1","budget_ref":"budget/1","budget_ceiling":100,"checkpoint_id":"checkpoint/1","evidence_root":"evidence/1","causal_id":"causal/1"}`
}

func postDispatchV2(t *testing.T, base, workID, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v2/works/"+workID+"/accept", strings.NewReader(body))
	if err != nil { t.Fatal(err) }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dispatchV2PlatformToken)
	req.Header.Set("X-Works-Platform-Bridge", dispatchV2BridgeSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatal(err) }
	return resp
}

func bindSubjectV2(t *testing.T, base, workID, executionID, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(
		http.MethodPost,
		base+"/v2/works/"+workID+"/acceptances/"+url.PathEscape(executionID)+"/verification-subject",
		strings.NewReader(body),
	)
	if err != nil { t.Fatal(err) }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dispatchV2PlatformToken)
	req.Header.Set("X-Works-Platform-Bridge", dispatchV2BridgeSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatal(err) }
	return resp
}

func subjectBody(subject string) string {
	return `{"schema":"dispatch.verification-subject/1.0","attempt_id":"attempt/1","effect_id":"effect/1","causal_id":"causal/1","subject":"` + subject + `"}`
}

func setupDispatchV2Work(t *testing.T) (base, workID, leaseID string) {
	t.Helper()
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", dispatchV2BridgeSecret)
	srv, ts, st := newTestServer(t)
	srv.PlatformAPIToken = []byte(dispatchV2PlatformToken)
	ctx := context.Background()
	w := &workgraph.Work{
		ID: workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Source: workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := st.CreateWork(ctx, w); err != nil { t.Fatal(err) }
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil { t.Fatal(err) }
	lease, _, err := st.GrantLease(ctx, w.ID, "a", "wrkr_77777777777777777777777777777777", time.Minute)
	if err != nil { t.Fatal(err) }
	return ts.URL, w.ID, lease.ID
}

func acceptV2(t *testing.T, base, workID, leaseID string) (string, executioncontext.Context) {
	t.Helper()
	resp := postDispatchV2(t, base, workID, dispatchV2Fixture(leaseID))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { t.Fatalf("status=%d want=200", resp.StatusCode) }
	var out struct {
		WorksExecutionID string                   `json:"works_execution_id"`
		ExecutionContext executioncontext.Context `json:"execution_context"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { t.Fatal(err) }
	return out.WorksExecutionID, out.ExecutionContext
}

func TestDispatchV2MaterializesResolvableExecutionContext(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	executionID, ec := acceptV2(t, base, workID, leaseID)
	if executionID == "" || ec.WorkID != workID || ec.WorkerLeaseID != leaseID ||
		ec.AuthorityLeaseID != "auth_44444444444444444444444444444444" {
		t.Fatalf("bad contextual acceptance: execution=%q context=%+v", executionID, ec)
	}

	got, err := http.Get(base + "/v1/execution-contexts/" + ec.ID)
	if err != nil { t.Fatal(err) }
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK { t.Fatalf("context GET status=%d want=200", got.StatusCode) }
	var persisted executioncontext.Context
	if err := json.NewDecoder(got.Body).Decode(&persisted); err != nil { t.Fatal(err) }
	if persisted != ec { t.Fatalf("persisted context mismatch: got=%+v want=%+v", persisted, ec) }
}

func TestDispatchV2InitialAcceptanceRejectsFutureVerificationSubject(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	body := strings.TrimSuffix(dispatchV2Fixture(leaseID), "}") + `,"verification_subject":"` + dispatchV2Subject + `"}`
	resp := postDispatchV2(t, base, workID, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want=400", resp.StatusCode)
	}
}

func TestDispatchV2SubjectBindingIsExactOnceAndIdempotent(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	executionID, _ := acceptV2(t, base, workID, leaseID)

	first := bindSubjectV2(t, base, workID, executionID, subjectBody(dispatchV2Subject))
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK { t.Fatalf("first bind status=%d", first.StatusCode) }
	var firstBody map[string]any
	if err := json.NewDecoder(first.Body).Decode(&firstBody); err != nil { t.Fatal(err) }
	if firstBody["subject"] != dispatchV2Subject { t.Fatalf("subject=%v", firstBody["subject"]) }

	second := bindSubjectV2(t, base, workID, executionID, subjectBody(dispatchV2Subject))
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK { t.Fatalf("same-subject replay status=%d", second.StatusCode) }
	var secondBody map[string]any
	if err := json.NewDecoder(second.Body).Decode(&secondBody); err != nil { t.Fatal(err) }
	if secondBody["bound_at"] != firstBody["bound_at"] {
		t.Fatalf("idempotent replay changed durable binding time: %v != %v", secondBody["bound_at"], firstBody["bound_at"])
	}

	other := "git:Aftergraph/STEWARD-by-Aftergraph@" + strings.Repeat("c", 40)
	conflict := bindSubjectV2(t, base, workID, executionID, subjectBody(other))
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("different-subject rebind status=%d want=409", conflict.StatusCode)
	}
}

func TestDispatchV2SubjectBindingRejectsWrongCausalTuple(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	executionID, _ := acceptV2(t, base, workID, leaseID)
	body := strings.Replace(subjectBody(dispatchV2Subject), `"effect_id":"effect/1"`, `"effect_id":"effect/other"`, 1)
	resp := bindSubjectV2(t, base, workID, executionID, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want=409", resp.StatusCode)
	}
}

func TestDispatchV2ReplayReturnsSameCorrelation(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	_, first := acceptV2(t, base, workID, leaseID)
	_, second := acceptV2(t, base, workID, leaseID)
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
	_, _, leaseID := setupDispatchV2Work(t)
	base2, workID2, _ := setupDispatchV2Work(t)
	resp := postDispatchV2(t, base2, workID2, dispatchV2Fixture(leaseID))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict { t.Fatalf("status=%d want=409", resp.StatusCode) }
}

func TestDispatchV2CausalMismatchFailsClosed(t *testing.T) {
	base, workID, leaseID := setupDispatchV2Work(t)
	first := postDispatchV2(t, base, workID, dispatchV2Fixture(leaseID))
	first.Body.Close()
	if first.StatusCode != http.StatusOK { t.Fatalf("first status=%d", first.StatusCode) }
	conflict := strings.Replace(dispatchV2Fixture(leaseID), `"causal_id":"causal/1"`, `"causal_id":"causal/other"`, 1)
	resp := postDispatchV2(t, base, workID, conflict)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict { t.Fatalf("status=%d want=409", resp.StatusCode) }
}
