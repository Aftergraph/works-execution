// Lease owner-authorization (k-065).
//
// k-060 bound the GRANT verb (body.worker_id == token.worker_id). That
// covered the state-creating write but left the four state-_mutating_ verbs
// on an existing lease (heartbeat / complete / release / revoke) under
// bearer-only auth: any valid worker token that could guess the 128-bit
// lease id could revoke the victim's lease (k-064 finding D).
//
// This file closes D by applying the identical ownership interlock to those
// verbs: the bearer token's worker_id must equal the lease's WorkerID. The
// comparison happens AFTER lease resolution so a denial never mutates state,
// but BEFORE any of the four handlers runs (see leases.go leaseItemHandler).
//
// Denial ordering is 404-before-403: we resolve the lease to fetch its
// WorkerID for the comparison, and a not-found lease returns 404 (not_found)
// — never 403. This prevents the lease verb surface from becoming an
// existence oracle for lease ids. k-064 (D) already proved lease ids appear
// on no read surface; k-065 removes even the denial-side channel.
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// ReasonLeaseNotOwner is the wire reason returned when a non-grant lease verb
// (heartbeat/complete/release/revoke) is called by a token that does not own
// the lease. Pinned by TestAdversary34_NonGrantLeaseVerbsUnbound (k-064) —
// that test flips to a regression check the moment this exists.
const ReasonLeaseNotOwner = "lease_not_owner"

// gateLeaseOwner enforces k-065's owner-bind on a non-grant lease verb.
// Returns (0, "", true) when the caller owns the lease (handler proceeds),
// (NotFound, "not_found", false) when the lease doesn't exist, or
// (Forbidden, lease_not_owner, false) when a found lease belongs to another
// worker. Dev mode (no claims in context) passes through — the k-065 rule is
// a production-posture law, matching k-060's nil-claims => pass precedent.
func (s *Server) gateLeaseOwner(r *http.Request, leaseID string) (int, string, bool) {
	claims := ClaimsFrom(r.Context())
	if claims == nil {
		return 0, "", true
	}
	lease, err := s.Store.GetLease(r.Context(), leaseID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return http.StatusNotFound, "not_found", false
		}
		// Store error is not an auth question — surface as internal, not as
		// a spurious owner denial.
		return http.StatusInternalServerError, "lease_lookup_failed", false
	}
	if claims.WorkerID == lease.WorkerID {
		return 0, "", true
	}
	return http.StatusForbidden, ReasonLeaseNotOwner, false
}

// ReasonLeaseFenced is the wire reason returned when the fencing triple
// presented on a lease verb is stale — the epoch does not match the lease's
// current generation, or the executor identity does not match the lease's
// holder. ADR-0033.
//
// 409 Conflict, deliberately the same class as "lease_not_active": from the
// caller's point of view both mean "this lease is no longer yours to act
// on", and neither is an authentication failure. The distinction the
// caller DOES need is against 403 lease_not_owner: a 403 means "you are not
// the holder", a 409 lease_fenced means "you WERE the holder and your
// grant has since been superseded". Only the second tells a worker its
// view of the world is stale and it must re-acquire rather than retry.
const ReasonLeaseFenced = "lease_fenced"

// leaseExecutorIdentity resolves the executor identity for the fencing
// triple (ADR-0033).
//
// Production path: the token's worker_id. gateLeaseOwner has already
// established that this equals the lease's holder before any handler runs,
// so passing it into the store adds no new authorization decision — it
// makes the store's own check non-vacuous for callers that reach it
// directly, which the old signature (CompleteLease took no worker identity
// at all) did not.
//
// Dev path (no claims in context): fall back to the lease's own worker_id,
// matching the existing k-065 "nil claims => pass" precedent. The epoch is
// still enforced in dev, so the fencing law is testable without a token;
// only the identity half of the triple degrades, and only in the posture
// that already had no authentication at all.
func (s *Server) leaseExecutorIdentity(ctx context.Context, r *http.Request, leaseID string) (string, error) {
	if claims := ClaimsFrom(r.Context()); claims != nil {
		return claims.WorkerID, nil
	}
	lease, err := s.Store.GetLease(ctx, leaseID)
	if err != nil {
		return "", err
	}
	return lease.WorkerID, nil
}

// workgraph.Lease is the concrete type GetLease returns; keep the import
// explicit so a future interface change to another return type surfaces here
// rather than letting the file compile against an implicit type.
var _ *workgraph.Lease = (*workgraph.Lease)(nil)
