package api

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
	"github.com/JonasAbde/works-execution/services/runner"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func TestPlacementReservationV2MintsWorksOwnedLeaseForBoundWorker(t *testing.T) {
	const token = "works-platform-api-placement-test-0123456789abcdef"
	const bridge = "works-platform-bridge-placement-test-0123456789"
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", bridge)

	st, err := store.Open(filepath.Join(t.TempDir(), "placement-reservation.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	workerID := "wrkr_jonas_lenovo"
	srv := &Server{
		Store: st,
		PlatformAPIToken: []byte(token),
		RunnerRegistry: newRunnerRegistry(),
	}
	srv.RunnerRegistry.put(&runner.Identity{
		RunnerID: workerID,
		TrustClass: runner.TrustStandard,
		LifecycleState: runner.StateActive,
		EnrolledAt: now.Add(-time.Hour),
		LastHeartbeatAt: ptrToTime(now),
		Capabilities: runner.Capabilities{
			OS: []string{"windows"},
			Arch: []string{"amd64"},
			Labels: []string{"pool:jonas-lenovo"},
		},
	})

	work := &workgraph.Work{
		ID: workgraph.NewID("wrk"),
		State: workgraph.StateQueued,
		Source: workgraph.Source{Type:"controller"},
		Objective: workgraph.Objective{Type:"verify_change"},
		Requirements: workgraph.Requirements{Pool:"jonas-lenovo", OS:"windows", Arch:"amd64"},
		Policy: workgraph.Policy{TrustClass:"standard"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"computer": {ID:"computer", Run:"Write-Output ready"},
		}},
	}
	if err := st.CreateWork(context.Background(), work); err != nil { t.Fatal(err) }

	body := `{
		"schema":"placement.reservation/1.0",
		"node_id":"computer",
		"selected_worker":"wrkr_jonas_lenovo",
		"placement_binding":{
			"schema":"runtime.placement-dispatch-binding/0.1",
			"mission_id":"mis_lume_computer",
			"selected_node":"wrkr_jonas_lenovo",
			"placement_decision_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"snapshot_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"policy_version":"placement-policy/1"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v2/works/"+work.ID+"/placement-reservations", strings.NewReader(body))
	req.SetPathValue("id", work.ID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Works-Platform-Bridge", bridge)
	rec := httptest.NewRecorder()
	srv.reservePlacementV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out placementReservationV2Response
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil { t.Fatal(err) }
	if out.WorkerID != workerID || out.WorkerLeaseID == "" || out.AttemptID == "" {
		t.Fatalf("bad reservation: %+v", out)
	}
	lease, err := st.GetLease(context.Background(), out.WorkerLeaseID)
	if err != nil { t.Fatal(err) }
	if lease.WorkerID != workerID || lease.WorkID != work.ID {
		t.Fatalf("lease binding mismatch: %+v", lease)
	}
}

func TestPlacementReservationV2RejectsPlacementWorkerMismatchBeforeMutation(t *testing.T) {
	const token = "works-platform-api-placement-test-0123456789abcdef"
	const bridge = "works-platform-bridge-placement-test-0123456789"
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", bridge)

	st, err := store.Open(filepath.Join(t.TempDir(), "placement-mismatch.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = st.Close() })
	srv := &Server{Store: st, PlatformAPIToken: []byte(token)}

	work := &workgraph.Work{
		ID: workgraph.NewID("wrk"), State: workgraph.StateQueued,
		Source: workgraph.Source{Type:"controller"},
		Objective: workgraph.Objective{Type:"verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"computer":{ID:"computer",Run:"true"}}},
	}
	if err := st.CreateWork(context.Background(), work); err != nil { t.Fatal(err) }

	body := `{
		"schema":"placement.reservation/1.0",
		"node_id":"computer",
		"selected_worker":"wrkr_a",
		"placement_binding":{
			"schema":"runtime.placement-dispatch-binding/0.1",
			"mission_id":"mis_lume_computer",
			"selected_node":"wrkr_b",
			"placement_decision_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"snapshot_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"policy_version":"placement-policy/1"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v2/works/"+work.ID+"/placement-reservations", strings.NewReader(body))
	req.SetPathValue("id", work.ID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Works-Platform-Bridge", bridge)
	rec := httptest.NewRecorder()
	srv.reservePlacementV2(rec, req)
	if rec.Code != http.StatusConflict { t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String()) }
	active, err := st.ActiveLeasesByWorkID(context.Background(), work.ID)
	if err != nil { t.Fatal(err) }
	if len(active) != 0 { t.Fatalf("mismatch created lease: %+v", active) }
}


func TestPlacementReservationV2AtomicallyMovesCreatedWorkToRunning(t *testing.T) {
	const token = "works-platform-api-placement-created-test"
	const bridge = "works-platform-bridge-placement-created-test"
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", bridge)

	st, err := store.Open(filepath.Join(t.TempDir(), "placement-created.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	workerID := "wrkr_vds_1"
	srv := &Server{
		Store: st,
		PlatformAPIToken: []byte(token),
		RunnerRegistry: newRunnerRegistry(),
	}
	srv.RunnerRegistry.put(&runner.Identity{
		RunnerID: workerID,
		TrustClass: runner.TrustStandard,
		LifecycleState: runner.StateActive,
		EnrolledAt: now.Add(-time.Hour),
		LastHeartbeatAt: ptrToTime(now),
		Capabilities: runner.Capabilities{
			OS: []string{"linux"},
			Arch: []string{"amd64"},
			Labels: []string{"pool:vds"},
		},
	})

	work := &workgraph.Work{
		ID: workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Source: workgraph.Source{Type:"api"},
		Objective: workgraph.Objective{Type:"custom"},
		Requirements: workgraph.Requirements{Pool:"vds", OS:"linux", Arch:"amd64"},
		Policy: workgraph.Policy{TrustClass:"standard"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"computer": {ID:"computer", Run:"runtime:computer"},
		}},
	}
	if err := st.CreateWork(context.Background(), work); err != nil { t.Fatal(err) }

	body := `{
		"schema":"placement.reservation/1.0",
		"node_id":"computer",
		"selected_worker":"wrkr_vds_1",
		"placement_binding":{
			"schema":"runtime.placement-dispatch-binding/0.1",
			"mission_id":"mis_lume_computer",
			"selected_node":"wrkr_vds_1",
			"placement_decision_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"snapshot_digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			"policy_version":"placement-policy/1"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v2/works/"+work.ID+"/placement-reservations", strings.NewReader(body))
	req.SetPathValue("id", work.ID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Works-Platform-Bridge", bridge)
	rec := httptest.NewRecorder()
	srv.reservePlacementV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	got, err := st.GetWork(context.Background(), work.ID)
	if err != nil { t.Fatal(err) }
	if got.State != workgraph.StateRunning {
		t.Fatalf("state=%s want RUNNING", got.State)
	}
	var out placementReservationV2Response
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil { t.Fatal(err) }
	if out.WorkerID != workerID || out.WorkerLeaseID == "" || out.AttemptID == "" {
		t.Fatalf("bad reservation: %+v", out)
	}
}
