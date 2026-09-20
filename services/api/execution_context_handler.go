package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/services/work/store"
)

type createExecutionContextBody struct {
	OrganizationID      string `json:"organization_id"`
	TenantID            string `json:"tenant_id"`
	PrincipalID         string `json:"principal_id"`
	MissionID           string `json:"mission_id"`
	AuthorityLeaseID    string `json:"authority_lease_id"`
	WorkerLeaseID       string `json:"worker_lease_id"`
	AdmissionDecisionID string `json:"admission_decision_id"`
	TraceID             string `json:"trace_id"`
	PriorContextID      string `json:"prior_execution_context_id,omitempty"`
}

func (s *Server) createExecutionContext(w http.ResponseWriter, r *http.Request, workID string) {
	// Execution-context minting is a platform authority operation, not a
	// worker-owned mutation. Require the same dual server-to-server boundary
	// as execution-PDR correlation: a stable platform bearer plus the bridge
	// transport secret. Worker enrollment JWTs are never accepted here.
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

	var body createExecutionContextBody
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	created, err := s.Store.CreateExecutionContext(r.Context(), executioncontext.Context{
		OrganizationID: body.OrganizationID, TenantID: body.TenantID, PrincipalID: body.PrincipalID,
		MissionID: body.MissionID, AuthorityLeaseID: body.AuthorityLeaseID, WorkID: workID,
		WorkerLeaseID: body.WorkerLeaseID, AdmissionDecisionID: body.AdmissionDecisionID,
		TraceID: body.TraceID, PriorContextID: body.PriorContextID,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrExecutionContextLeaseMismatch):
			writeError(w, http.StatusNotFound, "execution_context_not_available", "execution context inputs do not match an admitted work lease")
		case errors.Is(err, store.ErrExecutionContextConflict):
			writeError(w, http.StatusConflict, "execution_context_conflict", err.Error())
		default:
			writeError(w, http.StatusBadRequest, "execution_context_invalid", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) executionContextItemHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/execution-contexts/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
		return
	}
	ctx, err := s.Store.GetExecutionContext(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", id)
			return
		}
		writeError(w, http.StatusInternalServerError, "execution_context_read_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ctx)
}
