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

func TestGrantLeaseRequiresLivePoolRunner(t *testing.T) {
	now := time.Now().UTC()
	activeRunner := &runner.Identity{
		RunnerID:        "wrkr_jonas_lenovo",
		TrustClass:      runner.TrustStandard,
		LifecycleState:  runner.StateActive,
		EnrolledAt:      now.Add(-time.Hour),
		LastHeartbeatAt: ptrToTime(now),
		Capabilities: runner.Capabilities{
			OS:     []string{"windows"},
			Arch:   []string{"amd64"},
			Labels: []string{"pool:jonas-lenovo"},
		},
	}

	cases := []struct {
		name     string
		workPool string
		registry bool
		identity *runner.Identity
		want     int
	}{
		{name: "missing registry", workPool: "jonas-lenovo", want: http.StatusForbidden},
		{name: "unregistered runner", workPool: "jonas-lenovo", registry: true, want: http.StatusForbidden},
		{
			name:     "missing heartbeat",
			workPool: "jonas-lenovo",
			registry: true,
			identity: withHeartbeat(activeRunner, nil),
			want:     http.StatusForbidden,
		},
		{
			name:     "stale heartbeat",
			workPool: "jonas-lenovo",
			registry: true,
			identity: withHeartbeat(activeRunner, ptrToTime(now.Add(-4*defaultHeartbeatInterval))),
			want:     http.StatusForbidden,
		},
		{
			name:     "inactive runner",
			workPool: "jonas-lenovo",
			registry: true,
			identity: withLifecycle(activeRunner, runner.StateDraining),
			want:     http.StatusForbidden,
		},
		{
			name:     "wrong pool",
			workPool: "jonas-lenovo",
			registry: true,
			identity: withLabels(activeRunner, []string{"pool:other"}),
			want:     http.StatusForbidden,
		},
		{
			name:     "pool member with wrong operating system",
			workPool: "jonas-lenovo",
			registry: true,
			identity: withOS(activeRunner, []string{"linux"}),
			want:     http.StatusForbidden,
		},
		{
			name:     "active live member",
			workPool: "jonas-lenovo",
			registry: true,
			identity: activeRunner,
			want:     http.StatusCreated,
		},
		{name: "unscoped legacy work", want: http.StatusCreated},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "pool-gate.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })

			srv := &Server{Store: st, AuthEnabled: true}
			if tc.registry {
				srv.RunnerRegistry = newRunnerRegistry()
				if tc.identity != nil {
					srv.RunnerRegistry.put(tc.identity)
				}
			}
			ts := httptest.NewServer(srv.Routes())
			t.Cleanup(ts.Close)

			workerID := activeRunner.RunnerID
			token, err := srv.Auth.Mint(context.Background(), workerID, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			work := &workgraph.Work{
				ID:           workgraph.NewID("wrk"),
				State:        workgraph.StateQueued,
				Source:       workgraph.Source{Type: "cli"},
				Objective:    workgraph.Objective{Type: "verify_change"},
				Requirements: workgraph.Requirements{Pool: tc.workPool, OS: "windows", Arch: "amd64"},
				Policy:       workgraph.Policy{TrustClass: "standard", ProductionAccess: false},
				Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
					"smoke": {ID: "smoke", Run: "Write-Output ready"},
				}},
			}
			if err := st.CreateWork(context.Background(), work); err != nil {
				t.Fatal(err)
			}

			body, err := json.Marshal(map[string]any{
				"work_id": work.ID, "node_id": "smoke", "worker_id": workerID, "ttl_seconds": 25,
			})
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/leases/grant", strings.NewReader(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}

			if tc.want == http.StatusForbidden {
				active, err := st.ActiveLeasesByWorkIDs(context.Background(), []string{work.ID})
				if err != nil {
					t.Fatal(err)
				}
				if len(active[work.ID]) != 0 {
					t.Fatalf("denied pool claim created active leases: %#v", active[work.ID])
				}
				stored, err := st.GetWork(context.Background(), work.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.State != workgraph.StateQueued {
					t.Fatalf("denied pool claim changed work state to %q", stored.State)
				}
			}
		})
	}
}

