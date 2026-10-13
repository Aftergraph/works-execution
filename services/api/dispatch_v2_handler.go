package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	"github.com/JonasAbde/works-execution/packages/executioncontext"
)

type dispatchV2StoreProvider interface {
	DispatchAcceptanceV2Store(workID string) dispatch.V2Store
}

type dispatchPlacementBinding struct {
	Schema                  string `json:"schema"`
	MissionID               string `json:"mission_id"`
	SelectedNode            string `json:"selected_node"`
	PlacementDecisionDigest string `json:"placement_decision_digest"`
	SnapshotDigest          string `json:"snapshot_digest"`
	PolicyVersion           string `json:"policy_version"`
}

type dispatchV2Request struct {
	Schema              string `json:"schema"`
	ActionID string `json:"action_id"`
	EffectDigest string `json:"effect_digest"`
	OrganizationID      string `json:"organization_id"`
	TenantID            string `json:"tenant_id"`
	PrincipalID         string `json:"principal_id"`
	MissionID           string `json:"mission_id"`
	AuthorityLeaseID    string `json:"authority_lease_id"`
	WorkerLeaseID       string `json:"worker_lease_id"`
	AdmissionDecisionID string `json:"admission_decision_id"`
	RuntimeDispatchID   string `json:"runtime_dispatch_id"`
	AttemptID           string `json:"attempt_id"`
	EffectID            string `json:"effect_id"`
	IdempotencyKey      string `json:"idempotency_key"`
	BudgetRef           string `json:"budget_ref"`
	BudgetCeiling       int64  `json:"budget_ceiling"`
	CheckpointID        string `json:"checkpoint_id"`
	EvidenceRoot        string `json:"evidence_root"`
	CausalID            string                    `json:"causal_id"`
	PlacementBinding     *dispatchPlacementBinding `json:"placement_binding,omitempty"`
}

type dispatchV2Response struct {
	Schema           string                   `json:"schema"`
	WorkID           string                   `json:"work_id"`
	WorksExecutionID string                   `json:"works_execution_id"`
	Request          dispatchV2Request        `json:"request"`
	ExecutionContext executioncontext.Context `json:"execution_context"`
	Outcome          string                   `json:"outcome"`
	Verified         bool                     `json:"verified"`
	VerifierID       string                   `json:"verifier_id,omitempty"`
	Verdict          *dispatchVerdictDTO      `json:"verdict,omitempty"`
}

type dispatchSubjectV2Request struct {
	Schema    string `json:"schema"`
	AttemptID string `json:"attempt_id"`
	EffectID  string `json:"effect_id"`
	CausalID  string `json:"causal_id"`
	Subject   string `json:"subject"`
}

type dispatchSubjectV2Response struct {
	Schema           string `json:"schema"`
	WorkID           string `json:"work_id"`
	WorksExecutionID string `json:"works_execution_id"`
	AttemptID        string `json:"attempt_id"`
	EffectID         string `json:"effect_id"`
	CausalID         string `json:"causal_id"`
	Subject          string `json:"subject"`
	BoundAt          string `json:"bound_at"`
}

func (s *Server) authorizeDispatchV2Platform(w http.ResponseWriter, r *http.Request) bool {
	if len(s.PlatformAPIToken) < 32 {
		writeError(w, http.StatusServiceUnavailable, "platform_auth_unavailable", "platform API token not configured")
		return false
	}
	const bearerPrefix = "Bearer "
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, bearerPrefix) {
		writeError(w, http.StatusUnauthorized, "platform_auth_required", "platform Bearer token required")
		return false
	}
	gotToken := strings.TrimSpace(authz[len(bearerPrefix):])
	if gotToken == "" || subtle.ConstantTimeCompare([]byte(gotToken), s.PlatformAPIToken) != 1 {
		writeError(w, http.StatusUnauthorized, "platform_auth_failed", "invalid platform Bearer token")
		return false
	}
	bridgeSecret := bridgeSecretFromEnv()
	if !BridgeSecretConfigured(bridgeSecret) {
		writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "platform bridge not configured")
		return false
	}
	gotBridge := r.Header.Get("X-Works-Platform-Bridge")
	if gotBridge == "" || subtle.ConstantTimeCompare([]byte(gotBridge), []byte(bridgeSecret)) != 1 {
		writeError(w, http.StatusUnauthorized, "bridge_unauthorized", "missing or invalid platform bridge header")
		return false
	}
	return true
}

