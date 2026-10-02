package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/internal/scheduler"
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
	// k-065: owner-bind the state-mutating lease verbs. Every action on an
	// existing lease (heartbeat/complete/release/revoke) must be issued by
	// the token bound to the lease's worker_id — bearer-only auth here was
	// finding D's gap. Grant is covered separately by k-060's gate in
	// grantLease; this is the non-creation path only. 404-before-403 (no
	// oracle); denial never mutates state. Dev mode (nil claims) passes.
	if code, reason, ownerOK := s.gateLeaseOwner(r, leaseID); !ownerOK {
		s.logf("lease %s denied: code=%s reason=%s", action, reason, leaseID)
		writeError(w, code, reason, reason)
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
	// k-067: canonicalize the caller-supplied worker_id before any gating or
	// persistence. A trailing/padding space (`"wrkr_a "`) is the k-064 finding
	// C exploit: in dev mode (nil claims) the lookup-by-id is exact-match so
	// the padded id lands in no-RAB legacy-pass and is then stored verbatim on
	// the lease + attempt, forging a second worker identity. Trimming here
	// makes the persisted identity equal the registry key for the real runner.
	// This is a normalization LAW, not a charset law (k-066), so it does not
	// relax validWorkerID; a non-space-padded bad id still fails elsewhere.
	body.WorkerID = strings.TrimSpace(body.WorkerID)
	if body.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "invalid_worker_id", "worker_id must not be whitespace-only")
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
	// advisory — /ready does not offer pool-scoped nodes to foreign or stale
	// workers. Enforcement happens HERE at lease grant as well, so a direct
	// claim, missing registry, stale heartbeat, or inactive registration
	// cannot bypass the isolation boundary.
	if pool := work.Requirements.Pool; pool != "" && !s.runnerIsLivePoolMember(body.WorkerID, pool, time.Now()) {
		s.logf("pool denied: worker=%s is not an active live member of pool=%q for work=%s",
			body.WorkerID, pool, body.WorkID)
		writeError(w, http.StatusForbidden, "pool_mismatch",
			"worker is not an active, live member of pool "+pool)
		return
	}
	if work.Requirements.Pool != "" {
		if eligible, reason := s.poolRunnerMeetsWorkRequirements(r.Context(), body.WorkerID, work, body.NodeID); !eligible {
			writeError(w, http.StatusForbidden, "runner_not_eligible", reason)
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

// runnerIsLivePoolMember is the mutation-time BYOC boundary. /ready is a
// placement hint and may retain pre-BYOC identities without heartbeats for
// compatibility; a pool-scoped lease must always have a current, active
// registration because a pool label alone is not proof of liveness.
func (s *Server) runnerIsLivePoolMember(workerID, pool string, now time.Time) bool {
	if s.RunnerRegistry == nil {
		return false
	}
	id, ok := s.RunnerRegistry.get(workerID)
	if !ok || !runnerHasFreshHeartbeat(id, now) {
		return false
	}
	for _, label := range id.Capabilities.Labels {
		if label == "pool:"+pool {
			return true
		}
	}
	return false
}

// runnerHasFreshHeartbeat is the shared definition of an available runner.
// Pool claims and alive-only discovery must agree: registration by itself is
// not evidence that a worker process is still polling.
func runnerHasFreshHeartbeat(id *runner.Identity, now time.Time) bool {
	return id != nil &&
		id.LifecycleState == runner.StateActive &&
		id.LastHeartbeatAt != nil &&
		!id.LastHeartbeatAt.Before(now.Add(-3*defaultHeartbeatInterval))
}

// poolRunnerMeetsWorkRequirements applies the same hard scheduler constraints
// at the lease mutation boundary that /ready applies during placement. A
// caller cannot bypass OS, architecture, trust, or other hard requirements by
// posting a lease claim directly.
func (s *Server) poolRunnerMeetsWorkRequirements(ctx context.Context, workerID string, work *workgraph.Work, nodeID string) (bool, string) {
	if s.RunnerRegistry == nil {
		return false, "runner registry is unavailable"
	}
	id, ok := s.RunnerRegistry.get(workerID)
	if !ok || id == nil {
		return false, "runner is not registered"
	}
	node, ok := work.Graph.Nodes[nodeID]
	if !ok {
		return true, "" // The store reports an unknown node using its established error.
	}
	assignment, err := scheduler.Select(ctx, work, &node, runnersFromIdentities([]*runner.Identity{id}))
	if err == nil {
		return true, ""
	}
	if assignment != nil && assignment.Reasoning != "" {
		return false, assignment.Reasoning
	}
	return false, err.Error()
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
	// Epoch is the fencing token issued with the lease (ADR-0033).
	// Required: omitting it yields 0, which matches no real lease
	// generation, and the store refuses the heartbeat.
	Epoch int64 `json:"epoch"`
}

func (s *Server) heartbeatLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body heartbeatLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body) // body optional
	ttl := time.Duration(body.TTLSeconds) * time.Second
	ref, ok := s.leaseRef(w, r, leaseID, body.Epoch)
	if !ok {
		return
	}
	lease, err := s.Store.RenewLease(r.Context(), ref, ttl)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		case errors.Is(err, store.ErrLeaseFenced):
			writeError(w, http.StatusConflict, ReasonLeaseFenced, err.Error())
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
	ExitCode        int                  `json:"exit_code"`
	Artifact        *workgraph.Artifact  `json:"artifact,omitempty"`
	ArtifactContent []byte               `json:"artifact_content"`
	Evidence        []workgraph.Evidence `json:"evidence,omitempty"`
	// Epoch is the fencing token issued with the lease (ADR-0033).
	// Required; a completion without it is refused as fenced.
	Epoch int64 `json:"epoch"`
}

