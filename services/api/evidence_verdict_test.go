package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/evidence"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// seedTerminalWork drives a single-node work to SUCCEEDED and returns it.
func seedTerminalWork(t *testing.T, s store.Store) *workgraph.Work {
	t.Helper()
	ctx := context.Background()
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	lease, _, err := s.GrantLease(ctx, w.ID, "a", "wkr", 5*time.Second)
	if err != nil {
		t.Fatalf("GrantLease: %v", err)
	}
	art := &workgraph.Artifact{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", NodeID: "a", MimeType: "text/plain", Size: 1, Path: "/tmp/o"}
	if _, err := s.CompleteLease(ctx, lease.ID, 0, art, nil); err != nil {
		t.Fatalf("CompleteLease: %v", err)
	}
	got, err := s.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if !got.State.IsTerminal() {
		if _, err := s.UpdateState(ctx, w.ID, workgraph.StateSucceeded); err != nil {
			t.Fatalf("UpdateState SUCCEEDED: %v", err)
		}
		got, err = s.GetWork(ctx, w.ID)
		if err != nil {
			t.Fatalf("GetWork: %v", err)
		}
	}
	return got
}

func verdictKey(t *testing.T) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte("evidence-verdict-test-key"))
	return sum[:]
}

func getOutcomeVerification(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // test-local httptest server
	if err != nil {
		t.Fatalf("GET evidence: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ov, ok := out["outcome_verification"].(map[string]any)
	if !ok {
		t.Fatalf("outcome_verification missing: %v", out)
	}
	return ov
}

func TestEvidence_OutcomeVerificationPendingWithoutVerdict(t *testing.T) {
	srv, ts, s := newTestServer(t)
	srv.EvidenceConfig = &api.EvidenceConfig{
		KeyID: "k1", HMACKey: verdictKey(t),
		Runner: evidence.Runner{ID: "r1", TrustClass: "standard"},
	}
	w := seedTerminalWork(t, s)
	ov := getOutcomeVerification(t, ts.URL+"/v1/works/"+w.ID+"/evidence")
	if ov["status"] != "pending" {
		t.Errorf("status: got %v, want pending", ov["status"])
	}
	if _, ok := ov["verifier_id"]; ok {
		t.Errorf("verifier_id must be omitted when pending, got %v", ov["verifier_id"])
	}
}

func TestEvidence_OutcomeVerificationProjectsStoredVerdict(t *testing.T) {
	srv, ts, s := newTestServer(t)
	srv.EvidenceConfig = &api.EvidenceConfig{
		KeyID: "k1", HMACKey: verdictKey(t),
		Runner: evidence.Runner{ID: "r1", TrustClass: "standard"},
	}
	w := seedTerminalWork(t, s)
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if err := s.SaveVerificationVerdict(context.Background(), store.VerificationVerdict{
		WorkID: w.ID, Result: "passed", VerifierID: "verifier-7",
		EvidenceRef: "evb_probe", VerifiedAt: at,
	}); err != nil {
		t.Fatalf("SaveVerificationVerdict: %v", err)
	}
	ov := getOutcomeVerification(t, ts.URL+"/v1/works/"+w.ID+"/evidence")
	if ov["status"] != "passed" {
		t.Errorf("status: got %v, want passed", ov["status"])
	}
	if ov["verifier_id"] != "verifier-7" {
		t.Errorf("verifier_id: got %v, want verifier-7", ov["verifier_id"])
	}
	if ov["evidence_ref"] != "evb_probe" {
		t.Errorf("evidence_ref: got %v, want evb_probe", ov["evidence_ref"])
	}
	if ov["verified_at"] != "2026-09-10T12:00:00Z" {
		t.Errorf("verified_at: got %v, want 2026-09-10T12:00:00Z", ov["verified_at"])
	}
}
