package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/evidence"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// projectOutcomeVerification returns pending only when the lookup yields no
// record; otherwise it projects the stored verifier record with its
// provenance. A lookup error fails closed to evidence_failed — a store fault
// is not a verdict. The lookup is injected (production: Store method value)
// so the error path stays unit-testable without stubbing the whole Store.
func projectOutcomeVerification(
	ctx context.Context,
	lookup func(context.Context, string) (*store.VerificationVerdict, error),
	workID string,
) (outcomeVerificationProjection, error) {
	v, err := lookup(ctx, workID)
	if err != nil {
		return outcomeVerificationProjection{}, err
	}
	if v == nil {
		return outcomeVerificationProjection{Status: "pending"}, nil
	}
	return outcomeVerificationProjection{
		Status:      v.Result,
		VerifierID:  v.VerifierID,
		EvidenceRef: v.EvidenceRef,
		VerifiedAt:  v.VerifiedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// outcomeVerificationProjection is deliberately separate from the evidence
// integrity verdicts returned by workgraph.VerifyEvidence. Executor state and
// hash-valid evidence are inputs to a verifier, not a verifier decision.
// Until a durable independent verdict exists, the only truthful projection is
// pending. Optional provenance fields are omitted rather than invented.
type outcomeVerificationProjection struct {
	Status      string `json:"status"`
	VerifierID  string `json:"verifier_id,omitempty"`
	EvidenceRef string `json:"evidence_ref,omitempty"`
	VerifiedAt  string `json:"verified_at,omitempty"`
}

type platformVerificationProjection struct {
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	OutcomeStatus string `json:"outcome_status,omitempty"`
}

var executionContextIDRE = regexp.MustCompile("^ctx_[a-f0-9]{32}$")
var executionPDRIDRE = regexp.MustCompile("^pdr_[a-f0-9]{32}$")

type executionPolicyDecisionRef struct {
	ExecutionContextID string `json:"execution_context_id"`
	ExecutionPDRID     string `json:"execution_pdr_id"`
}

func executionPolicyEvidenceID(workID, contextID, pdrID string) string {
	sum := sha256.Sum256([]byte(workID + "\x00" + contextID + "\x00" + pdrID))
	return "evd_" + hex.EncodeToString(sum[:])[:32]
}

func executionPolicyBindingMAC(secret, workID, contextID, pdrID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(workID + "\x00" + contextID + "\x00" + pdrID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) recordExecutionPolicyDecision(w http.ResponseWriter, r *http.Request, workID string) {
	if len(s.PlatformAPIToken) < 32 {
		writeError(w, http.StatusServiceUnavailable, "platform_auth_unavailable", "platform API token not configured")
		return
	}
	const bearerPrefix = "Bearer "
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, bearerPrefix) {
		writeError(w, http.StatusUnauthorized, "platform_auth_required", "platform Bearer token required")
		return
	}
	gotToken := strings.TrimSpace(authz[len(bearerPrefix):])
	if gotToken == "" || subtle.ConstantTimeCompare([]byte(gotToken), s.PlatformAPIToken) != 1 {
		writeError(w, http.StatusUnauthorized, "platform_auth_failed", "invalid platform Bearer token")
		return
	}

	bridgeSecret := bridgeSecretFromEnv()
	if !BridgeSecretConfigured(bridgeSecret) {
		writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "platform bridge not configured")
		return
	}
	gotBridge := r.Header.Get("X-Works-Platform-Bridge")
	if gotBridge == "" || subtle.ConstantTimeCompare([]byte(gotBridge), []byte(bridgeSecret)) != 1 {
		writeError(w, http.StatusUnauthorized, "bridge_unauthorized", "missing or invalid platform bridge header")
		return
	}

	var body executionPolicyDecisionRef
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !executionContextIDRE.MatchString(body.ExecutionContextID) || !executionPDRIDRE.MatchString(body.ExecutionPDRID) {
		writeError(w, http.StatusBadRequest, "invalid_platform_reference", "canonical execution context and PDR ids required")
		return
	}

	ctxRecord, err := s.Store.GetExecutionContext(r.Context(), body.ExecutionContextID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "execution_context_not_found", body.ExecutionContextID)
			return
		}
		writeError(w, http.StatusInternalServerError, "execution_context_lookup_failed", err.Error())
		return
	}
	if ctxRecord.WorkID != workID {
		writeError(w, http.StatusConflict, "execution_context_work_mismatch", "execution context belongs to another work")
		return
	}

	wk, err := s.Store.GetWork(r.Context(), workID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", workID)
			return
		}
		writeError(w, http.StatusInternalServerError, "work_lookup_failed", err.Error())
		return
	}
	for _, ev := range wk.Evidence {
		if ev.Type != "policy" || ev.Details == nil {
			continue
		}
		kind, _ := ev.Details["record_kind"].(string)
		ctxID, _ := ev.Details["execution_context_id"].(string)
		pdrID, _ := ev.Details["execution_pdr_id"].(string)
		if kind == "execution_policy_decision" && ctxID == body.ExecutionContextID {
			if pdrID != body.ExecutionPDRID {
				writeError(w, http.StatusConflict, "execution_pdr_conflict", "execution context already correlated to another PDR")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "already_recorded",
				"evidence_id": ev.ID,
				"execution_context_id": body.ExecutionContextID,
				"execution_pdr_id": body.ExecutionPDRID,
			})
			return
		}
	}

	ev := workgraph.Evidence{
		ID:         executionPolicyEvidenceID(workID, body.ExecutionContextID, body.ExecutionPDRID),
		Type:       "policy",
		Result:     "pass",
		RecordedAt: time.Now().UTC(),
		Signer:     "trust-gateway",
		Details: map[string]any{
			"record_kind":          "execution_policy_decision",
			"execution_context_id": body.ExecutionContextID,
			"execution_pdr_id":     body.ExecutionPDRID,
			"binding_hmac":         executionPolicyBindingMAC(bridgeSecret, workID, body.ExecutionContextID, body.ExecutionPDRID),
		},
	}
	if _, err := s.Store.AppendEvidence(r.Context(), workID, ev); err != nil {
		writeError(w, http.StatusInternalServerError, "evidence_record_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "recorded",
		"evidence_id": ev.ID,
		"execution_context_id": body.ExecutionContextID,
		"execution_pdr_id": body.ExecutionPDRID,
	})
}

