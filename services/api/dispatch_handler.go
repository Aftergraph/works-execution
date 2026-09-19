package api

// HTTP surface for the Runtime -> WORKS dispatch acceptance seam
// (contract:dispatch.acceptance/1.0). This is the production carrier of the
// correlation identity that internal/dispatch mints at accept time: WORKS owns
// the mint, Runtime adopts it, and this route is where "Runtime sent" becomes
// "WORKS owns" over the wire.
//
// The surface is additive and fail-closed, exactly like the Brain, Evidence and
// Link surfaces. The load-bearing gate is CurrentEpoch: Acceptor.Accept
// compares the client-asserted dispatch.authority_epoch against a server-owned
// current epoch, and WORKS has no authority-epoch resolver today. Defaulting
// that resolver permissively (for example to the client's own epoch) would turn
// the staleness guard into a silent no-op, which the platform's "visible, never
// silent" law forbids. So an unwired resolver closes the surface with 503
// instead of pretending to enforce freshness it cannot.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// DispatchConfig wires the dispatch acceptance seam. Both fields must be set
// for the surface to serve; either one missing answers 503 dispatch_accept_unavailable.
type DispatchConfig struct {
	// Acceptor is the durable dispatch seal, backed by the shared WORKS
	// database (never an independent source of execution truth).
	Acceptor *dispatch.Acceptor
	// CurrentEpoch returns the server's authoritative current authority epoch.
	// Nil means WORKS has no authority-epoch resolver, and the surface refuses
	// every request rather than running the staleness guard against a fiction.
	CurrentEpoch func() int64
}

// dispatchAcceptRequest is the Runtime-built dispatch envelope, mapped 1:1 to
// the required fields of contract:dispatch.acceptance/1.0. The correlation
// identities are deliberately absent from this struct: WORKS mints
// execution_context_id and trace_id at accept and clients cannot choose either
// field, so decoding with DisallowUnknownFields rejects any injection attempt
// at the boundary instead of silently discarding it.
type dispatchAcceptRequest struct {
	MissionID           string `json:"mission_id"`
	AuthorityRef        string `json:"authority_ref"`
	AuthorityEpoch      int64  `json:"authority_epoch"`
	RuntimeDispatchID   string `json:"runtime_dispatch_id"`
	AttemptID           string `json:"attempt_id"`
	EffectID            string `json:"effect_id"`
	IdempotencyKey      string `json:"idempotency_key"`
	BudgetRef           string `json:"budget_ref"`
	BudgetCeiling       int64  `json:"budget_ceiling"`
	CheckpointID        string `json:"checkpoint_id"`
	EvidenceRoot        string `json:"evidence_root"`
	VerificationSubject string `json:"verification_subject"`
	CausalID            string `json:"causal_id"`
}

// dispatchVerdictDTO is the verdict sub-object of
// contract:dispatch.acceptance/1.0. The contract pins this object to
// additionalProperties:false with all four fields required, so the wire shape
// is stated explicitly rather than inherited from the domain struct.
type dispatchVerdictDTO struct {
	Result      string `json:"result"`
	Subject     string `json:"subject"`
	EvidenceRef string `json:"evidence_ref"`
	RecordedAt  string `json:"recorded_at"`
}

// dispatchAcceptanceDTO is the wire shape of contract:dispatch.acceptance/1.0.
// The domain Acceptance carries no JSON tags (it is persisted as an opaque blob
// keyed by idempotency key), so the HTTP surface maps it field by field to the
// frozen snake_case names instead of leaking Go field names onto the wire.
type dispatchAcceptanceDTO struct {
	WorksExecutionID    string              `json:"works_execution_id"`
	MissionID           string              `json:"mission_id"`
	AuthorityRef        string              `json:"authority_ref"`
	AuthorityEpoch      int64               `json:"authority_epoch"`
	RuntimeDispatchID   string              `json:"runtime_dispatch_id"`
	AttemptID           string              `json:"attempt_id"`
	EffectID            string              `json:"effect_id"`
	IdempotencyKey      string              `json:"idempotency_key"`
	BudgetRef           string              `json:"budget_ref"`
	BudgetCeiling       int64               `json:"budget_ceiling"`
	CheckpointID        string              `json:"checkpoint_id"`
	EvidenceRoot        string              `json:"evidence_root"`
	VerificationSubject string              `json:"verification_subject"`
	CausalID            string              `json:"causal_id"`
	Outcome             string              `json:"outcome"`
	Verified            bool                `json:"verified"`
	VerifierID          string              `json:"verifier_id,omitempty"`
	Verdict             *dispatchVerdictDTO `json:"verdict,omitempty"`
	ExecutionContextID  string              `json:"execution_context_id"`
	TraceID             string              `json:"trace_id"`
}

