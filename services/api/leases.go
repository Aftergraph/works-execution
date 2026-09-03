package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/runner"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// grantLeaseBody is the POST /v1/leases/grant request payload.
type grantLeaseBody struct {
	WorkID     string `json:"work_id"`
	NodeID     string `json:"node_id"`
	WorkerID   string `json:"worker_id"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"` // default 25
}

// leasesPathHandler routes /v1/leases (POST = grant). /v1/leases/{id}/...
// is handled by leaseItemHandler. This split is needed because
// net/http.ServeMux disallows two handlers on the same prefix.
func (s *Server) leasesPathHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.grantLease(w, r)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
}

// leaseItemHandler routes /v1/leases/{id}/{action} where action is one of
// heartbeat, complete, release, revoke. It also handles the special
// "grant" sub-action (POST /v1/leases/grant) which is the lease creation
// endpoint; net/http.ServeMux dispatches the bare prefix to the trailing-
// slash handler so we catch it here.
func (s *Server) leaseItemHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/leases/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "lease id required")
		return
	}
	// Special case: /v1/leases/grant is the creation endpoint.
	if len(parts) == 1 && parts[0] == "grant" {
		s.grantLease(w, r)
		return
	}
	if len(parts) < 2 {
		writeError(w, http.StatusBadRequest, "missing_id", "lease id required")
		return
	}
	leaseID := parts[0]
	action := parts[1]

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	switch action {
	case "heartbeat":
		s.heartbeatLease(w, r, leaseID)
	case "complete":
		s.completeLease(w, r, leaseID)
	case "release":
		s.releaseLease(w, r, leaseID)
	case "revoke":
		s.revokeLease(w, r, leaseID)
	default:
		writeError(w, http.StatusNotFound, "not_found", action)
	}
}

