package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	"github.com/JonasAbde/works-execution/services/work/store"
)

var missionAcceptanceReceiptID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var missionAcceptanceGitSubject = regexp.MustCompile(`^git:[A-Za-z0-9._-]+/[A-Za-z0-9._-]+@([a-f0-9]{40})$`)

type missionAcceptanceV2Request struct {
	Schema             string `json:"schema"`
	ExecutionContextID string `json:"execution_context_id"`
	ExecutionPDRID     string `json:"execution_pdr_id"`
	VerifierID         string `json:"verifier_id"`
	SentinelHeadSHA    string `json:"sentinel_head_sha"`
	SentinelVerdict    string `json:"sentinel_verdict"`
	SentinelReceiptID  string `json:"sentinel_receipt_id"`
}

type missionAcceptanceV2Response struct {
	Schema              string `json:"schema"`
	WorkID              string `json:"work_id"`
	WorksExecutionID    string `json:"works_execution_id"`
	ExecutionContextID  string `json:"execution_context_id"`
	ExecutionPDRID      string `json:"execution_pdr_id"`
	VerificationSubject string `json:"verification_subject"`
	Verified            bool   `json:"verified"`
	VerifierID          string `json:"verifier_id"`
	EvidenceRef         string `json:"evidence_ref"`
	Outcome             string `json:"outcome"`
}

type missionAcceptanceCorrelationReader interface {
	GetExecutionPolicyCorrelation(context.Context, string) (*store.ExecutionPolicyCorrelation, error)
}

// recordMissionAcceptanceV2 is the final WORKS-owned P2 acceptance boundary.
// It does not trust a client assertion that a subject is current. The request
// must arrive through both platform and verifier credentials, the execution PDR
// must already be durably correlated to the exact execution context, and the
// Sentinel head must equal WORKS' immutable post-effect subject binding.
func (s *Server) recordMissionAcceptanceV2(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	worksExecutionID := r.PathValue("execution")
	if workID == "" || worksExecutionID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work and execution ids are required")
		return
	}
	if !s.authorizeDispatchV2Platform(w, r) {
		return
	}
	if !s.verifierTokenConfigured() || !s.verifierTokenOK(r) {
		writeError(w, http.StatusUnauthorized, "verification_token_invalid", "verification credential invalid")
		return
	}

	var req missionAcceptanceV2Request
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Schema != "dispatch.mission-acceptance/1.0" ||
		req.ExecutionContextID == "" ||
		req.ExecutionPDRID == "" ||
		!strings.HasPrefix(req.VerifierID, "sentinel:") ||
		req.SentinelVerdict != "SHIP" ||
		!missionAcceptanceReceiptID.MatchString(req.SentinelReceiptID) {
		writeError(w, http.StatusBadRequest, "mission_acceptance_contract_violation", "invalid mission acceptance evidence")
		return
	}

	provider, ok := s.Store.(dispatchV2StoreProvider)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "dispatch_v2_unavailable", "store does not implement contextual dispatch acceptance")
		return
	}
	v2store := provider.DispatchAcceptanceV2Store(workID)
	acc, err := v2store.LoadByExecution(worksExecutionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mission_acceptance_read_failed", "could not read dispatch acceptance")
		return
	}
	if acc == nil || acc.ContractVersion != "dispatch.acceptance/2.0" || acc.WorkID != workID {
		writeError(w, http.StatusNotFound, "dispatch_acceptance_not_found", worksExecutionID)
		return
	}
	if acc.ExecutionContextID != req.ExecutionContextID {
		writeError(w, http.StatusConflict, "mission_acceptance_context_mismatch", "execution context mismatch")
		return
	}

	correlationReader, ok := s.Store.(missionAcceptanceCorrelationReader)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "mission_acceptance_correlation_unavailable", "execution policy correlation store unavailable")
		return
	}
	correlation, err := correlationReader.GetExecutionPolicyCorrelation(r.Context(), req.ExecutionContextID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mission_acceptance_correlation_failed", "could not read execution policy correlation")
		return
	}
	if correlation == nil ||
		correlation.WorkID != workID ||
		correlation.ExecutionContextID != req.ExecutionContextID ||
		correlation.ExecutionPDRID != req.ExecutionPDRID {
		writeError(w, http.StatusConflict, "mission_acceptance_pdr_mismatch", "execution PDR correlation mismatch")
		return
	}

	subjectStore, ok := v2store.(dispatch.VerificationSubjectStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "mission_acceptance_subject_unavailable", "verification subject store unavailable")
		return
	}
	binding, err := subjectStore.LoadVerificationSubject(r.Context(), worksExecutionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mission_acceptance_subject_read_failed", "could not read verification subject")
		return
	}
	if binding == nil ||
		binding.WorkID != workID ||
		binding.AttemptID != acc.Dispatch.AttemptID ||
		binding.EffectID != acc.Dispatch.EffectID ||
		binding.CausalID != acc.Dispatch.CausalID {
		writeError(w, http.StatusConflict, "mission_acceptance_subject_mismatch", "verification subject correlation mismatch")
		return
	}
	match := missionAcceptanceGitSubject.FindStringSubmatch(binding.Subject)
	if len(match) != 2 || match[1] != req.SentinelHeadSHA {
		writeError(w, http.StatusConflict, "mission_acceptance_stale_subject", "Sentinel head is not the bound exact subject")
		return
	}

	evidenceRef := "sentinel.receipt:" + req.SentinelReceiptID
	acceptor := dispatch.NewAcceptor(v2store, nil)
	err = acceptor.FinalizeVerifiedSuccess(
		worksExecutionID,
		req.VerifierID,
		binding.Subject,
		true,
		true,
		evidenceRef,
	)
	if err != nil {
		switch {
		case errors.Is(err, dispatch.ErrRevoked):
			writeError(w, http.StatusConflict, "mission_acceptance_revoked", err.Error())
		case errors.Is(err, dispatch.ErrStaleSubject):
			writeError(w, http.StatusConflict, "mission_acceptance_stale_subject", err.Error())
		case errors.Is(err, dispatch.ErrSelfVerification):
			writeError(w, http.StatusForbidden, "mission_acceptance_self_verification", err.Error())
		case errors.Is(err, dispatch.ErrVerdictConflict):
			writeError(w, http.StatusConflict, "mission_acceptance_conflict", err.Error())
		default:
			writeError(w, http.StatusConflict, "mission_acceptance_rejected", err.Error())
		}
		return
	}

	final, err := v2store.LoadByExecution(worksExecutionID)
	if err != nil || final == nil || !final.Verified ||
		final.Outcome != "SUCCEEDED" || final.Verdict == nil ||
		final.Verdict.Subject != binding.Subject ||
		final.Verdict.EvidenceRef != evidenceRef {
		writeError(w, http.StatusInternalServerError, "mission_acceptance_readback_failed", "durable mission acceptance readback failed")
		return
	}

	writeJSON(w, http.StatusOK, missionAcceptanceV2Response{
		Schema:              "dispatch.mission-acceptance/1.0",
		WorkID:              workID,
		WorksExecutionID:    worksExecutionID,
		ExecutionContextID:  req.ExecutionContextID,
		ExecutionPDRID:      req.ExecutionPDRID,
		VerificationSubject: binding.Subject,
		Verified:            final.Verified,
		VerifierID:          final.VerifierID,
		EvidenceRef:         final.Verdict.EvidenceRef,
		Outcome:             final.Outcome,
	})
}
