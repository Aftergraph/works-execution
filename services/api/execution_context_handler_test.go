package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

const executionContextPlatformToken = "works-platform-api-context-test-0123456789abcdef"
const executionContextBridgeSecret = "works-platform-bridge-context-test-0123456789"

func setupExecutionContextLease(t *testing.T) (*httptest.Server, store.Store, *workgraph.Work, string) {
	t.Helper()
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", executionContextBridgeSecret)
	srv, ts, st := newTestServer(t)
	srv.PlatformAPIToken = []byte(executionContextPlatformToken)
	w := &workgraph.Work{
		ID: workgraph.NewID("wrk"), State: workgraph.StateCreated,
		Source: workgraph.Source{Type: "cli"}, Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := st.CreateWork(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateState(context.Background(), w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := st.GrantLease(context.Background(), w.ID, "a", "wrkr_77777777777777777777777777777777", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return ts, st, w, lease.ID
}

func executionContextBody(leaseID string) string {
	return `{"organization_id":"org_11111111111111111111111111111111","tenant_id":"ten_22222222222222222222222222222222","principal_id":"prn_33333333333333333333333333333333","mission_id":"mis_example","authority_lease_id":"auth_44444444444444444444444444444444","worker_lease_id":"` + leaseID + `","admission_decision_id":"pdr_55555555555555555555555555555555","trace_id":"trc_66666666666666666666666666666666"}`
}


func postExecutionContext(t *testing.T, base, workID, body string, auth bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v1/works/"+workID+"/execution-contexts", strings.NewReader(body))
	if err != nil { t.Fatal(err) }
	req.Header.Set("Content-Type", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+executionContextPlatformToken)
		req.Header.Set("X-Works-Platform-Bridge", executionContextBridgeSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatal(err) }
	return resp
}

func TestExecutionContextPostAndGet(t *testing.T) {
	ts, _, w, leaseID := setupExecutionContextLease(t)
	resp := postExecutionContext(t, ts.URL, w.ID, executionContextBody(leaseID), true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post status=%d", resp.StatusCode)
	}
	var created executioncontext.Context
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.WorkID != w.ID || created.WorkerID == "" {
		t.Fatalf("created=%+v", created)
	}

	gotResp, err := http.Get(ts.URL + "/v1/execution-contexts/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer gotResp.Body.Close()
	if gotResp.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d", gotResp.StatusCode)
	}
	var got executioncontext.Context
	if err := json.NewDecoder(gotResp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got != created {
		t.Fatalf("got=%+v created=%+v", got, created)
	}
}


func TestExecutionContextPostRequiresPlatformAuthority(t *testing.T) {
	ts, _, w, leaseID := setupExecutionContextLease(t)
	resp := postExecutionContext(t, ts.URL, w.ID, executionContextBody(leaseID), false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want=401", resp.StatusCode)
	}
}

func TestExecutionContextPostRejectsClientWorkerID(t *testing.T) {
	ts, _, w, leaseID := setupExecutionContextLease(t)
	body := strings.TrimSuffix(executionContextBody(leaseID), "}") + `,"worker_id":"wrkr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	resp := postExecutionContext(t, ts.URL, w.ID, body, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want=400", resp.StatusCode)
	}
}

func TestExecutionContextPostRejectsForeignLeaseWithoutEnumeration(t *testing.T) {
	ts, st, w1, leaseID := setupExecutionContextLease(t)
	w2 := &workgraph.Work{ID: workgraph.NewID("wrk"), State: workgraph.StateCreated, Source: workgraph.Source{Type: "cli"}, Objective: workgraph.Objective{Type: "verify_change"}, Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}}}
	if err := st.CreateWork(context.Background(), w2); err != nil {
		t.Fatal(err)
	}
	resp := postExecutionContext(t, ts.URL, w2.ID, executionContextBody(leaseID), true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want=404 (w1=%s)", resp.StatusCode, w1.ID)
	}
}

func TestExecutionContextGetUnknownIs404(t *testing.T) {
	ts, _, _, _ := setupExecutionContextLease(t)
	resp, err := http.Get(ts.URL + "/v1/execution-contexts/ctx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want=404", resp.StatusCode)
	}
}
