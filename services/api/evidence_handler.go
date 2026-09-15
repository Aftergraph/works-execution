package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	workID := parts[0]

	if s.EvidenceConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "evidence_unavailable", "evidence producer not configured")
		return
	}

	cfg := evidence.ProducerConfig{
		KeyID:   s.EvidenceConfig.KeyID,
		HMACKey: s.EvidenceConfig.HMACKey,
		Runner:  s.EvidenceConfig.Runner,
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
	writeJSON(w, http.StatusOK, out)
}
