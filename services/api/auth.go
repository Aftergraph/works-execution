// Package api — Zero-Secret worker enrollment (k-impl-003).
//
// Slice 4 adds a token-based auth layer between worker and control plane.
//
// DEV-MODE TOKEN ISSUER (V1)
// ==========================
// In V1 there is no real OIDC issuer; this file mints HS256-signed JWTs
// with an in-process secret. The Issuer interface is the boundary that
// real OIDC will replace — handlers depend only on the interface, so
// swapping to a remote OIDC provider (Vault, AWS IAM, GitHub OIDC, etc.)
// requires zero changes to handler / route code.
//
// To swap in production:
//
//	type oidcIssuer struct{ /* ...real provider... */ }
//	func (o *oidcIssuer) Mint(ctx, workerID) (EnrollmentToken, error) { return ... }
//	func (o *oidcIssuer) Verify(ctx, raw) (EnrollmentClaims, error) { return ... }
//
// and construct Server.Auth = oidcIssuer instead of NewHMACIssuer(...).
//
// The HS256 secret is generated per-process at startup (see NewHMACIssuer)
// and never written to disk, never accepted from the environment. That is
// the Zero-Secret Standard (#114): no static cloud credentials in code,
// config, or storage.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Issuer interface — the OIDC swap point.
// ---------------------------------------------------------------------------

// EnrollmentClaims is the verified payload of a worker enrollment token.
//
// Scope is the set of routes the bearer may invoke. In V1 the API only
// recognizes "worker". Future scopes (e.g. "operator", "auditor") will
// extend this without breaking the wire format.
type EnrollmentClaims struct {
	WorkerID  string    `json:"worker_id"`
	Scope     string    `json:"scope"`         // currently always "worker"
	IssuedAt  time.Time `json:"iat"`           // unix seconds, encoded by Mint
	ExpiresAt time.Time `json:"exp"`           // unix seconds, encoded by Mint
	Issuer    string    `json:"iss,omitempty"` // dev | <oidc-issuer>
}

// Issuer mints and verifies enrollment tokens. Implementations must be
// safe for concurrent use.
//
// V1 implementation: HMACIssuer (this file). Production: OIDC issuer
// (separate file/package, see comment at top).
type Issuer interface {
	Mint(ctx context.Context, workerID string, ttl time.Duration) (string, error)
	Verify(ctx context.Context, raw string) (EnrollmentClaims, error)
	KeyID() string
}

// ---------------------------------------------------------------------------
// HMAC implementation (V1, dev mode).
// ---------------------------------------------------------------------------

// HMACIssuer is the V1 Issuer. The signing key is generated per-process
// at construction time and never persisted. Token TTL is enforced via
// the standard JWT `exp` claim.
//
// The issuer has no configuration knobs that introduce static credentials —
// the key is always freshly random per process. This is intentional: a
// restart invalidates outstanding tokens, matching the Zero-Secret rule.
type HMACIssuer struct {
	mu     sync.Mutex
	key    []byte           // 32 bytes, never persisted
	keyID  string           // for kid header / audit logs
	issuer string           // JWT "iss" claim value
	maxTTL time.Duration    // hard cap on requested TTL
	clock  func() time.Time // injectable for tests
}

// NewHMACIssuer returns a dev-mode issuer with a freshly-generated 256-bit
// signing key. The returned Issuer is the only way to mint or verify
// tokens minted during this process lifetime — restarts rotate the key.
func NewHMACIssuer() *HMACIssuer {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		// rand.Read failing on Linux is essentially impossible, but we
		// don't want a nil key. Use a deterministic fallback that is
		// still per-process random (tied to a fresh random suffix).
		// In practice this branch is unreachable.
		panic(fmt.Sprintf("HMACIssuer: rand.Read failed: %v", err))
	}
	sum := sha256.Sum256(k)
	return &HMACIssuer{
		key:    k,
		keyID:  hex.EncodeToString(sum[:8]),
		issuer: "dev",
		maxTTL: 24 * time.Hour,
		clock:  time.Now,
	}
}

// NewHMACIssuerWithKey is exported for tests so a deterministic key can be
// injected. NOT for production: passing a static key defeats the
// Zero-Secret guarantee.
func NewHMACIssuerWithKey(key []byte) *HMACIssuer {
	sum := sha256.Sum256(key)
	return &HMACIssuer{
		key:    append([]byte(nil), key...),
		keyID:  hex.EncodeToString(sum[:8]),
		issuer: "dev",
		maxTTL: 24 * time.Hour,
		clock:  time.Now,
	}
}

// SetClock is a test hook — production callers should never call this.
func (h *HMACIssuer) SetClock(f func() time.Time) {
	h.mu.Lock()
	h.clock = f
	h.mu.Unlock()
}

// KeyID returns the kid associated with the signing key. Exposed for
// log correlation; the key itself never leaves the issuer.
func (h *HMACIssuer) KeyID() string { return h.keyID }

