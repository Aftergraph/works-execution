// Package policy_test exercises the first OPA Rego policy bundle
// (k-impl-011, slice 4). The tests cover both the engine in isolation
// (TestEngine_*) and its integration with POST /v1/leases/grant
// (TestLeaseGrant_Policy_*).
//
// The Rego source under test is at ../../policies/lease_grant.rego.
// We load it from disk in TestMain so the test binary can be run from
// any working directory.
package policy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/runner"
	"github.com/JonasAbde/works-execution/services/work/store"
)

var defaultBundle *api.Engine

// TestMain loads the default Rego bundle once for all tests. If the bundle
// fails to parse, every test fails fast with a clear message — a parse
// error here means the bundle under test is broken, not a test bug.
func TestMain(m *testing.M) {
	repoRoot := findRepoRoot()
	src, err := os.ReadFile(filepath.Join(repoRoot, "policies", "lease_grant.rego"))
	if err != nil {
		// Fail loud: print to stderr and exit non-zero.
		os.Stderr.WriteString("FATAL: cannot read policies/lease_grant.rego: " + err.Error() + "\n")
		os.Exit(2)
	}
	eng, err := api.NewEngine(string(src))
	if err != nil {
		os.Stderr.WriteString("FATAL: cannot compile policies/lease_grant.rego: " + err.Error() + "\n")
		os.Exit(2)
	}
	defaultBundle = eng
	os.Exit(m.Run())
}

// findRepoRoot walks up from the test's working directory looking for the
// repo's go.mod file. This lets `go test ./tests/policy/` work whether
// invoked from the repo root or the tests/policy directory.
func findRepoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return wd
}

// -----------------------------------------------------------------------------
// Test helpers
// -----------------------------------------------------------------------------

// newTestServer creates an api.Server backed by a fresh SQLite DB and the
// default policy engine. Each test gets its own server so they can run in
// parallel without cross-contamination.
func newTestServer(t *testing.T) (*httptest.Server, store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &api.Server{
		Store:  st,
		Policy: defaultBundle,
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, st
}

// sampleWork returns a fresh Work with the given policy. The ID is
// server-minted; the test creates the work via the API.
func sampleWork(policy workgraph.Policy) workgraph.Work {
	return workgraph.Work{
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{
				"a": {ID: "a", Run: "echo a"},
			},
		},
		Policy: policy,
	}
}

// registerRunner POSTs a runner identity to /v1/runners/register so the
// policy engine can resolve the runner's trust_class. Without this, the
// runner defaults to TrustUntrusted in the API layer and any
// production-class work will be denied. Sends a fully-populated
// runner-identity record (per docs/standards/schemas/runner-identity.schema.json).
func registerRunner(t *testing.T, ts *httptest.Server, runnerID string, trust runner.TrustClass) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"runner_id":       runnerID,
		"spiffe_id":       "spiffe://works-execution/ns/default/sa/" + runnerID,
		"trust_class":     string(trust),
		"lifecycle_state": string(runner.StateActive),
		"enrolled_at":     time.Now().UTC().Format(time.RFC3339),
		"capabilities": map[string]any{
			"os":           []string{"linux"},
			"arch":         []string{"amd64"},
			"cpu_milli":    4000,
			"memory_mib":   8192,
			"gpu":          0,
			"toolchains":   []string{"go1.23"},
			"labels":       []string{"e2e"},
		},
	})
	resp, err := http.Post(ts.URL+"/v1/runners/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register runner: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var b map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&b)
		t.Fatalf("register runner: status %d (body=%v)", resp.StatusCode, b)
	}
}

// createWork POSTs a work to the API and returns its server-assigned ID.
func createWork(t *testing.T, ts *httptest.Server, w workgraph.Work) string {
	t.Helper()
	body, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/v1/works?queue=true", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create work: status %d", resp.StatusCode)
	}
	var got workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.ID
}

// appendEvidence attaches an evidence record to an existing work via the
// store. We bypass the API for this — the evidence endpoint isn't part of
// the contract under test here, and the store layer is the source of truth
// for "what evidence is associated with this work".
func appendEvidence(t *testing.T, st store.Store, workID string, ev workgraph.Evidence) {
	t.Helper()
	if _, err := st.AppendEvidence(context.Background(), workID, ev); err != nil {
		t.Fatal(err)
	}
}

// -----------------------------------------------------------------------------
// Engine-level tests (no HTTP)
// -----------------------------------------------------------------------------

