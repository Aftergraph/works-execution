// Package api: per-runner ownership authz on the runner registry
// surface (k-061).
//
// Closes the residual pinned by k-059 and by auth.go's own note ("this
// is NOT a substitute for per-action authz"). k-059 put the mutating
// POST /v1/runners/{id}/abi behind requireBearer, which proves token
// VALIDITY but not OWNERSHIP: any enrolled worker could still rewrite
// ANY runner's capability advertisement. And POST /v1/runners/register
// was fully anonymous (k-002), so any tokenless client could mint
// identities and - via the idempotent upsert with a supplied runner_id -
// heartbeat-flood or clobber a foreign runner.
//
// The law implemented here:
//
//   - Claims absent (AuthEnabled=false dev mode): the gate passes. This
//     interlock is deliberate and pinned by
//     TestRunnerAuthz_DevModeInterlock: the e2e suite and local
//     development run the zero-auth server, and requireBearer never
//     populates the context in that mode.
//   - Claims present: a caller may mutate runner identity X only when
//     its token's worker_id is exactly X. The worker_id == runner_id
//     convention is already load-bearing on this surface (BYOC pool
//     enforcement in grantLease, the k-058 claim gate), so this is the
//     rule the rest of the control plane already assumes - it is not a
//     new convention.
//
// Scope: MUTATIONS only. POST /abi (advertise/overwrite) and
// POST /v1/runners/register with a caller-supplied runner_id. The
// capability READS (GET /abi, POST /abi/negotiate) moved behind
// requireBearer in k-061 - capability info is operationally sensitive -
// but are deliberately NOT ownership-bound: negotiate is a caller-side
// computation (the scheduler asks what it may run on runner X), and
// binding it to runner X's own token would break placement. Identity
// reads on GET /v1/runners/{id} stay public (operator discovery).
//
// Legacy mode at registration: a caller may still OMIT runner_id and
// let the server mint one; the minting token is NOT auto-bound to the
// minted id (documented in docs/AUTH.md). Only the exact-match path -
// the one internal/worker uses at startup (runner_id == worker_id, see
// internal/worker/worker.go registerRunner) - satisfies the gate.
package api

import (
	"net/http"
)

// gateRunnerOwnership answers whether the bearer on r may MUTATE runner
// runnerID. It returns (status, errorCode, ok); when ok is false the
// caller must write the error and stop BEFORE any mint, parse, or
// registry mutation, so a denied request provably leaves state
// unchanged.
func (s *Server) gateRunnerOwnership(r *http.Request, runnerID string) (int, string, bool) {
	claims := ClaimsFrom(r.Context())
	if claims == nil {
		// Dev mode (AuthEnabled=false): middleware passed through
		// without claims. Pinned interlock - see file header.
		return 0, "", true
	}
	if claims.WorkerID == runnerID {
		return 0, "", true
	}
	return http.StatusForbidden, "not_runner_owner", false
}
