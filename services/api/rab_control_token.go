// Package api: rab/1.0 control tokens as server-verified credentials
// (k-062 / closes k-058's documented scope boundary).
//
// WHAT CHANGED (when configured):
//
//	k-058 (claim_abi_gate.go) enforced the ADVERTISEMENT law only: a
//	runner whose RAB sets control_token_required=true had to PRESENT a
//	non-empty X-RAB-Control-Token header at lease claim, and any value
//	passed. This file upgrades that law into a real credential check:
//	when the server holds a control-token key (Server.RABControlKey, set
//	in cmd/works-api from WORKS_RAB_CONTROL_TOKEN), the presented value
//	MUST verify -- as an HMAC-SHA256 proof bound to the CLAIMING
//	runner's identity. A well-formed token minted for another runner is
//	denied (this is exactly what k-058 case (e) pinned as NOT yet
//	bound; k-062 closes the binding in verification mode).
//
// WHAT DID NOT CHANGE (when unconfigured):
//
//	Empty/unset WORKS_RAB_CONTROL_TOKEN => verification mode OFF. The
//	claim gate remains the k-058 advertisement law EXACTLY as shipped:
//	presence of a non-empty header, any value passes, zero behavior
//	change. Dev-mode and pre-k-062 deployments are untouched; the
//	unedited k-058 tests (claim_abi_gate_test.go) pin this.
//
// TOKEN FORMAT (stateless; no issuer round-trip, no expiry claims):
//
//	base64url(runner_id) + "." + hex(HMAC-SHA256(key, runner_id))
//
//	base64url is Go's RawURLEncoding (unpadded), reusing the b64URL
//	helper from auth.go. The signature is lowercase hex. The header
//	carries the raw value -- there is NO scheme prefix (do NOT parse it
//	like a "Bearer " Authorization value; that parsing in auth.go is a
//	different credential surface entirely).
//
// The mint path is an operator-side helper (MintRABControlToken), not a
// public endpoint: no HTTP surface here issues tokens. Verification is
// constant-time over the HMAC bytes; the runner-id half is a public
// identifier (the server already knows it from the claim body), so a
// plain equality check on it is not a timing oracle.
//
// The token value and the key are NEVER logged and never echoed back in
// error bodies (k-062 tests sweep for leaks).
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// ReasonControlTokenInvalid is the stable error code returned when a
// control RAB's claim presents a token that FAILS server-side
// verification in k-062 mode (malformed, wrong key, or bound to a
// different runner). It is deliberately distinct from
// ReasonControlTokenRequired (k-058): missing stays "required", bad
// value becomes "invalid".
const ReasonControlTokenInvalid = "control_token_invalid"

// controlTokenSep separates the base64url(runner_id) half from the
// hex(HMAC) half on the wire.
const controlTokenSep = "."

var (
	// ErrControlTokenNoKey is returned by MintRABControlToken when the
	// caller passes an empty key -- there is nothing to sign with.
	ErrControlTokenNoKey = errors.New("control token: empty signing key")
	// ErrControlTokenNoRunner is returned when the runner id is empty.
	ErrControlTokenNoRunner = errors.New("control token: empty runner id")
)

// MintRABControlToken produces the control-token wire value binding
// runnerID to the HMAC-SHA256 proof over it keyed by key. Exported for
// OPERATOR use (a future cmd may expose it); this slice ships no public
// mint endpoint. Callers generate the token out-of-band and hand it to
// the runner's config -- exactly the "issuing authority" that k-058
// said it never talked to.
func MintRABControlToken(key []byte, runnerID string) (string, error) {
	if len(key) == 0 {
		return "", ErrControlTokenNoKey
	}
	if runnerID == "" {
		return "", ErrControlTokenNoRunner
	}
	return b64URL([]byte(runnerID)) + controlTokenSep + hex.EncodeToString(controlMAC(key, runnerID)), nil
}

// controlMAC = HMAC-SHA256(key, runner_id). Single definition shared by
// mint and verify so the signing input can never drift between them.
func controlMAC(key []byte, runnerID string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(runnerID))
	return mac.Sum(nil)
}

// VerifyControlToken reads the X-RAB-Control-Token header off r and
// checks it as a k-062 credential for runnerID. It returns true only
// when: the server key is set, the header is present and exactly
// well-formed (base64url "." hex), the decoded id equals runnerID (the
// identity binding), and the MAC matches under a constant-time compare.
//
// Empty key (verification mode OFF) => false: there is no credential to
// verify against, and callers must not consult this function in that
// mode (gateClaimByRAB keeps the k-058 presence law there). This fail-
// closed default prevents "forgot the key" from turning into "accept
// anything" at any future call site.
//
// It never logs and returns no string, so it cannot leak the value.
func (s *Server) VerifyControlToken(r *http.Request, runnerID string) bool {
	if len(s.RABControlKey) == 0 || r == nil || runnerID == "" {
		return false
	}
	parts := strings.Split(r.Header.Get(rabControlTokenHeader), controlTokenSep)
	if len(parts) != 2 {
		return false
	}
	idBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	// Identity binding: the token asserts WHICH runner it was minted
	// for; the assertion must name the claiming runner.
	if string(idBytes) != runnerID {
		return false
	}
	sig, err := hex.DecodeString(parts[1])
	if err != nil || len(sig) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(sig, controlMAC(s.RABControlKey, runnerID)) == 1
}