// TestEngine_NonProductionAllowed is the happy path: a work that does not
// require production access is granted without any evidence or trust check.
func TestEngine_NonProductionAllowed(t *testing.T) {
	t.Parallel()
	in := api.DecisionInput{
		Request: api.RequestContext{Action: "lease.grant", WorkID: "wrk_x", NodeID: "a", WorkerID: "wrkr_untrusted"},
		Work: api.WorkView{
			ID:     "wrk_x",
			Policy: workgraph.Policy{ProductionAccess: false, TrustClass: "standard"},
		},
		Evidence: nil,
		Runner: api.RunnerView{
			RunnerID:       "wrkr_untrusted",
			TrustClass:     runner.TrustUntrusted,
			LifecycleState: runner.StateActive,
		},
	}
	dec, err := defaultBundle.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allow {
		t.Errorf("expected allow=true, got %+v", dec)
	}
	if len(dec.DenyReasons) != 0 {
		t.Errorf("expected no deny reasons, got %v", dec.DenyReasons)
	}
	if dec.RequiredTrust != "standard" {
		t.Errorf("required_trust: got %q, want \"standard\"", dec.RequiredTrust)
	}
}

// TestEngine_ProductionAllowedWithEvidence verifies the core rule:
// production_access=true + approved evidence + standard trust + active runner
// → allow.
func TestEngine_ProductionAllowedWithEvidence(t *testing.T) {
	t.Parallel()
	in := api.DecisionInput{
		Request: api.RequestContext{Action: "lease.grant", WorkID: "wrk_y", NodeID: "a", WorkerID: "wrkr_standard"},
		Work: api.WorkView{
			ID: "wrk_y",
			Policy: workgraph.Policy{
				ProductionAccess: true,
				TrustClass:       "standard",
			},
		},
		Evidence: []workgraph.Evidence{
			{ID: "ev_1", Type: "test", Result: "pass"},
		},
		Runner: api.RunnerView{
			RunnerID:       "wrkr_standard",
			TrustClass:     runner.TrustStandard,
			LifecycleState: runner.StateActive,
		},
	}
	dec, err := defaultBundle.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allow {
		t.Errorf("expected allow=true, got %+v", dec)
	}
}

// TestEngine_ProductionDeniedMissingEvidence verifies the deny reason
// for the "no approved evidence" case.
func TestEngine_ProductionDeniedMissingEvidence(t *testing.T) {
	t.Parallel()
	in := api.DecisionInput{
		Request: api.RequestContext{Action: "lease.grant", WorkID: "wrk_z", NodeID: "a", WorkerID: "wrkr_standard"},
		Work: api.WorkView{
			ID: "wrk_z",
			Policy: workgraph.Policy{
				ProductionAccess: true,
				TrustClass:       "standard",
			},
		},
		Evidence: []workgraph.Evidence{}, // empty
		Runner: api.RunnerView{
			RunnerID:       "wrkr_standard",
			TrustClass:     runner.TrustStandard,
			LifecycleState: runner.StateActive,
		},
	}
	dec, err := defaultBundle.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allow {
		t.Errorf("expected allow=false, got %+v", dec)
	}
	if !containsReason(dec.DenyReasons, api.ReasonMissingApprovedEvidence) {
		t.Errorf("expected deny reason %q, got %v", api.ReasonMissingApprovedEvidence, dec.DenyReasons)
	}
}

// TestEngine_ProductionDeniedTrustBelowFloor verifies the deny reason
// for "runner trust < work trust floor".
func TestEngine_ProductionDeniedTrustBelowFloor(t *testing.T) {
	t.Parallel()
	in := api.DecisionInput{
		Request: api.RequestContext{Action: "lease.grant", WorkID: "wrk_q", NodeID: "a", WorkerID: "wrkr_untrusted"},
		Work: api.WorkView{
			ID: "wrk_q",
			Policy: workgraph.Policy{
				ProductionAccess: true,
				TrustClass:       "standard", // requires at least standard
			},
		},
		Evidence: []workgraph.Evidence{
			{ID: "ev_1", Type: "test", Result: "pass"},
		},
		Runner: api.RunnerView{
			RunnerID:       "wrkr_untrusted",
			TrustClass:     runner.TrustUntrusted, // below standard
			LifecycleState: runner.StateActive,
		},
	}
	dec, err := defaultBundle.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allow {
		t.Errorf("expected allow=false, got %+v", dec)
	}
	if !containsReason(dec.DenyReasons, api.ReasonRunnerTrustBelowFloor) {
		t.Errorf("expected deny reason %q, got %v", api.ReasonRunnerTrustBelowFloor, dec.DenyReasons)
	}
}

// -----------------------------------------------------------------------------
// Integration tests: POST /v1/leases/grant with policy enforcement
// -----------------------------------------------------------------------------

