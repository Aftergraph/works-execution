// Package api: rab/1.0 control-token law at lease claim (k-058 / ADR-0012/0014).
//
// THE LAW ENFORCED HERE -- and the law ONLY:
//
//	contract:rab/1.0 rule 2 (packages/abi): any runtime that advertises
//	the "control" capability MUST require a control token. k-053 made that
//	law hold at ADVERTISEMENT time (a control RAB without
//	control_token_required=true is rejected fail-closed at POST /abi).
//	This file makes the advertisement leg LIVE on the production claim
//	path: a runner that advertised a RAB requiring a control token must
//	present a token when it claims (leases) work. A claim without one is
//	denied 403 "control_token_required" BEFORE any lease state
//	transition.
//
//	OUT OF SCOPE -- LOUDLY (as shipped by k-058): this gate performs NO
//	TOKEN VALUE VERIFICATION. It never talks to an issuing authority,
//	never checks signatures, expiry, or audience, and never binds the
//	presented token to the claiming identity. The law enforced here is
//	the ADVERTISEMENT law: a control-capable runtime must PRESENT a
//	non-empty X-RAB-Control-Token header at claim time. Any non-empty
//	value passes. Token-identity binding and per-action authz are a
//	separate future slice (see the NOTE in auth.go: requireBearer "is
//	NOT a substitute for per-action authz").
//
//	k-062 UPDATE (this is the boundary k-058 declared, now closed when
//	configured): when Server.RABControlKey is non-empty (set from
//	WORKS_RAB_CONTROL_TOKEN by cmd/works-api), the presented value must
//	VERIFY as an HMAC credential bound to the claiming runnerID -- see
//	rab_control_token.go. When the key is empty/unset, verification mode
//	is OFF and the paragraph above is STILL the whole law, byte for
//	byte: unconfigured deployments keep the advertisement-only behavior
//	with zero change; only configured ones demand a real credential.
//
// RUNNER-IDENTITY INTERLOCK (exact semantics shipped):
//
//	Leases bind worker_id, not runner_id -- workgraph.Lease has no runner
//	field and no scheduling assignment is persisted on the lease record.
//	The current convention (already load-bearing for BYOC pool
//	enforcement in grantLease, RFC-0004: s.RunnerRegistry.get(worker_id))
//	is worker_id == runner_id. This gate therefore resolves the CLAIMING
//	WORKER'S worker_id against the registered runner identities:
//	  - no registry / unknown worker_id / registered runner that never
//	    posted a RAB  =>  LEGACY PASS. Pre-k-053 runners and unregistered
//	    workers are unaffected (zero behavior change for them).
//	  - stored RAB with RequiresControlToken() == false => pass.
//	  - stored RAB with RequiresControlToken() == true  => the claim MUST
//	    carry X-RAB-Control-Token non-empty, else 403.
//	No new data model is invented here; a lease->runner assignment record
//	is a separate slice.
package api

import (
	"context"
	"net/http"

	"github.com/JonasAbde/works-execution/packages/abi"
)

// rabControlTokenHeader is the HTTP header a control-capable runtime must
// present at lease claim. The name is the wire-visible half of the k-058
// law; keep it stable.
const rabControlTokenHeader = "X-RAB-Control-Token"

// ReasonControlTokenRequired is the stable error code returned when a
// runner whose stored RAB requires a control token claims without
// presenting one.
const ReasonControlTokenRequired = "control_token_required"

// abiFor returns the stored, already-validated rab/1.0 advertisement for
// runnerID. It is a thin read-only accessor over the k-053 registry leg
// (runnerRegistry.getABI in runner_abi.go, which this file never edits):
// the advertisement law and storage stay owned by k-053; this file only
// consumes it. ok=false means "no RAB stored" -- for an unregistered
// runner as well as a registered-but-never-advertising one; both are the
// legacy case the gate must not disturb.
func (s *Server) abiFor(runnerID string) (*abi.RAB, bool) {
	if s.RunnerRegistry == nil || runnerID == "" {
		return nil, false
	}
	return s.RunnerRegistry.getABI(runnerID)
}

// gateClaimByRAB is the whole law, in one function. Callers (the claim
// path, grantLease) invoke it BEFORE any lease state transition; when ok
// is false the caller must answer with writeError(code, reason, ...) and
// return without touching the store.
//
// runnerID is resolved by the caller from the CLAIMING WORKER'S worker_id
// (the worker_id == runner_id convention; see file header). ctx is
// carried for the future slice that backs the registry with a durable
// store; it is deliberately unused today.
func (s *Server) gateClaimByRAB(ctx context.Context, runnerID string, r *http.Request) (code int, reason string, ok bool) {
	_ = ctx // reserved: durable-registry lookup slice.

	rab, found := s.abiFor(runnerID)
	if !found {
		// Legacy interlock: no advertisement on file => pre-k-053
		// runner (or unregistered worker). The law binds only what a
		// runner ADVERTISED; nothing advertised, nothing gated.
		return 0, "", true
	}
	if !rab.RequiresControlToken() {
		// Observe-only (and passive-tier) RABs need no token: the
		// control-only law from packages/abi -- screenshot|input|
		// record|observe without control are token-free (ADR-0025
		// T1/T2). Do not over-gate.
		return 0, "", true
	}

	// *** THE ADVERTISEMENT LAW, UPGRADED TO A CREDENTIAL CHECK WHEN
	// CONFIGURED (k-062) ***
	// With Server.RABControlKey set, the presented value must VERIFY as
	// an HMAC credential bound to THIS runnerID (rab_control_token.go):
	// a garbage value or a valid token minted for another runner is
	// denied 403 "control_token_invalid" -- still before any lease state
	// transition, same ordering law as k-058. With the key unset, this
	// block is skipped and the k-058 presence-only behavior below runs
	// unchanged: any non-empty value from any runner passes, including a
	// token that "belongs to" a second runner (pinned by the k-058
	// tests, which must keep passing UNEDITED).
	if len(s.RABControlKey) > 0 {
		if !s.VerifyControlToken(r, runnerID) {
			if r.Header.Get(rabControlTokenHeader) == "" {
				// Missing/empty presentation keeps the k-058 code.
				return http.StatusForbidden, ReasonControlTokenRequired, false
			}
			return http.StatusForbidden, ReasonControlTokenInvalid, false
		}
		return 0, "", true
	}
	if r.Header.Get(rabControlTokenHeader) == "" {
		return http.StatusForbidden, ReasonControlTokenRequired, false
	}
	return 0, "", true
}