// grantLease is POST /v1/leases/grant.
//
// Before any store mutation, the request is evaluated against the OPA
// Rego policy bundle (slice 4 / k-impl-011). The bundle is the authoritative
// source of policy logic — see policies/lease_grant.rego. Failures here are
// returned to the caller as 403 + a stable error code so the worker can
// retry or surface a user-visible message.
func (s *Server) grantLease(w http.ResponseWriter, r *http.Request) {
	var body grantLeaseBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if body.WorkID == "" || body.NodeID == "" || body.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "work_id, node_id, worker_id are required")
		return
	}
	// k-060: per-action authz -- the body's worker_id must equal the
	// authenticated token's worker_id (closes the slice-4 TODO in
	// auth.go: requireBearer authenticates, this authorizes the
	// action). Denied claims return 403 "worker_id_mismatch" and the
	// return below guarantees ZERO store touches.
	//
	// ORDERING LAW: owner check sits after the missing_field guard (an
	// empty worker_id is a malformed request, 400, not an
	// authorization question) and BEFORE gateClaimByRAB (k-058): the
	// claimer's identity must be real before we ask what their runtime
	// is allowed to do. Dev mode (AuthEnabled=false => ClaimsFrom nil)
	// passes unchanged -- the pinned interlock; see
	// claim_owner_authz.go for the full law.
	if code, reason, ownerOK := s.gateClaimOwner(r, body.WorkerID); !ownerOK {
		claims := ClaimsFrom(r.Context())
		s.logf("owner gate denied: claimed=%s token=%s code=%s", body.WorkerID, claims.WorkerID, reason)
		writeError(w, code, reason, "token worker_id "+claims.WorkerID+" may not claim leases as worker_id "+body.WorkerID)
		return
	}
	// k-058: rab/1.0 advertisement law at claim time (claim = lease grant;
	// workers self-claim via POST /v1/leases/grant). Denied claims return
	// 403 BEFORE any lease state transition. The runner-identity interlock
	// is the claiming worker's worker_id resolved against the runner
	// registry (the worker_id == runner_id convention already load-bearing
	// for BYOC pool enforcement below); no RAB on file => legacy pass.
	// See claim_abi_gate.go for the full law.
	if code, reason, gateOK := s.gateClaimByRAB(r.Context(), body.WorkerID, r); !gateOK {
		s.logf("claim gate denied: worker=%s code=%s", body.WorkerID, reason)
		writeError(w, code, reason, "advertised RAB requires "+rabControlTokenHeader+" at claim")
		return
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second

	// Load the work once; both the pool check and the policy check
	// need it. 404 without touching the store on unknown work.
	work, err := s.Store.GetWork(r.Context(), body.WorkID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "work_not_found", body.WorkID)
			return
		}
		s.logf("grant: get work: %v", err)
		writeError(w, http.StatusInternalServerError, "get_work_failed", err.Error())
		return
	}

	// BYOC pool enforcement (RFC-0004): the scheduler's pool filter is
	// advisory — the /ready endpoint simply won't offer pool-scoped
	// nodes to foreign workers. Enforcement happens HERE at lease
	// grant: if the work names a pool, the leasing worker must be
	// registered with the matching pool:<name> label. A worker that
	// bypasses /ready (or races it) gets 403. This is the actual
	// isolation boundary; without it the scheduler filter is just a
	// performance hint.
	if s.RunnerRegistry != nil && work.Requirements.Pool != "" {
		id, ok := s.RunnerRegistry.get(body.WorkerID)
		inPool := false
		if ok && id != nil {
			for _, l := range id.Capabilities.Labels {
				if l == "pool:"+work.Requirements.Pool {
					inPool = true
					break
				}
			}
		}
		if !inPool {
			s.logf("pool denied: worker=%s not in pool=%q for work=%s",
				body.WorkerID, work.Requirements.Pool, body.WorkID)
			writeError(w, http.StatusForbidden, "pool_mismatch",
				"worker is not registered in pool "+work.Requirements.Pool)
			return
		}
	}

	// Policy check SECOND. Builds a DecisionInput, evaluates the
	// bundle. Denials return 403 without touching the store.
	// Production cmd/works-api loads the bundle at startup; tests can
	// leave Policy nil to opt out.
	if s.Policy != nil {
		evidence := work.Evidence
		if evidence == nil {
			evidence = []workgraph.Evidence{}
		}
		runnerView := RunnerView{
			RunnerID:       body.WorkerID,
			TrustClass:     runner.TrustUntrusted,
			LifecycleState: runner.StateActive,
		}
		if s.RunnerRegistry != nil {
			if id, ok := s.RunnerRegistry.get(body.WorkerID); ok && id != nil {
				runnerView.TrustClass = id.TrustClass
				runnerView.LifecycleState = id.LifecycleState
			}
		}
		input := DecisionInput{
			Request: RequestContext{
				Action:   "lease.grant",
				WorkID:   body.WorkID,
				NodeID:   body.NodeID,
				WorkerID: body.WorkerID,
			},
			Work: WorkView{
				ID:     work.ID,
				Policy: work.Policy,
				State:  work.State,
			},
			Evidence: evidence,
			Runner:   runnerView,
		}
		dec, perr := s.Policy.EvaluateOrError(r.Context(), input)
		if perr != nil {
			s.logf("policy denied lease grant work=%s node=%s worker=%s reason=%s",
				body.WorkID, body.NodeID, body.WorkerID, dec.DenyReasons)
			writeError(w, http.StatusForbidden, formatDenyReason(firstReason(dec.DenyReasons)),
				"policy denied: "+firstReason(dec.DenyReasons))
			return
		}
	}

	lease, attempt, err := s.Store.GrantLease(r.Context(), body.WorkID, body.NodeID, body.WorkerID, ttl)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "work_not_found", body.WorkID)
		case errors.Is(err, store.ErrLeaseConflict):
			writeError(w, http.StatusConflict, "lease_conflict", "node already leased")
		default:
			s.logf("grant lease: %v", err)
			writeError(w, http.StatusInternalServerError, "grant_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"lease":   lease,
		"attempt": attempt,
	})
}

// firstReason returns the first deny reason in a slice, or a fallback
// constant when the slice is empty. The policy engine guarantees the
// slice is non-empty on deny, but defensive against future bundle changes.
func firstReason(rs []string) string {
	if len(rs) == 0 {
		return ReasonProductionAccessDenied
	}
	return rs[0]
}