// TestLeaseGrant_PolicyEnforced_NonProduction verifies that the policy
// engine is actually invoked by the lease-grant endpoint and that a
// non-production work succeeds (201 Created).
func TestLeaseGrant_PolicyEnforced_NonProduction(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	w := sampleWork(workgraph.Policy{ProductionAccess: false})
	w.ID = createWork(t, ts, w)

	body, _ := json.Marshal(map[string]any{
		"work_id":  w.ID,
		"node_id":  "a",
		"worker_id": "wrkr_any",
		"ttl_seconds": 25,
	})
	resp, err := http.Post(ts.URL+"/v1/leases/grant", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		var b map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&b)
		t.Fatalf("status: got %d, want 201 (body=%v)", resp.StatusCode, b)
	}
}

// TestLeaseGrant_PolicyDenied_MissingEvidence verifies that a production
// work with no evidence is rejected at the HTTP layer with 403 + a stable
// error code. This is the integration check that the policy engine is
// actually wired into the request path.
func TestLeaseGrant_PolicyDenied_MissingEvidence(t *testing.T) {
	t.Parallel()
	ts, st := newTestServer(t)
	w := sampleWork(workgraph.Policy{
		ProductionAccess: true,
		TrustClass:       "standard",
	})
	w.ID = createWork(t, ts, w)

	// Note: NO evidence appended.
	_ = st // silence unused

	body, _ := json.Marshal(map[string]any{
		"work_id":   w.ID,
		"node_id":   "a",
		"worker_id": "wrkr_standard",
	})
	resp, err := http.Post(ts.URL+"/v1/leases/grant", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		var b map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&b)
		t.Fatalf("status: got %d, want 403 (body=%v)", resp.StatusCode, b)
	}
	var errBody struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Error != "policy_missing_approved_evidence" {
		t.Errorf("error code: got %q, want %q", errBody.Error, "policy_missing_approved_evidence")
	}
}

// TestLeaseGrant_PolicyAllowed_WithEvidence is the happy-path integration
// test: production work + approved evidence + standard runner → 201.
func TestLeaseGrant_PolicyAllowed_WithEvidence(t *testing.T) {
	t.Parallel()
	ts, st := newTestServer(t)
	w := sampleWork(workgraph.Policy{
		ProductionAccess: true,
		TrustClass:       "standard",
	})
	w.ID = createWork(t, ts, w)
	registerRunner(t, ts, "wrkr_standard", runner.TrustStandard)

	appendEvidence(t, st, w.ID, workgraph.Evidence{
		ID:      "ev_pass",
		Type:    "test",
		Result:  "pass",
		Signer:  "ci",
		RecordedAt: time.Now().UTC(),
	})

	body, _ := json.Marshal(map[string]any{
		"work_id":   w.ID,
		"node_id":   "a",
		"worker_id": "wrkr_standard",
	})
	resp, err := http.Post(ts.URL+"/v1/leases/grant", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		var b map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&b)
		t.Fatalf("status: got %d, want 201 (body=%v)", resp.StatusCode, b)
	}
}

// TestLeaseGrant_PolicyDenied_TrustBelowFloor is the second deny path:
// production work has evidence but the runner is "untrusted" while the
// work floor is "standard". Must return 403 with a trust-related code.
func TestLeaseGrant_PolicyDenied_TrustBelowFloor(t *testing.T) {
	t.Parallel()
	ts, st := newTestServer(t)
	w := sampleWork(workgraph.Policy{
		ProductionAccess: true,
		TrustClass:       "standard",
	})
	w.ID = createWork(t, ts, w)

	appendEvidence(t, st, w.ID, workgraph.Evidence{
		ID:      "ev_pass",
		Type:    "test",
		Result:  "pass",
		RecordedAt: time.Now().UTC(),
	})

	// WorkerID not in any registry → defaults to TrustUntrusted.
	body, _ := json.Marshal(map[string]any{
		"work_id":   w.ID,
		"node_id":   "a",
		"worker_id": "wrkr_untrusted_never_seen",
	})
	resp, err := http.Post(ts.URL+"/v1/leases/grant", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		var b map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&b)
		t.Fatalf("status: got %d, want 403 (body=%v)", resp.StatusCode, b)
	}
	var errBody struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Error != "policy_runner_trust_below_floor" {
		t.Errorf("error code: got %q, want %q", errBody.Error, "policy_runner_trust_below_floor")
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// containsReason returns true if reason appears in the slice. We use a
// helper instead of slices.Contains so the tests don't pull in extra deps.
func containsReason(rs []string, reason string) bool {
	for _, r := range rs {
		if r == reason {
			return true
		}
	}
	return false
}