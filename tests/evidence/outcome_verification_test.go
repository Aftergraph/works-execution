package evidence_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Complete != Verified: a successful executor state is not an independent
// verification decision. The public evidence projection must expose this as
// pending until a separate verifier verdict exists.
func TestEvidenceEndpoint_SucceededWithoutIndependentVerdictIsPending(t *testing.T) {
	_, ts, st := newTestAPIServer(t)
	w := seedTerminalWork(t, st, workgraph.StateSucceeded)

	resp, err := http.Get(ts.URL + "/v1/works/" + w.ID + "/evidence")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	ov, ok := got["outcome_verification"].(map[string]any)
	if !ok {
		t.Fatalf("outcome_verification missing: %#v", got["outcome_verification"])
	}
	if ov["status"] != "pending" {
		t.Fatalf("SUCCEEDED self-upgraded to verification=%v; want pending", ov["status"])
	}
	if _, exists := ov["verifier_id"]; exists {
		t.Fatalf("pending projection invented verifier_id: %#v", ov)
	}
	if _, exists := ov["evidence_ref"]; exists {
		t.Fatalf("pending projection invented evidence_ref: %#v", ov)
	}
	if _, exists := ov["verified_at"]; exists {
		t.Fatalf("pending projection invented verified_at: %#v", ov)
	}
}

// evidence_verdicts is an integrity projection (ok/tampered/unsealed), not an
// outcome verdict. Even if every evidence item is hash-valid, the outcome
// remains pending without an independent verifier.
func TestEvidenceIntegrityOKDoesNotImplyOutcomeVerified(t *testing.T) {
	_, ts, st := newTestAPIServer(t)
	w := seedTerminalWork(t, st, workgraph.StateSucceeded)

	resp, err := http.Get(ts.URL + "/v1/works/" + w.ID + "/evidence")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["evidence_verdicts"]; !ok {
		t.Fatal("existing evidence_verdicts integrity projection disappeared")
	}
	ov, ok := got["outcome_verification"].(map[string]any)
	if !ok || ov["status"] != "pending" {
		t.Fatalf("integrity verdict leaked into outcome semantics: %#v", ov)
	}
}