// Mint signs an enrollment token for the given worker. The token is a
// compact JWS (HS256) with claims:
//
//	{ "worker_id": ..., "scope": "worker", "iat": ..., "exp": ..., "iss": "dev" }
//
// ttl is clamped to MaxTTL (default 24h).
func (h *HMACIssuer) Mint(ctx context.Context, workerID string, ttl time.Duration) (string, error) {
	if workerID == "" {
		return "", errors.New("enrollment: empty worker id")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	h.mu.Lock()
	max := h.maxTTL
	now := h.clock()
	h.mu.Unlock()
	if ttl > max {
		ttl = max
	}
	claims := EnrollmentClaims{
		WorkerID:  workerID,
		Scope:     "worker",
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
		Issuer:    h.issuer,
	}
	header := map[string]any{
		"alg": "HS256",
		"typ": "JWT",
		"kid": h.keyID,
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("enrollment: marshal header: %w", err)
	}
	payloadJSON, err := json.Marshal(map[string]any{
		"worker_id": claims.WorkerID,
		"scope":     claims.Scope,
		"iat":       claims.IssuedAt.Unix(),
		"exp":       claims.ExpiresAt.Unix(),
		"iss":       claims.Issuer,
	})
	if err != nil {
		return "", fmt.Errorf("enrollment: marshal claims: %w", err)
	}
	signingInput := b64URL(headerJSON) + "." + b64URL(payloadJSON)
	sig := h.sign([]byte(signingInput))
	return signingInput + "." + b64URL(sig), nil
}

// Verify parses and validates a token. On success returns the claims;
// on failure returns one of:
//
//	ErrTokenMalformed, ErrTokenBadSignature, ErrTokenExpired.
//
// Implementations must NOT trust any claim until the signature is verified
// and the `exp` is in the future.
func (h *HMACIssuer) Verify(ctx context.Context, raw string) (EnrollmentClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	if hdr.Alg != "HS256" || (hdr.Typ != "" && hdr.Typ != "JWT") {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	signingInput := parts[0] + "." + parts[1]
	h.mu.Lock()
	expected := h.sign([]byte(signingInput))
	now := h.clock()
	h.mu.Unlock()
	if !hmac.Equal(sig, expected) {
		return EnrollmentClaims{}, ErrTokenBadSignature
	}
	var raw2 struct {
		WorkerID string `json:"worker_id"`
		Scope    string `json:"scope"`
		IAT      int64  `json:"iat"`
		EXP      int64  `json:"exp"`
		Issuer   string `json:"iss"`
	}
	if err := json.Unmarshal(payloadJSON, &raw2); err != nil {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	if raw2.WorkerID == "" || raw2.EXP == 0 {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	if now.Unix() >= raw2.EXP {
		return EnrollmentClaims{}, ErrTokenExpired
	}
	if raw2.Scope != "worker" {
		return EnrollmentClaims{}, ErrTokenMalformed
	}
	return EnrollmentClaims{
		WorkerID:  raw2.WorkerID,
		Scope:     raw2.Scope,
		IssuedAt:  time.Unix(raw2.IAT, 0),
		ExpiresAt: time.Unix(raw2.EXP, 0),
		Issuer:    raw2.Issuer,
	}, nil
}

// sign returns HS256(signingInput) using the in-process key.
func (h *HMACIssuer) sign(signingInput []byte) []byte {
	mac := hmac.New(sha256.New, h.key)
	mac.Write(signingInput)
	return mac.Sum(nil)
}

func b64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Verification errors.
// ---------------------------------------------------------------------------

var (
	ErrTokenMalformed    = errors.New("enrollment: token malformed")
	ErrTokenBadSignature = errors.New("enrollment: bad signature")
	ErrTokenExpired      = errors.New("enrollment: token expired")
)

// ---------------------------------------------------------------------------
// Context plumbing + middleware.
// ---------------------------------------------------------------------------

type ctxKey int

const claimsKey ctxKey = 1

// ClaimsFrom returns the verified enrollment claims placed on the context
// by RequireBearer. Returns nil if the request did not pass through the
// middleware (e.g. /healthz, /v1/workers/enroll).
func ClaimsFrom(ctx context.Context) *EnrollmentClaims {
	v, _ := ctx.Value(claimsKey).(*EnrollmentClaims)
	return v
}

// requireBearer is the HTTP middleware that enforces a valid Bearer
// enrollment token. On failure it writes a JSON 401/401/401 (depending on
// the failure mode) and does NOT invoke next.
//
// Endpoints behind this middleware can trust that:
//   - the request has Authorization: Bearer <jwt>
//   - the JWT signature is valid for the current process's signing key
//   - the JWT has not expired
//   - claims.WorkerID is non-empty
//
// NOTE: this is NOT a substitute for per-action authz (e.g. a worker
// trying to complete another worker's lease). Slice 5 will add per-lease
// owner check. Slice 4 only authenticates the caller.
func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Dev mode: auth disabled. The slice-1+2 e2e tests and local
		// development use this. Production deployments must set
		// AuthEnabled=true (the default in cmd/works-api).
		if !s.AuthEnabled {
			next.ServeHTTP(w, r)
			return
		}
		hdr := r.Header.Get("Authorization")
		if hdr == "" {
			writeError(w, http.StatusUnauthorized, "missing_authorization", "Authorization header required")
			return
		}
		const prefix = "Bearer "
		if !strings.HasPrefix(hdr, prefix) {
			writeError(w, http.StatusUnauthorized, "bad_authorization_scheme", "Authorization must be 'Bearer <token>'")
			return
		}
		raw := strings.TrimSpace(hdr[len(prefix):])
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "missing_token", "empty bearer token")
			return
		}
		claims, err := s.Auth.Verify(r.Context(), raw)
		if err != nil {
			switch {
			case errors.Is(err, ErrTokenExpired):
				writeError(w, http.StatusUnauthorized, "token_expired", err.Error())
			case errors.Is(err, ErrTokenBadSignature):
				writeError(w, http.StatusUnauthorized, "bad_signature", err.Error())
			default:
				writeError(w, http.StatusUnauthorized, "invalid_token", err.Error())
			}
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, &claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
