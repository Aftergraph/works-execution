package api

import (
	"context"
	"encoding/json"
	"net/http"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/JonasAbde/works-execution/services/runner"
)

const githubActionsOIDCIssuer = "https://token.actions.githubusercontent.com"

// GitHubActionsOIDCClaims are the signed claims WORKS relies on for
// bootstrap enrollment. Keep this set deliberately small: every accepted
// field is an authority input and therefore must be verified, not inferred.
type GitHubActionsOIDCClaims struct {
	Repository        string `json:"repository"`
	RepositoryID      string `json:"repository_id"`
	RepositoryOwner   string `json:"repository_owner"`
	Ref               string `json:"ref"`
	SHA               string `json:"sha"`
	WorkflowRef       string `json:"workflow_ref"`
	WorkflowSHA       string `json:"workflow_sha"`
	RunnerEnvironment string `json:"runner_environment"`
	Actor             string `json:"actor"`
	EventName         string `json:"event_name"`
}

// GitHubActionsOIDCVerifier is injectable so route tests never depend on
// GitHub/network state. Production uses RemoteGitHubActionsOIDCVerifier.
type GitHubActionsOIDCVerifier interface {
	Verify(ctx context.Context, raw string) (GitHubActionsOIDCClaims, error)
}

// RemoteGitHubActionsOIDCVerifier verifies GitHub Actions ID tokens using
// GitHub's OIDC discovery document and rotating public keys.
type RemoteGitHubActionsOIDCVerifier struct {
	Audience string
}

func (v RemoteGitHubActionsOIDCVerifier) Verify(ctx context.Context, raw string) (GitHubActionsOIDCClaims, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return GitHubActionsOIDCClaims{}, errors.New("oidc token required")
	}
	if strings.TrimSpace(v.Audience) == "" {
		return GitHubActionsOIDCClaims{}, errors.New("oidc audience not configured")
	}
	provider, err := oidc.NewProvider(ctx, githubActionsOIDCIssuer)
	if err != nil {
		return GitHubActionsOIDCClaims{}, fmt.Errorf("github oidc discovery: %w", err)
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: v.Audience}).Verify(ctx, raw)
	if err != nil {
		return GitHubActionsOIDCClaims{}, fmt.Errorf("github oidc verify: %w", err)
	}
	var claims GitHubActionsOIDCClaims
	if err := idToken.Claims(&claims); err != nil {
		return GitHubActionsOIDCClaims{}, fmt.Errorf("github oidc claims: %w", err)
	}
	return claims, nil
}

// GitHubActionsOIDCPolicy is a strict allow-list. Empty required fields do
// not mean wildcard: Validate rejects an incomplete policy fail-closed.
type GitHubActionsOIDCPolicy struct {
	Repository        string
	RepositoryID      string
	Ref               string
	WorkflowRef       string
	RunnerEnvironment string
	EventName         string
}

func (p GitHubActionsOIDCPolicy) Validate(c GitHubActionsOIDCClaims) error {
	if strings.TrimSpace(p.Repository) == "" || strings.TrimSpace(p.RepositoryID) == "" || strings.TrimSpace(p.Ref) == "" ||
		strings.TrimSpace(p.WorkflowRef) == "" || strings.TrimSpace(p.RunnerEnvironment) == "" || strings.TrimSpace(p.EventName) == "" {
		return errors.New("github oidc policy incomplete")
	}
	if c.Repository != p.Repository {
		return errors.New("github oidc repository rejected")
	}
	if c.RepositoryID != p.RepositoryID {
		return errors.New("github oidc repository_id rejected")
	}
	if c.Ref != p.Ref {
		return errors.New("github oidc ref rejected")
	}
	if c.WorkflowRef != p.WorkflowRef {
		return errors.New("github oidc workflow_ref rejected")
	}
	if c.RunnerEnvironment != p.RunnerEnvironment {
		return errors.New("github oidc runner_environment rejected")
	}
	if c.EventName != p.EventName {
		return errors.New("github oidc event_name rejected")
	}
	return nil
}

type githubOIDCEnrollmentReq struct {
	WorkerID   string `json:"worker_id"`
	OIDCToken  string `json:"oidc_token"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

func (s *Server) githubOIDCEnrollHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	if s.GitHubOIDCVerifier == nil || s.GitHubOIDCPolicy == nil {
		writeError(w, http.StatusServiceUnavailable, "github_oidc_enrollment_disabled", "GitHub Actions OIDC enrollment is not configured")
		return
	}
	var req githubOIDCEnrollmentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	if !validWorkerID(req.WorkerID) {
		writeError(w, http.StatusBadRequest, "invalid_worker_id", "worker_id must match "+runner.RunnerIDPatternSource)
		return
	}
	claims, err := s.GitHubOIDCVerifier.Verify(r.Context(), req.OIDCToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "github_oidc_invalid", "GitHub Actions identity rejected")
		return
	}
	if err := s.GitHubOIDCPolicy.Validate(claims); err != nil {
		writeError(w, http.StatusForbidden, "github_oidc_policy_rejected", err.Error())
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	tok, err := s.Auth.Mint(r.Context(), req.WorkerID, ttl)
	if err != nil {
		s.logf("github oidc enrollment mint failed: %v", err)
		writeError(w, http.StatusInternalServerError, "mint_failed", "worker token mint failed")
		return
	}
	verified, err := s.Auth.Verify(r.Context(), tok)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "verify_failed", "worker token verification failed")
		return
	}
	writeJSON(w, http.StatusOK, enrollmentResp{
		Token: tok, WorkerID: req.WorkerID, Scope: "worker",
		ExpiresAt: verified.ExpiresAt.UTC().Format(time.RFC3339),
		ExpiresIn: int(time.Until(verified.ExpiresAt).Seconds()),
		TokenType: "Bearer", Issuer: "github-actions-oidc", KeyID: s.Auth.KeyID(),
	})
}
