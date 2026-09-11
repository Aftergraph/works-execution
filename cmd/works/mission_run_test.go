package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JonasAbde/works-execution/packages/missionhandoff"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

const missionYAML = `version: 1
mission_id: mission-cli-test-001
objective: survive coordinator exit
purpose_bindings: [test]
budget: {wall_clock_h: 1}
verification:
  - criterion: marker exists
    kind: deterministic
stages:
  execute:
    run: echo ok
    permissions: [read, execute]
`

func writeMissionConfig(t *testing.T, raw string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mission.yaml")
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil { t.Fatal(err) }
	return p
}

func compiledMission(t *testing.T, raw string) *workgraph.Work {
	t.Helper()
	cfg, err := missionhandoff.Parse([]byte(raw))
	if err != nil { t.Fatal(err) }
	w, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	return w
}

func TestMissionRunDetachedCreatesQueuedDurableWork(t *testing.T) {
	want := compiledMission(t, missionYAML)
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" { http.Error(w, "unauthorized", 401); return }
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/works/"+want.ID:
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/works":
			posts.Add(1)
			var body struct { workgraph.Work; Queue bool `json:"queue"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
			if !body.Queue { t.Error("mission was not queued") }
			if body.ID != want.ID || body.IdempotencyKey != "mission-cli-test-001" { t.Errorf("identity=%s idem=%s", body.ID, body.IdempotencyKey) }
			body.State = workgraph.StateQueued
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(body.Work)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var out, errOut strings.Builder
	err := runMission([]string{"--config", writeMissionConfig(t, missionYAML), "--api", srv.URL, "--token", "test-token"}, &out, &errOut)
	if err != nil { t.Fatalf("runMission: %v stderr=%s", err, errOut.String()) }
	if posts.Load() != 1 { t.Fatalf("posts=%d", posts.Load()) }
	if !strings.Contains(out.String(), "submitted durable mission") || !strings.Contains(out.String(), want.ID) { t.Fatalf("output=%q", out.String()) }
}

func TestMissionRunDuplicateResolvesExistingWithoutSecondPost(t *testing.T) {
	existing := compiledMission(t, missionYAML)
	existing.State = workgraph.StateRunning
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/works/"+existing.ID {
			_ = json.NewEncoder(w).Encode(existing); return
		}
		if r.Method == http.MethodPost { posts.Add(1) }
		http.NotFound(w, r)
	}))
	defer srv.Close()
	var out, errOut strings.Builder
	err := runMission([]string{"--config", writeMissionConfig(t, missionYAML), "--api", srv.URL}, &out, &errOut)
	if err != nil { t.Fatalf("runMission: %v", err) }
	if posts.Load() != 0 { t.Fatalf("duplicate caused %d POSTs", posts.Load()) }
	if !strings.Contains(out.String(), "reconciled existing") { t.Fatalf("output=%q", out.String()) }
}

func TestMissionRunRejectsMissionIDReuseWithDifferentSpec(t *testing.T) {
	existing := compiledMission(t, missionYAML)
	existing.Objective.Constraints["mission_spec_sha256"] = "different"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { _ = json.NewEncoder(w).Encode(existing); return }
		t.Fatal("changed spec must fail before POST")
	}))
	defer srv.Close()
	var out, errOut strings.Builder
	err := runMission([]string{"--config", writeMissionConfig(t, missionYAML), "--api", srv.URL}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "different spec") { t.Fatalf("err=%v", err) }
}

func TestMissionRunFollowObservesTerminalState(t *testing.T) {
	want := compiledMission(t, missionYAML)
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			n := gets.Add(1)
			if n == 1 { http.NotFound(w, r); return }
			done := *want; done.State = workgraph.StateSucceeded
			_ = json.NewEncoder(w).Encode(&done)
		case http.MethodPost:
			queued := *want; queued.State = workgraph.StateQueued
			w.WriteHeader(http.StatusCreated); _ = json.NewEncoder(w).Encode(&queued)
		default:
			http.Error(w, fmt.Sprintf("unexpected %s", r.Method), 500)
		}
	}))
	defer srv.Close()
	var out, errOut strings.Builder
	err := runMission([]string{"--config", writeMissionConfig(t, missionYAML), "--api", srv.URL, "--follow"}, &out, &errOut)
	if err != nil { t.Fatalf("runMission: %v", err) }
	if !strings.Contains(out.String(), "state=SUCCEEDED") { t.Fatalf("output=%q", out.String()) }
}