func (s *Server) acceptDispatchV2(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if workID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	if !s.authorizeDispatchV2Platform(w, r) {
		return
	}

	var req dispatchV2Request
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Schema != "dispatch.acceptance/2.0" || req.BudgetCeiling < 0 {
		writeError(w, http.StatusBadRequest, "dispatch_v2_contract_violation", "invalid dispatch.acceptance/2.0 request")
		return
	}


	if !actionIDPattern.MatchString(req.ActionID) || !effectDigestPatternV2.MatchString(req.EffectDigest) {
		writeError(w,http.StatusBadRequest,"dispatch_action_binding_invalid","action_id and effect_digest required")
		return
	}
	provider, ok := s.Store.(dispatchV2StoreProvider)
	if !ok {
		writeError(w,http.StatusServiceUnavailable,"dispatch_v2_unavailable","store does not implement contextual acceptance")
		return
	}
	vs := provider.DispatchAcceptanceV2Store(workID)
	prior,err:=vs.LoadByIdempotency(req.IdempotencyKey)
	if err!=nil{
		writeError(w,http.StatusServiceUnavailable,"dispatch_replay_unavailable","unable to inspect accepted Work")
		return
	}
	if prior!=nil {
		// Recovery is strictly read-only. An already committed winner can be
		// re-read after AIE revocation or lease expiry, but a different action
		// must NEVER reuse its idempotency identity.
		ec,err:=s.Store.GetExecutionContext(r.Context(),prior.ExecutionContextID)
		if err!=nil||!exactDispatchV2Replay(prior,ec,req,workID){
			writeError(w,http.StatusConflict,"dispatch_replay_identity_conflict","immutable accepted dispatch differs")
			return
		}
	} else {
		if s.DispatchV2Authority==nil {
			writeError(w,http.StatusServiceUnavailable,"dispatch_authority_unconfigured","authoritative AIE revalidation not configured")
			return
		}
		binding:=DispatchV2AuthorityRequest{
		 Schema:"aftergraph.works-dispatch-revalidation-request/1",
		 ActionID:req.ActionID,EffectDigest:req.EffectDigest,
		 OrganizationID:req.OrganizationID,TenantID:req.TenantID,PrincipalID:req.PrincipalID,
		 MissionID:req.MissionID,WorkID:workID,AuthorityLeaseID:req.AuthorityLeaseID,
		 WorkerLeaseID:req.WorkerLeaseID,RuntimeDispatchID:req.RuntimeDispatchID,
		 AttemptID:req.AttemptID,EffectID:req.EffectID,IdempotencyKey:req.IdempotencyKey,
		 BudgetCeiling:req.BudgetCeiling,
		}
		if err:=s.DispatchV2Authority.RevalidateDispatchV2(r.Context(),binding);err!=nil{
			writeError(w,http.StatusServiceUnavailable,"dispatch_authority_denied","live AIE authority unavailable or denied")
			return
		}
	}
	acceptor:=dispatch.NewAcceptor(vs,nil)
	accepted, ec, err := acceptor.AcceptV2(r.Context(), dispatch.Dispatch{
		MissionID:         req.MissionID,
		AuthorityRef:      req.AuthorityLeaseID,
		ActionID: req.ActionID,
		EffectDigest: req.EffectDigest,
		AuthorityEpoch:    0,
		RuntimeDispatchID: req.RuntimeDispatchID,
		AttemptID:         req.AttemptID,
		EffectID:          req.EffectID,
		IdempotencyKey:    req.IdempotencyKey,
		BudgetRef:         req.BudgetRef,
		BudgetCeiling:     req.BudgetCeiling,
		CheckpointID:      req.CheckpointID,
		EvidenceRoot:      req.EvidenceRoot,
		CausalID:          req.CausalID,
		Placement: func() *dispatch.PlacementBinding {
			if req.PlacementBinding == nil {
				return nil
			}
			return &dispatch.PlacementBinding{
				Schema:                  req.PlacementBinding.Schema,
				MissionID:               req.PlacementBinding.MissionID,
				SelectedNode:            req.PlacementBinding.SelectedNode,
				PlacementDecisionDigest: req.PlacementBinding.PlacementDecisionDigest,
				SnapshotDigest:          req.PlacementBinding.SnapshotDigest,
				PolicyVersion:           req.PlacementBinding.PolicyVersion,
			}
		}(),
	}, dispatch.V2Binding{
		WorkID:              workID,
		OrganizationID:      req.OrganizationID,
		TenantID:            req.TenantID,
		PrincipalID:         req.PrincipalID,
		AuthorityLeaseID:    req.AuthorityLeaseID,
		WorkerLeaseID:       req.WorkerLeaseID,
		AdmissionDecisionID: req.AdmissionDecisionID,
	})
	if err != nil {
		switch {
		case errors.Is(err, dispatch.ErrMissingBinding), errors.Is(err, dispatch.ErrContextBinding), errors.Is(err, dispatch.ErrPlacementBindingInvalid):
			writeError(w, http.StatusBadRequest, "dispatch_v2_binding_invalid", err.Error())
		case errors.Is(err, dispatch.ErrPlacementMismatch):
			writeError(w, http.StatusConflict, "placement_mismatch", err.Error())
		case errors.Is(err, dispatch.ErrWorkerLeaseUnavailable):
			writeError(w, http.StatusConflict, "worker_lease_unavailable", err.Error())
		case errors.Is(err, dispatch.ErrCausalMismatch):
			writeError(w, http.StatusConflict, "dispatch_causal_mismatch", err.Error())
		case errors.Is(err, dispatch.ErrV2StoreRequired):
			writeError(w, http.StatusServiceUnavailable, "dispatch_v2_unavailable", err.Error())
		default:
			s.logf("dispatch v2 accept: %v", err)
			writeError(w, http.StatusInternalServerError, "dispatch_v2_accept_failed", "dispatch v2 acceptance failed")
		}
		return
	}

	out := dispatchV2Response{
		Schema:           "dispatch.acceptance/2.0",
		WorkID:           workID,
		WorksExecutionID: accepted.WorksExecutionID,
		Request:          req,
		ExecutionContext: *ec,
		Outcome:          accepted.Outcome,
		Verified:         accepted.Verified,
		VerifierID:       accepted.VerifierID,
	}
	if accepted.Verdict != nil {
		out.Verdict = &dispatchVerdictDTO{
			Result:      accepted.Verdict.Result,
			Subject:     accepted.Verdict.Subject,
			EvidenceRef: accepted.Verdict.EvidenceRef,
			RecordedAt:  accepted.Verdict.RecordedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) bindDispatchVerificationSubjectV2(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	worksExecutionID := r.PathValue("execution")
	if workID == "" || worksExecutionID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work and execution ids are required")
		return
	}
	if !s.authorizeDispatchV2Platform(w, r) {
		return
	}

	var req dispatchSubjectV2Request
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Schema != "dispatch.verification-subject/1.0" {
		writeError(w, http.StatusBadRequest, "subject_binding_contract_violation", "unsupported subject binding schema")
		return
	}

	provider, ok := s.Store.(dispatchV2StoreProvider)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "dispatch_v2_unavailable", "store does not implement contextual dispatch acceptance")
		return
	}
	acceptor := dispatch.NewAcceptor(provider.DispatchAcceptanceV2Store(workID), nil)
	binding, err := acceptor.BindVerificationSubject(
		r.Context(),
		worksExecutionID,
		workID,
		req.AttemptID,
		req.EffectID,
		req.CausalID,
		req.Subject,
	)
	if err != nil {
		switch {
		case errors.Is(err, dispatch.ErrInvalidSubject):
			writeError(w, http.StatusBadRequest, "invalid_verification_subject", err.Error())
		case errors.Is(err, dispatch.ErrUnknownAcceptance):
			writeError(w, http.StatusNotFound, "dispatch_acceptance_not_found", err.Error())
		case errors.Is(err, dispatch.ErrCausalMismatch), errors.Is(err, dispatch.ErrSubjectConflict):
			writeError(w, http.StatusConflict, "verification_subject_conflict", err.Error())
		case errors.Is(err, dispatch.ErrV2StoreRequired):
			writeError(w, http.StatusServiceUnavailable, "dispatch_v2_unavailable", err.Error())
		default:
			s.logf("dispatch v2 subject bind: %v", err)
			writeError(w, http.StatusInternalServerError, "verification_subject_bind_failed", "verification subject binding failed")
		}
		return
	}

	writeJSON(w, http.StatusOK, dispatchSubjectV2Response{
		Schema:           "dispatch.verification-subject/1.0",
		WorkID:           binding.WorkID,
		WorksExecutionID: binding.WorksExecutionID,
		AttemptID:        binding.AttemptID,
		EffectID:         binding.EffectID,
		CausalID:         binding.CausalID,
		Subject:          binding.Subject,
		BoundAt:          binding.BoundAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}


// exactDispatchV2Replay permits only readback of the SAME durable winner,
// not re-execution or re-authorization. Used after operator+bridge checks.
func exactDispatchV2Replay(a *dispatch.Acceptance,c *executioncontext.Context,
 r dispatchV2Request,workID string)bool{
 if a==nil||c==nil||a.ContractVersion!="dispatch.acceptance/2.0"||
  a.WorkID!=workID||a.Dispatch.IdempotencyKey!=r.IdempotencyKey||
  a.Dispatch.ActionID!=r.ActionID||a.Dispatch.EffectDigest!=r.EffectDigest||
  a.Dispatch.AuthorityRef!=r.AuthorityLeaseID||
  a.Dispatch.MissionID!=r.MissionID||
  a.Dispatch.RuntimeDispatchID!=r.RuntimeDispatchID||
  a.Dispatch.AttemptID!=r.AttemptID||a.Dispatch.EffectID!=r.EffectID||
  a.Dispatch.CausalID!=r.CausalID||
  a.Dispatch.BudgetRef!=r.BudgetRef||
  a.Dispatch.BudgetCeiling!=r.BudgetCeiling||
  a.Dispatch.CheckpointID!=r.CheckpointID||
  a.Dispatch.EvidenceRoot!=r.EvidenceRoot||
  c.ID!=a.ExecutionContextID||c.WorkID!=workID||
  c.OrganizationID!=r.OrganizationID||c.TenantID!=r.TenantID||
  c.PrincipalID!=r.PrincipalID||c.AuthorityLeaseID!=r.AuthorityLeaseID||
  c.WorkerLeaseID!=r.WorkerLeaseID||
  c.AdmissionDecisionID!=r.AdmissionDecisionID{
  return false
 }
 p:=a.Dispatch.Placement
 q:=r.PlacementBinding
 if (p==nil)!=(q==nil){return false}
 if p!=nil && (p.Schema!=q.Schema||p.MissionID!=q.MissionID||
 p.SelectedNode!=q.SelectedNode||
 p.PlacementDecisionDigest!=q.PlacementDecisionDigest||
 p.SnapshotDigest!=q.SnapshotDigest||p.PolicyVersion!=q.PolicyVersion){
  return false
 }
 return true
}