// heartbeatLeaseBody is POST /v1/leases/{id}/heartbeat.
type heartbeatLeaseBody struct {
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

func (s *Server) heartbeatLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body heartbeatLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body) // body optional
	ttl := time.Duration(body.TTLSeconds) * time.Second
	lease, err := s.Store.RenewLease(r.Context(), leaseID, ttl)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		case errors.Is(err, store.ErrLeaseNotActive):
			writeError(w, http.StatusConflict, "lease_not_active", leaseID)
		default:
			writeError(w, http.StatusInternalServerError, "renew_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// completeLeaseBody is POST /v1/leases/{id}/complete.
type completeLeaseBody struct {
	ExitCode int                `json:"exit_code"`
	Artifact *workgraph.Artifact `json:"artifact,omitempty"`
	Evidence []workgraph.Evidence `json:"evidence,omitempty"`
}

func (s *Server) completeLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body completeLeaseBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	wk, err := s.Store.CompleteLease(r.Context(), leaseID, body.ExitCode, body.Artifact, body.Evidence)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		case errors.Is(err, store.ErrLeaseNotActive):
			writeError(w, http.StatusConflict, "lease_not_active", leaseID)
		default:
			writeError(w, http.StatusInternalServerError, "complete_failed", err.Error())
		}
		return
	}
	// Fire-and-forget publish to GitHub when the work has just
	// reached a terminal state (SUCCEEDED/FAILED) and has source
	// provenance. No-op when s.Publisher is nil.
	s.maybePublishOnTerminal(wk)
	writeJSON(w, http.StatusOK, wk)
}

// releaseLeaseBody is POST /v1/leases/{id}/release.
type releaseLeaseBody struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) releaseLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body releaseLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.Store.ReleaseLease(r.Context(), leaseID, body.Reason); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		default:
			writeError(w, http.StatusConflict, "release_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": leaseID})
}

// revokeLeaseBody is POST /v1/leases/{id}/revoke.
type revokeLeaseBody struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) revokeLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body revokeLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.Store.RevokeLease(r.Context(), leaseID, body.Reason); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		default:
			writeError(w, http.StatusConflict, "revoke_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": leaseID})
}

// ReaperConfig controls the lease-reaper goroutine.
type ReaperConfig struct {
	// Interval is how often the reaper scans for expired leases.
	// Default: 5s. Lower means faster SLO compliance at higher DB cost.
	Interval time.Duration
	// BatchLimit caps the number of leases expired per tick to bound work.
	// Default: 100.
	BatchLimit int
}

// RunLeaseReaper blocks until ctx is done, periodically expiring stale
// leases. The reaper is idempotent: it only marks ACTIVE leases as EXPIRED
// and only cancels attempts whose status is still 'running'. A future slice
// can run multiple reapers safely.
//
// This function is intended to be launched as `go api.RunLeaseReaper(...)`
// from cmd/works-api.
func RunLeaseReaper(ctx context.Context, s store.Store, cfg ReaperConfig) error {
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.BatchLimit == 0 {
		cfg.BatchLimit = 100
	}
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := reapOnce(ctx, s, cfg.BatchLimit); err != nil {
				// best-effort: log and continue
				fmt.Printf("reaper: %v\n", err)
			} else if n > 0 {
				fmt.Printf("reaper: expired %d lease(s)\n", n)
			}
		}
	}
}

// reapOnce performs a single reaper pass. Returns the number of leases expired.
func reapOnce(ctx context.Context, s store.Store, limit int) (int, error) {
	expired, err := s.ListExpiredLeases(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("list expired: %w", err)
	}
	n := 0
	for _, l := range expired {
		// Mark lease EXPIRED, cancel attempt. Both must be idempotent.
		if err := s.RevokeLease(ctx, l.ID, "lease expired"); err != nil {
			// Skip — probably already revoked by a concurrent reaper or worker.
			continue
		}
		_ = s.MarkAttemptCancelled(ctx, l.AttemptID, "lease expired")
		n++
	}
	return n, nil
}