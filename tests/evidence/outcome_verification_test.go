package evidence_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
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

// Failure is also execution state, not a verifier decision. This keeps the
// read model symmetric and prevents consumers from inventing a semantic
// verifier verdict merely because execution reached a terminal state.
func TestEvidenceEndpoint_FailedWithoutIndependentVerdictIsPending(t *testing.T) {
	_, ts, st := newTestAPIServer(t)
	w := seedTerminalWork(t, st, workgraph.StateFailed)

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
	if !ok || ov["status"] != "pending" {
		t.Fatalf("terminal failure invented outcome verification: %#v", ov)
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


func TestPlatformVerification_PassedOutcomeStillFailsClosedOnProvenanceGap(t *testing.T) {
	_, ts, st := newTestAPIServer(t)
	w := seedTerminalWork(t, st, workgraph.StateSucceeded)
	_ = seedV21Context(t, st, w)
	if err := st.SaveVerificationVerdict(context.Background(), store.VerificationVerdict{
		WorkID: w.ID, Result: "passed", VerifierID: "verifier-independent",
		EvidenceRef: "verdict://passed-gap", VerifiedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerificationVerdict: %v", err)
	}

	resp, err := http.Get(ts.URL + "/v1/works/" + w.ID + "/evidence")
	if err != nil { t.Fatal(err) }
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil { t.Fatal(err) }

	ov := got["outcome_verification"].(map[string]any)
	if ov["status"] != "passed" {
		t.Fatalf("independent verifier projection changed: %#v", ov)
	}
	pv := got["platform_outcome_verification"].(map[string]any)
	if pv["status"] != "provenance_gap" || pv["outcome_status"] != "passed" {
		t.Fatalf("passed outcome bypassed provenance gate: %#v", pv)
	}
}

func TestPlatformVerification_CompleteCorrelationAndPassedOutcomeProjectsVerified(t *testing.T) {
	_, ts, st := newTestAPIServer(t)
	w := seedTerminalWork(t, st, workgraph.StateSucceeded)
	ctx := seedV21Context(t, st, w)
	appendExecutionPDR(t, st, w, ctx.ID, "pdr_77777777777777777777777777777777")
	if err := st.SaveVerificationVerdict(context.Background(), store.VerificationVerdict{
		WorkID: w.ID, Result: "passed", VerifierID: "verifier-independent",
		EvidenceRef: "verdict://passed-complete", VerifiedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerificationVerdict: %v", err)
	}

	resp, err := http.Get(ts.URL + "/v1/works/" + w.ID + "/evidence")
	if err != nil { t.Fatal(err) }
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil { t.Fatal(err) }

	pv := got["platform_outcome_verification"].(map[string]any)
	if pv["status"] != "verified" || pv["outcome_status"] != "passed" {
		t.Fatalf("complete V2.1 provenance not projected verified: %#v", pv)
	}
	chain := got["identity_chain"].(map[string]any)
	if chain["execution_context_id"] != ctx.ID ||
		chain["execution_policy_decision_id"] != "pdr_77777777777777777777777777777777" {
		t.Fatalf("identity chain incomplete: %#v", chain)
	}
}