func dispatchAcceptanceToDTO(a *dispatch.Acceptance) dispatchAcceptanceDTO {
	out := dispatchAcceptanceDTO{
		WorksExecutionID:    a.WorksExecutionID,
		MissionID:           a.Dispatch.MissionID,
		AuthorityRef:        a.Dispatch.AuthorityRef,
		AuthorityEpoch:      a.Dispatch.AuthorityEpoch,
		RuntimeDispatchID:   a.Dispatch.RuntimeDispatchID,
		AttemptID:           a.Dispatch.AttemptID,
		EffectID:            a.Dispatch.EffectID,
		IdempotencyKey:      a.Dispatch.IdempotencyKey,
		BudgetRef:           a.Dispatch.BudgetRef,
		BudgetCeiling:       a.Dispatch.BudgetCeiling,
		CheckpointID:        a.Dispatch.CheckpointID,
		EvidenceRoot:        a.Dispatch.EvidenceRoot,
		VerificationSubject: a.Dispatch.VerificationSubj,
		CausalID:            a.Dispatch.CausalID,
		Outcome:             a.Outcome,
		Verified:            a.Verified,
		VerifierID:          a.VerifierID,
		ExecutionContextID:  a.ExecutionContextID,
		TraceID:             a.TraceID,
	}
	if a.Verdict != nil {
		out.Verdict = &dispatchVerdictDTO{
			Result:      a.Verdict.Result,
			Subject:     a.Verdict.Subject,
			EvidenceRef: a.Verdict.EvidenceRef,
			RecordedAt:  a.Verdict.RecordedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return out
}

// acceptDispatch serves POST /v1/works/{id}/accept.
//
// Errors:
//
//	400 — non-POST body decode failure, unknown field (including an injected
//	      correlation identity), or a dispatch missing its binding
//	404 — the scoped work does not exist
//	405 — non-POST method
//	409 — stale authority epoch, or an idempotency key already bound to a
//	      different causal identity
//	503 — surface not configured (no Acceptor, or no authority-epoch resolver)
//	500 — store failure
//
// The response is 200 for BOTH a fresh acceptance and an idempotent replay of
// the same key. The seal deliberately does not distinguish them (a replay
// returns the winning record, correlation identities included), so the surface
// does not invent a 201-vs-200 distinction it cannot substantiate.
func (s *Server) acceptDispatch(w http.ResponseWriter, r *http.Request, workID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	if workID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	// Fail-closed before touching the body: an unconfigured surface must not
	// reveal anything about the dispatch it would have accepted.
	if s.Dispatch == nil || s.Dispatch.Acceptor == nil || s.Dispatch.CurrentEpoch == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatch_accept_unavailable",
			"dispatch acceptance surface not configured: no authority-epoch resolver")
		return
	}

	var req dispatchAcceptRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	// The seal is idempotency-keyed, not work-keyed, so the {id} in the route is
	// routing scope. Validating existence keeps an accept from orphaning a
	// dispatch against a work that was never created.
	if _, err := s.Store.GetWork(r.Context(), workID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", workID)
			return
		}
		s.logf("dispatch accept: load work %s: %v", workID, err)
		writeError(w, http.StatusInternalServerError, "work_read_failed", "could not load work")
		return
	}

	acc, err := s.Dispatch.Acceptor.Accept(dispatch.Dispatch{
		MissionID:         req.MissionID,
		AuthorityRef:      req.AuthorityRef,
		AuthorityEpoch:    req.AuthorityEpoch,
		RuntimeDispatchID: req.RuntimeDispatchID,
		AttemptID:         req.AttemptID,
		EffectID:          req.EffectID,
		IdempotencyKey:    req.IdempotencyKey,
		BudgetRef:         req.BudgetRef,
		BudgetCeiling:     req.BudgetCeiling,
		CheckpointID:      req.CheckpointID,
		EvidenceRoot:      req.EvidenceRoot,
		VerificationSubj:  req.VerificationSubject,
		CausalID:          req.CausalID,
	}, s.Dispatch.CurrentEpoch())
	if err != nil {
		switch {
		case errors.Is(err, dispatch.ErrMissingBinding):
			writeError(w, http.StatusBadRequest, "dispatch_missing_binding", err.Error())
		case errors.Is(err, dispatch.ErrStaleAuthority):
			writeError(w, http.StatusConflict, "dispatch_stale_authority", err.Error())
		case errors.Is(err, dispatch.ErrCausalMismatch):
			writeError(w, http.StatusConflict, "dispatch_causal_mismatch", err.Error())
		default:
			s.logf("dispatch accept: %v", err)
			writeError(w, http.StatusInternalServerError, "dispatch_accept_failed", "dispatch acceptance failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, dispatchAcceptanceToDTO(acc))
}
