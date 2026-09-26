package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

const missionAcceptanceVerifierToken = "sentinel-verifier-token-p2-test-0123456789abcdef"

func setupMissionAcceptanceV2(t *testing.T) (base, workID, leaseID string) {
	t.Helper()
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", dispatchV2BridgeSecret)
	srv, ts, st := newTestServer(t)
	srv.PlatformAPIToken = []byte(dispatchV2PlatformToken)
	srv.VerifierToken = []byte(missionAcceptanceVerifierToken)

	ctx := context.Background()
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := st.GrantLease(ctx, w.ID, "a", "wrkr_77777777777777777777777777777777", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return ts.URL, w.ID, lease.ID
}

func recordPDR(t *testing.T, base, workID, contextID, pdrID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"execution_context_id": contextID,
		"execution_pdr_id":     pdrID,
	})
	req, err := http.NewRequest(http.MethodPost, base+"/v1/works/"+workID+"/evidence", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dispatchV2PlatformToken)
	req.Header.Set("X-Works-Platform-Bridge", dispatchV2BridgeSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("record pdr status=%d", resp.StatusCode)
	}
}

func postMissionAcceptance(
	t *testing.T,
	base, workID, executionID string,
	body map[string]any,
	verifierToken string,
) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(
		http.MethodPost,
		base+"/v2/works/"+workID+"/acceptances/"+url.PathEscape(executionID)+"/mission-acceptance",
		bytes.NewReader(raw),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dispatchV2PlatformToken)
	req.Header.Set("X-Works-Platform-Bridge", dispatchV2BridgeSecret)
	if verifierToken != "" {
		req.Header.Set("X-WORKS-Verifier-Token", verifierToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestMissionAcceptanceV2PersistsCurrentExactSentinelSHIP(t *testing.T) {
	base, workID, leaseID := setupMissionAcceptanceV2(t)
	resp := postDispatchV2(t, base, workID, dispatchV2Fixture(leaseID))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dispatch status=%d", resp.StatusCode)
	}
	var accepted struct {
		WorksExecutionID string                   `json:"works_execution_id"`
		ExecutionContext executioncontext.Context `json:"execution_context"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}

	pdrID := "pdr_88888888888888888888888888888888"
	recordPDR(t, base, workID, accepted.ExecutionContext.ID, pdrID)

	subjectResp := bindSubjectV2(
		t,
		base,
		workID,
		accepted.WorksExecutionID,
		subjectBody(dispatchV2Subject),
	)
	defer subjectResp.Body.Close()
	if subjectResp.StatusCode != http.StatusOK {
		t.Fatalf("bind subject status=%d", subjectResp.StatusCode)
	}

	body := map[string]any{
		"schema":               "dispatch.mission-acceptance/1.0",
		"execution_context_id": accepted.ExecutionContext.ID,
		"execution_pdr_id":     pdrID,
		"verifier_id":          "sentinel:exact-head",
		"sentinel_head_sha":    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"sentinel_verdict":     "SHIP",
		"sentinel_receipt_id":  strings.Repeat("a", 64),
	}
	final := postMissionAcceptance(t, base, workID, accepted.WorksExecutionID, body, missionAcceptanceVerifierToken)
	defer final.Body.Close()
	if final.StatusCode != http.StatusOK {
		t.Fatalf("mission acceptance status=%d", final.StatusCode)
	}
	var out struct {
		Verified            bool   `json:"verified"`
		Outcome             string `json:"outcome"`
		VerificationSubject string `json:"verification_subject"`
		EvidenceRef         string `json:"evidence_ref"`
	}
	if err := json.NewDecoder(final.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Verified || out.Outcome != "SUCCEEDED" ||
		out.VerificationSubject != dispatchV2Subject ||
		out.EvidenceRef != "sentinel.receipt:"+body["sentinel_receipt_id"].(string) {
		t.Fatalf("bad mission acceptance: %+v", out)
	}

	// Exact replay of the same independent evidence is idempotent.
	replay := postMissionAcceptance(t, base, workID, accepted.WorksExecutionID, body, missionAcceptanceVerifierToken)
	defer replay.Body.Close()
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d", replay.StatusCode)
	}
}

func TestMissionAcceptanceV2FailsClosedOnStaleHeadOrMissingVerifierCredential(t *testing.T) {
	base, workID, leaseID := setupMissionAcceptanceV2(t)
	executionID, ec := acceptV2(t, base, workID, leaseID)
	pdrID := "pdr_88888888888888888888888888888888"
	recordPDR(t, base, workID, ec.ID, pdrID)

	subjectResp := bindSubjectV2(t, base, workID, executionID, subjectBody(dispatchV2Subject))
	defer subjectResp.Body.Close()
	if subjectResp.StatusCode != http.StatusOK {
		t.Fatalf("bind subject status=%d", subjectResp.StatusCode)
	}

	body := map[string]any{
		"schema":               "dispatch.mission-acceptance/1.0",
		"execution_context_id": ec.ID,
		"execution_pdr_id":     pdrID,
		"verifier_id":          "sentinel:exact-head",
		"sentinel_head_sha":    "cccccccccccccccccccccccccccccccccccccccc",
		"sentinel_verdict":     "SHIP",
		"sentinel_receipt_id":  strings.Repeat("b", 64),
	}
	stale := postMissionAcceptance(t, base, workID, executionID, body, missionAcceptanceVerifierToken)
	defer stale.Body.Close()
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale status=%d want=409", stale.StatusCode)
	}

	body["sentinel_head_sha"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	unauth := postMissionAcceptance(t, base, workID, executionID, body, "")
	defer unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing verifier credential status=%d want=401", unauth.StatusCode)
	}
}