func projectPlatformVerification(bundle *evidence.Bundle, outcome outcomeVerificationProjection) *platformVerificationProjection {
	if bundle == nil || bundle.PlatformVerification == nil {
		return nil
	}
	if bundle.PlatformVerification.Status == "provenance_gap" {
		return &platformVerificationProjection{
			Status: "provenance_gap",
			Reason: bundle.PlatformVerification.Reason,
			OutcomeStatus: outcome.Status,
		}
	}
	if bundle.PlatformVerification.Status != "correlated" {
		return &platformVerificationProjection{Status: "provenance_gap", Reason: "invalid_platform_correlation"}
	}
	switch outcome.Status {
	case "passed":
		return &platformVerificationProjection{Status: "verified", OutcomeStatus: outcome.Status}
	case "failed":
		return &platformVerificationProjection{Status: "failed", OutcomeStatus: outcome.Status}
	default:
		return &platformVerificationProjection{Status: "pending", OutcomeStatus: outcome.Status}
	}
}

// workEvidenceHandler implements GET /v1/works/{id}/evidence.
//
// It produces an evidence.Bundle from the durable Work state and returns
// it as JSON. The bundle is content-addressed (bundle_id = "evb_" +
// sha256(canonicalJSON)[:32hex]) and carries a single HMAC-SHA256
// signature over the canonical JSON.
//
// Errors:
//   400 — missing id
//   404 — work not found
//   409 — work is not in a terminal state (cannot bundle mid-execution)
//   405 — non-GET method
//   503 — EvidenceConfig not configured on the server
//   500 — store / canonicalize failure
func (s *Server) workEvidenceHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	workID := parts[0]
	if r.Method == http.MethodPost {
		s.recordExecutionPolicyDecision(w, r, workID)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}

	if s.EvidenceConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "evidence_unavailable", "evidence producer not configured")
		return
	}

	cfg := evidence.ProducerConfig{
		KeyID:                s.EvidenceConfig.KeyID,
		HMACKey:              s.EvidenceConfig.HMACKey,
		PlatformBridgeSecret: []byte(bridgeSecretFromEnv()),
		Runner:               s.EvidenceConfig.Runner,
	}

	bundle, err := evidence.Produce(r.Context(), s.Store, workID, cfg)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", workID)
		case errors.Is(err, evidence.ErrWorkNotTerminal):
			writeError(w, http.StatusConflict, "work_not_terminal", err.Error())
		default:
			s.logf("evidence produce: %v", err)
			writeError(w, http.StatusInternalServerError, "evidence_failed", err.Error())
		}
		return
	}

	// G5: per-evidence integrity-verdict (workgraph.VerifyEvidence — G1 kilde
	// til sandhed). Non-breaking: feltet er additivt på bundle-svaret.
	// Tampered/unsealed ALDRIG skjult (F2-loven).
	wk, werr := s.Store.GetWork(r.Context(), workID)
	verdicts := map[string]string{}
	if werr == nil && wk != nil {
		for _, ev := range wk.Evidence {
			verdicts[ev.ID] = workgraph.VerifyEvidence(ev)
		}
	}

	w.Header().Set("ETag", `"`+bundle.BundleID+`"`)

	// G5: non-breaking — bundle-felterne forbliver på TOP-niveau (eksisterende
	// klient-kontrakt), evidence_verdicts tilføjes som ekstra felt.
	merged, err2 := json.Marshal(bundle)
	if err2 != nil {
		s.logf("evidence marshal: %v", err2)
		writeError(w, http.StatusInternalServerError, "evidence_failed", err2.Error())
		return
	}
	var out map[string]any
	if err := json.Unmarshal(merged, &out); err != nil {
		s.logf("evidence unmarshal: %v", err)
		writeError(w, http.StatusInternalServerError, "evidence_failed", err.Error())
		return
	}
	out["evidence_verdicts"] = verdicts
	ov, verr := projectOutcomeVerification(r.Context(), s.Store.GetVerificationVerdict, workID)
	if verr != nil {
		s.logf("outcome verdict lookup: %v", verr)
		writeError(w, http.StatusInternalServerError, "evidence_failed", verr.Error())
		return
	}
	out["outcome_verification"] = ov
	if pv := projectPlatformVerification(bundle, ov); pv != nil {
		// Keep the signed bundle's platform_verification field untouched.
		// This response-only projection combines signed provenance completeness
		// with the independent verifier verdict without invalidating bundle_id/HMAC.
		out["platform_outcome_verification"] = pv
	}
	writeJSON(w, http.StatusOK, out)
}