// leaseRef assembles the fencing triple (executorId, leaseId, leaseEpoch)
// for a lease verb, writing the HTTP error itself and returning ok=false
// when the executor identity cannot be resolved. Centralised so all four
// verbs build the triple identically — a verb that forgot the epoch, or
// built it from a different source, would silently unfence itself.
func (s *Server) leaseRef(w http.ResponseWriter, r *http.Request, leaseID string, epoch int64) (store.LeaseRef, bool) {
	workerID, err := s.leaseExecutorIdentity(r.Context(), r, leaseID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
			return store.LeaseRef{}, false
		}
		writeError(w, http.StatusInternalServerError, "lease_lookup_failed", "failed to resolve lease executor identity")
		return store.LeaseRef{}, false
	}
	return store.LeaseRef{LeaseID: leaseID, WorkerID: workerID, Epoch: epoch}, true
}

const maxCompleteLeaseRequestBytes int64 = workgraph.MaxArtifactBytes*4/3 + (1 << 20)

func (s *Server) completeLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body completeLeaseBody
	r.Body = http.MaxBytesReader(w, r.Body, maxCompleteLeaseRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "completion_too_large", "lease completion exceeds the WORKS request limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if body.Artifact == nil && body.ArtifactContent != nil {
		writeError(w, http.StatusBadRequest, "artifact_metadata_required", "artifact metadata is required when artifact bytes are supplied")
		return
	}
	if body.ExitCode == 0 && body.Artifact == nil {
		writeError(w, http.StatusUnprocessableEntity, "artifact_required", "successful lease completion requires a published artifact")
		return
	}
	if body.Artifact != nil {
		lease, err := s.Store.GetLease(r.Context(), leaseID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
				return
			}
			writeError(w, http.StatusInternalServerError, "lease_lookup_failed", "failed to load lease")
			return
		}
		if lease.Status != workgraph.LeaseActive {
			writeError(w, http.StatusConflict, "lease_not_active", "artifact upload requires an active lease")
			return
		}
		if body.Artifact.NodeID != lease.NodeID {
			writeError(w, http.StatusBadRequest, "artifact_node_mismatch", "artifact node does not match the active lease")
			return
		}
		if err := s.persistWorkerArtifact(lease, body.Artifact, body.ArtifactContent); err != nil {
			switch {
			case errors.Is(err, errArtifactStoreUnavailable):
				writeError(w, http.StatusServiceUnavailable, "artifacts_unavailable", "artifact storage is not configured")
			case errors.Is(err, errArtifactTooLarge):
				writeError(w, http.StatusRequestEntityTooLarge, "artifact_too_large", "artifact exceeds the WORKS transfer limit")
			case errors.Is(err, errArtifactContentMissing):
				writeError(w, http.StatusUnprocessableEntity, "artifact_content_required", "worker must supply artifact bytes unless the canonical shared artifact file is present")
			case errors.Is(err, errArtifactMetadataMismatch):
				writeError(w, http.StatusUnprocessableEntity, "artifact_integrity_failed", "artifact digest, size, or node metadata does not match its content")
			default:
				s.logf("artifact persistence failed for lease %s: %v", leaseID, err)
				writeError(w, http.StatusInternalServerError, "artifact_store_failed", "failed to persist artifact")
			}
			return
		}
	}
	ref, ok := s.leaseRef(w, r, leaseID, body.Epoch)
	if !ok {
		return
	}
	wk, err := s.Store.CompleteLease(r.Context(), ref, body.ExitCode, body.Artifact, body.Evidence)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		case errors.Is(err, store.ErrLeaseFenced):
			writeError(w, http.StatusConflict, ReasonLeaseFenced, err.Error())
		case errors.Is(err, store.ErrLeaseNotActive):
			writeError(w, http.StatusConflict, "lease_not_active", leaseID)
		default:
			writeError(w, http.StatusInternalServerError, "complete_failed", err.Error())
		}
		return
	}
	// ADR-0033: the terminal-state publish is no longer fired from here.
	// Store.UpdateState recorded a work.terminal obligation in the outbox
	// inside the transition's own transaction, and the outbox dispatcher
	// delivers it. See publisher_hook.go for the migration.
	writeJSON(w, http.StatusOK, wk)
}

