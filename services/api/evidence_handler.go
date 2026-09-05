package api

import (
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"errors"
	"net/http"
	"strings"

	"github.com/JonasAbde/works-execution/services/evidence"
	"github.com/JonasAbde/works-execution/services/work/store"
)

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
		KeyID:    s.EvidenceConfig.KeyID,
		HMACKey:  s.EvidenceConfig.HMACKey,
		Runner:   s.EvidenceConfig.Runner,
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
	writeJSON(w, http.StatusOK, map[string]any{
		"bundle":            bundle,
		"evidence_verdicts": verdicts,
	})
}