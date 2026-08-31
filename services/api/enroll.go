// Package api — POST /v1/workers/enroll (k-impl-003).
//
// This endpoint is the ONLY public route that does not require a Bearer
// token. Workers POST their self-chosen ID + a one-time enrollment
// challenge (the freshly-generated random hex string the operator
// provisions into the worker, e.g. via `works-bootstrap`) and receive a
// short-lived signed JWT to use on every subsequent request.
//
// In V1 the "challenge" is a static shared secret loaded from
// WORKS_ENROLL_SECRET at server startup. If the env var is unset the
// enrollment endpoint refuses to issue tokens — there is no default
// challenge and no way to enroll without one. This matches the
// Zero-Secret Standard: a worker cannot self-register; an operator must
// pre-provision the shared secret out-of-band.
//
// Future OIDC flow (NOT V1):
//  1. Operator runs the worker with `--oidc-token=<jwt-from-provider>`.
//  2. Worker POSTs to /enroll with the OIDC JWT.
//  3. API verifies with the OIDC provider's public key, mints a worker
//     enrollment token, and the shared-secret path is removed.
//
// V1 / OIDC have the same wire shape so the worker client doesn't need
// to change when the server swaps issuers.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/internal/worker"
)

// enrollmentReq is the body of POST /v1/workers/enroll.
type enrollmentReq struct {
	WorkerID  string `json:"worker_id"`
	Challenge string `json:"challenge"`
	// Scope is optional; defaults to "worker".
	Scope string `json:"scope,omitempty"`
	// TTLSeconds is optional; defaults to 3600 (1h). Capped by Issuer.MaxTTL.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

// enrollmentResp is the body returned on successful enrollment.
type enrollmentResp struct {
	Token     string `json:"token"`
	WorkerID  string `json:"worker_id"`
	Scope     string `json:"scope"`
	ExpiresAt string `json:"expires_at"` // RFC3339
	ExpiresIn int    `json:"expires_in"` // seconds
	TokenType string `json:"token_type"` // "Bearer"
	Issuer    string `json:"issuer"`     // "dev" today; OIDC issuer in prod
	KeyID     string `json:"kid"`        // signing-key id (for diagnostics)
}

// enrollHandler is POST /v1/workers/enroll. It is the unauthenticated
// entry point that issues enrollment tokens. Once enrolled, the worker
// uses the returned token as `Authorization: Bearer <token>` on all
// subsequent requests.
func (s *Server) enrollHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	if s.EnrollSecret == "" {
		// Fail closed — no shared secret provisioned means no enrollment.
		writeError(w, http.StatusServiceUnavailable, "enrollment_disabled",
			"server has no enrollment secret configured (set WORKS_ENROLL_SECRET)")
		return
	}
	var req enrollmentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.Challenge = strings.TrimSpace(req.Challenge)
	if req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "worker_id required")
		return
	}
	if !validWorkerID(req.WorkerID) {
		writeError(w, http.StatusBadRequest, "invalid_worker_id",
			"worker_id must match ^[A-Za-z0-9_.-]{1,128}$")
		return
	}
	if !enrollEqual(req.Challenge, s.EnrollSecret) {
		writeError(w, http.StatusUnauthorized, "bad_challenge", "enrollment challenge rejected")
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	tok, err := s.Auth.Mint(r.Context(), req.WorkerID, ttl)
	if err != nil {
		s.logf("enrollment mint failed: %v", err)
		writeError(w, http.StatusInternalServerError, "mint_failed", err.Error())
		return
	}
	// Re-verify to get the actual expiry the issuer stamped (after clamping).
	claims, err := s.Auth.Verify(r.Context(), tok)
	if err != nil {
		s.logf("enrollment verify-after-mint failed: %v", err)
		writeError(w, http.StatusInternalServerError, "verify_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, enrollmentResp{
		Token:     tok,
		WorkerID:  req.WorkerID,
		Scope:     "worker",
		ExpiresAt: claims.ExpiresAt.UTC().Format(time.RFC3339),
		ExpiresIn: int(time.Until(claims.ExpiresAt).Seconds()),
		TokenType: "Bearer",
		Issuer:    claims.Issuer,
		KeyID:     s.Auth.KeyID(),
	})
}

// validWorkerID enforces a conservative character set. We don't want
// worker IDs to be passable as path segments, header values, or log
// keys with arbitrary control characters. The constraint is:
//
//	[A-Za-z0-9_.-]{1,128}
//
// This matches the IDs the worker generates itself (wrkr_<hex>) and
// the IDs operators may pass via flag/env.
func validWorkerID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}

// enrollEqual is a constant-time challenge comparison to prevent timing
// side channels against the shared secret.
func enrollEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// Ensure the worker package's error sentinel (if any) is reachable for
// future enrichment; currently unused but documents the dependency.
var _ = errors.New
var _ worker.Client // keep the import block consistent if worker pkg later adds helpers.