// releaseLeaseBody is POST /v1/leases/{id}/release.
type releaseLeaseBody struct {
	Reason string `json:"reason,omitempty"`
	// Epoch is the fencing token issued with the lease (ADR-0033).
	Epoch int64 `json:"epoch"`
}

func (s *Server) releaseLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body releaseLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	ref, ok := s.leaseRef(w, r, leaseID, body.Epoch)
	if !ok {
		return
	}
	if err := s.Store.ReleaseLease(r.Context(), ref, body.Reason); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		case errors.Is(err, store.ErrLeaseFenced):
			writeError(w, http.StatusConflict, ReasonLeaseFenced, err.Error())
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
	// Epoch is the fencing token issued with the lease (ADR-0033).
	Epoch int64 `json:"epoch"`
}

func (s *Server) revokeLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body revokeLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	ref, ok := s.leaseRef(w, r, leaseID, body.Epoch)
	if !ok {
		return
	}
	if err := s.Store.RevokeLease(r.Context(), ref, body.Reason); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		case errors.Is(err, store.ErrLeaseFenced):
			writeError(w, http.StatusConflict, ReasonLeaseFenced, err.Error())
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
//
// ADR-0033: the revoke presents the fencing triple ListExpiredLeases
// returned, i.e. the token this pass actually observed. Between the list
// and the revoke the node may have been re-granted to a new worker at a
// higher epoch; the fenced revoke is then refused rather than cancelling a
// live lease that belongs to someone else. Refusals are skipped, exactly
// like the pre-existing idempotency skip.
func reapOnce(ctx context.Context, s store.Store, limit int) (int, error) {
	expired, err := s.ListExpiredLeases(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("list expired: %w", err)
	}
	n := 0
	for _, l := range expired {
		// Mark lease EXPIRED, cancel attempt. Both must be idempotent.
		if err := s.RevokeLease(ctx, l.Ref(), "lease expired"); err != nil {
			// Skip — probably already revoked by a concurrent reaper or
			// worker, or the lease was re-granted since we listed it.
			continue
		}
		_ = s.MarkAttemptCancelled(ctx, l.AttemptID, "lease expired")
		n++
	}
	return n, nil
}
