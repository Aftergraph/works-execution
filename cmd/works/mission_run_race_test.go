package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func TestMissionRunPostSubmitReconcilesConcurrentIdentityRace(t *testing.T) {
	want := compiledMission(t, missionYAML)
	stored := *want
	stored.Objective.Constraints = map[string]any{"mission_spec_sha256": "other-spec"}
	stored.State = workgraph.StateRunning
	var gets atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if gets.Add(1) == 1 {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(&stored)
		case http.MethodPost:
			queued := *want
			queued.State = workgraph.StateQueued
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(&queued)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out, errOut strings.Builder
	err := runMission([]string{"--config", writeMissionConfig(t, missionYAML), "--api", srv.URL}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "different spec") {
		t.Fatalf("concurrent mission-id race was not rejected: err=%v output=%q", err, out.String())
	}
	if gets.Load() < 2 {
		t.Fatalf("expected post-submit reconciliation GET, got %d GETs", gets.Load())
	}
}
