package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// TestRunnerHeartbeatPersists verifies that re-registering a runner
// refreshes its LastHeartbeatAt in the registry (RFC-0004 heartbeat).
// Regression: get() returns a copy; without persisting the mutation
// back, the heartbeat was lost and the runner went stale after 3× the
// heartbeat interval — silently disabling pool routing AND cache
// fingerprinting in the ready handler.
func TestRunnerHeartbeatPersists(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := &api.Server{Store: s}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	reg := func() *http.Response {
		body := `{"runner_id":"wrkr_hb_1","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":["pool:avc-core"],"os":["linux"],"arch":["amd64"]}}`
		resp, err := http.Post(ts.URL+"/v1/runners/register", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// First registration.
	r1 := reg()
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK && r1.StatusCode != http.StatusCreated {
		t.Fatalf("first register: status %d", r1.StatusCode)
	}

	// Wait a moment, then re-register (the worker's heartbeat).
	time.Sleep(1100 * time.Millisecond)
	r2 := reg()
	defer r2.Body.Close()
	var updated struct {
		LastHeartbeatAt string `json:"last_heartbeat_at"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	hb2, err := time.Parse(time.RFC3339Nano, updated.LastHeartbeatAt)
	if err != nil {
		t.Fatalf("parse heartbeat: %v", err)
	}

	// The registry must reflect the refreshed heartbeat.
	resp, err := http.Get(ts.URL + "/v1/runners/wrkr_hb_1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var stored struct {
		LastHeartbeatAt string `json:"last_heartbeat_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	hbStored, err := time.Parse(time.RFC3339Nano, stored.LastHeartbeatAt)
	if err != nil {
		t.Fatalf("parse stored heartbeat: %v", err)
	}
	if !hbStored.Equal(hb2) {
		t.Errorf("stored heartbeat %v != refreshed %v — heartbeat lost (registry copy bug)", hbStored, hb2)
	}
}