func TestListRunnersAliveRequiresActiveFreshHeartbeat(t *testing.T) {
	now := time.Now().UTC()
	base := &runner.Identity{
		RunnerID:        "wrkr_jonas_lenovo",
		TrustClass:      runner.TrustStandard,
		LifecycleState:  runner.StateActive,
		EnrolledAt:      now.Add(-time.Hour),
		LastHeartbeatAt: ptrToTime(now),
		Capabilities: runner.Capabilities{
			OS:     []string{"windows"},
			Arch:   []string{"amd64"},
			Labels: []string{"pool:jonas-lenovo"},
		},
	}
	cases := []struct {
		name     string
		identity *runner.Identity
		want     bool
	}{
		{name: "active with fresh heartbeat", identity: base, want: true},
		{name: "missing heartbeat", identity: withHeartbeat(base, nil)},
		{name: "stale heartbeat", identity: withHeartbeat(base, ptrToTime(now.Add(-4*defaultHeartbeatInterval)))},
		{name: "draining runner", identity: withLifecycle(base, runner.StateDraining)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{RunnerRegistry: newRunnerRegistry()}
			srv.RunnerRegistry.put(tc.identity)
			req := httptest.NewRequest(http.MethodGet, "/v1/runners?pool=jonas-lenovo&alive=true", nil)
			resp := httptest.NewRecorder()
			srv.listRunners(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
			}
			var body struct {
				Runners []runner.Identity `json:"runners"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if got := len(body.Runners) > 0; got != tc.want {
				t.Fatalf("alive result = %v, want %v (runners=%+v)", got, tc.want, body.Runners)
			}
		})
	}
}

func TestReadyDoesNotOfferPoolWorkWithoutLiveRunner(t *testing.T) {
	now := time.Now().UTC()
	active := &runner.Identity{
		RunnerID:        "wrkr_jonas_lenovo",
		TrustClass:      runner.TrustStandard,
		LifecycleState:  runner.StateActive,
		EnrolledAt:      now.Add(-time.Hour),
		LastHeartbeatAt: ptrToTime(now),
		Capabilities: runner.Capabilities{
			OS:     []string{"windows"},
			Arch:   []string{"amd64"},
			Labels: []string{"pool:jonas-lenovo"},
		},
	}
	cases := []struct {
		name     string
		identity *runner.Identity
		want     int
	}{
		{name: "stale runner is not offered work", identity: withHeartbeat(active, ptrToTime(now.Add(-4*defaultHeartbeatInterval))), want: 0},
		{name: "fresh pool runner is offered work", identity: active, want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "ready-pool.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			srv := &Server{Store: st, RunnerRegistry: newRunnerRegistry()}
			srv.RunnerRegistry.put(tc.identity)
			work := &workgraph.Work{
				ID:           workgraph.NewID("wrk"),
				State:        workgraph.StateQueued,
				Source:       workgraph.Source{Type: "cli"},
				Objective:    workgraph.Objective{Type: "verify_change"},
				Requirements: workgraph.Requirements{Pool: "jonas-lenovo", OS: "windows", Arch: "amd64"},
				Policy:       workgraph.Policy{TrustClass: "standard"},
				Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
					"smoke": {ID: "smoke", Run: "Write-Output ready"},
				}},
			}
			if err := st.CreateWork(context.Background(), work); err != nil {
				t.Fatal(err)
			}

			resp := httptest.NewRecorder()
			srv.readyNodesHandler(resp, httptest.NewRequest(http.MethodGet, "/v1/workers/ready", nil))
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (%s)", resp.Code, http.StatusOK, resp.Body.String())
			}
			var body struct {
				Count int `json:"count"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Count != tc.want {
				t.Fatalf("ready count = %d, want %d (%s)", body.Count, tc.want, resp.Body.String())
			}
		})
	}
}

func ptrToTime(t time.Time) *time.Time { return &t }

func withHeartbeat(id *runner.Identity, heartbeat *time.Time) *runner.Identity {
	cp := *id
	cp.LastHeartbeatAt = heartbeat
	return &cp
}

func withLifecycle(id *runner.Identity, lifecycle runner.LifecycleState) *runner.Identity {
	cp := *id
	cp.LifecycleState = lifecycle
	return &cp
}

func withLabels(id *runner.Identity, labels []string) *runner.Identity {
	cp := *id
	cp.Capabilities.Labels = labels
	return &cp
}

func withOS(id *runner.Identity, os []string) *runner.Identity {
	cp := *id
	cp.Capabilities.OS = os
	return &cp
}
