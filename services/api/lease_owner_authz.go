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

// workgraph.Lease is the concrete type GetLease returns; keep the import
// explicit so a future interface change to another return type surfaces here
// rather than letting the file compile against an implicit type.
var _ *workgraph.Lease = (*workgraph.Lease)(nil)
