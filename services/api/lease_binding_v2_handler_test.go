package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func setupLeaseBindingWork(t *testing.T, ttl time.Duration) (base, workID, leaseID string) {
	t.Helper()
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", dispatchV2BridgeSecret)
	srv, ts, st := newTestServer(t)
	srv.PlatformAPIToken = []byte(dispatchV2PlatformToken)

	ctx := context.Background()
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "api"},
		Objective: workgraph.Objective{Type: "custom"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"a": {ID: "a", Run: "true"},
		}},
	}
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := st.GrantLease(
		ctx,
		w.ID,
		"a",
		"wrkr_77777777777777777777777777777777",
		ttl,
	)
	if err != nil {
		t.Fatal(err)
	}
	return ts.URL, w.ID, lease.ID
}

func getLeaseBinding(
	t *testing.T,
	base, workID, nodeID, workerID string,
	withBridge bool,
) *http.Response {
	t.Helper()
	u := base + "/v2/works/" + url.PathEscape(workID) +
		"/lease-binding?node_id=" + url.QueryEscape(nodeID) +
		"&worker_id=" + url.QueryEscape(workerID)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+dispatchV2PlatformToken)
	if withBridge {
		req.Header.Set("X-Works-Platform-Bridge", dispatchV2BridgeSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLeaseBindingV2ReturnsExactActiveWorkerLease(t *testing.T) {
	base, workID, leaseID := setupLeaseBindingWork(t, time.Minute)

	read := func() map[string]any {
		resp := getLeaseBinding(
			t,
			base,
			workID,
			"a",
			"wrkr_77777777777777777777777777777777",
			true,
		)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want=200", resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := read()
	second := read()

	if first["schema"] != "works.worker-lease-binding/1.0" {
		t.Fatalf("schema=%v", first["schema"])
	}
	if first["work_id"] != workID ||
		first["node_id"] != "a" ||
		first["worker_id"] != "wrkr_77777777777777777777777777777777" ||
		first["worker_lease_id"] != leaseID ||
		first["status"] != "ACTIVE" {
		t.Fatalf("unexpected binding: %+v", first)
	}
	if first["attempt_id"] == "" || first["attempt_id"] == nil {
		t.Fatalf("attempt_id missing: %+v", first)
	}
	if first["worker_lease_id"] != second["worker_lease_id"] ||
		first["attempt_id"] != second["attempt_id"] ||
		first["expires_at"] != second["expires_at"] {
		t.Fatalf("read replay changed canonical binding: first=%+v second=%+v", first, second)
	}
}

func TestLeaseBindingV2WrongWorkerReturnsNotFound(t *testing.T) {
	base, workID, _ := setupLeaseBindingWork(t, time.Minute)

	resp := getLeaseBinding(
		t,
		base,
		workID,
		"a",
		"wrkr_other",
		true,
	)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want=404", resp.StatusCode)
	}
}

func TestLeaseBindingV2ExpiredActiveRowFailsClosed(t *testing.T) {
	base, workID, _ := setupLeaseBindingWork(t, -time.Second)

	resp := getLeaseBinding(
		t,
		base,
		workID,
		"a",
		"wrkr_77777777777777777777777777777777",
		true,
	)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want=409", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["error"] != "worker_lease_stale" {
		t.Fatalf("error=%v want=worker_lease_stale", out["error"])
	}
}

func TestLeaseBindingV2RequiresPlatformBridgeCredential(t *testing.T) {
	base, workID, _ := setupLeaseBindingWork(t, time.Minute)

	resp := getLeaseBinding(
		t,
		base,
		workID,
		"a",
		"wrkr_77777777777777777777777777777777",
		false,
	)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want=401", resp.StatusCode)
	}
}
