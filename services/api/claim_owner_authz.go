// Package api: per-action authz at lease claim -- the claimer's identity
// must match the authenticated token (k-060, closes the slice-4 TODO).
//
// THE LAW ENFORCED HERE -- and the law ONLY:
//
//	auth.go (slice 4 / k-impl-003) ships requireBearer, which proves a
//	token is VALID (signed, unexpired, scope "worker") and stores the
//	verified EnrollmentClaims on the request context. Its own header
//	comment pinned the gap: "this is NOT a substitute for per-action
//	authz ... Slice 5 will add per-lease owner check. Slice 4 only
//	authenticates the caller." Until k-060, any validly enrolled worker
//	token could POST /v1/leases/grant with ANY worker_id string in the
//	body: token A claimed work as worker B -- identity confusion on the
//	lease path. This file closes that gap:
//
//	  the body's worker_id MUST equal the authenticated token's
//	  worker_id (claims.WorkerID from ClaimsFrom(r.Context())).
//	  Mismatch => 403 "worker_id_mismatch" BEFORE any lease state
//	  transition.
//
// This is per-ACTION authz on the claim verb only. It does not gate
// heartbeat/complete/release/revoke by lease id (those bind to a lease
// created under the verified identity and are separate verbs; a future
// slice can extend the same ClaimsFrom pattern to them). It performs no
// capability reasoning -- that is k-058's job (claim_abi_gate.go), and
// the owner check deliberately runs FIRST: the claimer's identity must
// be real before we ask what their runtime is allowed to do.
//
// DEV-MODE INTERLOCK (exact semantics shipped):
//
//	ClaimsFrom returns nil when the request never passed through the
//	bearer middleware with auth on -- which is precisely what
//	AuthEnabled=false does (requireBearer passes through without
//	storing claims; see auth.go). A nil claims set therefore means
//	"dev mode / local e2e, no identity layer at all" and PASSES the
//	gate unchanged: zero behavior change for AuthEnabled=false
//	servers (the slice-1+2 e2e surface). This mirrors k-058's legacy-
//	pass interlock (no RAB on file => nothing to gate). When auth IS
//	enabled, requireBearer guarantees claims is non-nil with a
//	non-empty WorkerID, so the equality check below only ever compares
//	two real identities. The interlock is pinned by
//	claim_owner_authz_test.go case (a): it is deliberate surface, not
//	an accident of ordering.
//
// Error strings carry BOTH worker ids on a mismatch. Worker ids are
// public identifiers, not secrets: GET /v1/runners/{id} and the lease
// records themselves expose them. Echoing them back is the whole point
// -- the honest caller (a misconfigured agent) learns which two ids
// disagree; the dishonest one learns nothing it could not enumerate.
package api

import (
	"net/http"
)

// ReasonWorkerIDMismatch is the stable error code returned when the
// authenticated token's worker_id does not equal the worker_id the
// request body tries to claim as. Keep it stable: workers and the
// k-060 tests match on it.
const ReasonWorkerIDMismatch = "worker_id_mismatch"

// gateClaimOwner is the whole law, in one function. Callers (the claim
// path, grantLease) invoke it BEFORE any lease state transition and
// BEFORE gateClaimByRAB; when ok is false the caller must answer with
// writeError(code, reason, ...) and return without touching the store.
//
// claimedWorkerID is the body-supplied identity the caller wants to
// claim as -- never trusted; it is only ever compared against the
// verified token identity.
func (s *Server) gateClaimOwner(r *http.Request, claimedWorkerID string) (code int, reason string, ok bool) {
	claims := ClaimsFrom(r.Context())
	if claims == nil {
		// Dev-mode interlock (see file header): no verified identity
		// was established at all (AuthEnabled=false), so there is no
		// token-vs-body pairing to adjudicate. The claim proceeds
		// exactly as it did pre-k-060.
		return 0, "", true
	}
	if claims.WorkerID == claimedWorkerID {
		// The claimer IS the enrolled identity. Pass.
		return 0, "", true
	}
	// Identity confusion attempted: a valid token for one worker
	// claiming leases as another. Deny; the caller answers 403 before
	// any lease state transition.
	return http.StatusForbidden, ReasonWorkerIDMismatch, false
}
