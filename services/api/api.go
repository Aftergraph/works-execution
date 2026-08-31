// Package api exposes the public HTTP API for works-execution.
//
// The API is the control plane boundary. It validates input, delegates
// persistence to the store, and enforces the state machine. It does not
// execute work — that is the worker's job.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/JonasAbde/works-execution/internal/manifest"
	"github.com/JonasAbde/works-execution/internal/scheduler"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/evidence"
	"github.com/JonasAbde/works-execution/services/observability"
	"github.com/JonasAbde/works-execution/services/publisher"
	"github.com/JonasAbde/works-execution/services/runner"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// Server is the HTTP API server. It holds a reference to the store.
type Server struct {
	Store  store.Store
	Logger *log.Logger
	// ArtifactsDir is the directory where workers write artifact/log files.
	// Required for the GET /v1/works/{id}/nodes/{n}/logs endpoint. Optional
	// in V1; when nil, the log endpoint returns 503.
	ArtifactsDir string
	// EvidenceConfig configures the /v1/works/{id}/evidence handler.
	// When nil, the endpoint returns 503 (evidence unavailable).
	EvidenceConfig *EvidenceConfig
	// ProvenanceConfig configures the /v1/works/{id}/provenance handler.
	// When nil, the endpoint returns 503 (provenance unavailable).
	ProvenanceConfig *ProvenanceConfig
	// RunnerRegistry holds runner identities (slice 3 / k-impl-002). Lazily
	// allocated by the runner endpoints; nil-safe for callers that don't
	// use the runner API.
	RunnerRegistry *runnerRegistry
	// Auth is the enrollment-token issuer (slice 4 / k-impl-003). Lazily
	// set to a fresh per-process HMACIssuer by Routes() when nil.
	// Production cmd/works-api can leave it nil — the auto-issuer's
	// signing key is freshly random per process, never persisted.
	Auth Issuer
	// EnrollSecret is the shared challenge required at POST /v1/workers/enroll.
	// When empty, /v1/workers/enroll returns 503 (fail-closed). Production
	// must set this from WORKS_ENROLL_SECRET via cmd/works-api.
	EnrollSecret string
	// Metrics is the optional observability registry served at GET /metrics.
	// When nil, the /metrics route is not mounted.
	Metrics *observability.Registry
	// MetricsCollector is invoked before each /metrics scrape to refresh
	// pull-class metrics (queue depth, process runtime). Ignored when
	// Metrics is nil.
	MetricsCollector *observability.Collector
	// Policy is the OPA Rego policy engine evaluated BEFORE every state-
	// mutating action (slice 4 / k-impl-011). When nil, the engine falls
	// back to a permissive default that denies nothing (legacy behavior).
	// Production cmd/works-api MUST set this to a loaded bundle — see
	// cmd/works-api/main.go. Tests can leave it nil.
	Policy *Engine
	// PolicyPath is the file-system path used to lazy-load the policy
	// engine when Policy is nil. Defaults to "policies/lease_grant.rego".
	// Ignored when Policy is set directly.
	PolicyPath string
	// AuthEnabled toggles Bearer-token enforcement. When false, the
	// requireBearer middleware is a no-op. Default false; production
	// cmd/works-api sets it true. Tests can leave it false.
	AuthEnabled bool
	// WebhookConfig configures the GitHub webhook receiver
	// (M1 / k-impl-018). When nil, POST /v1/webhook/github
	// returns 503 (webhook not enabled). The webhook endpoint
	// is intentionally outside requireBearer — the HMAC
	// signature is the security boundary, not a Bearer token.
	WebhookConfig *WebhookConfig
	// Publisher, when non-nil, is invoked once when a Work reaches
	// a terminal state (SUCCEEDED, FAILED, CANCELLED) and the
	// work's Source has a Repository + SHA. Publish runs in a
	// background goroutine so the HTTP request that triggered
	// the state transition is not delayed by GitHub's API.
	// Publish errors are logged and never propagated.
	Publisher publisher.Publisher
}

// ensureIssuer returns s.Auth, lazily constructing a default HMACIssuer
// if none was configured. This keeps the existing test surface working
// while guaranteeing production cmd/works-api gets a fresh, per-process
// signing key.
func (s *Server) ensureIssuer() Issuer {
	if s.Auth != nil {
		return s.Auth
	}
	s.Auth = NewHMACIssuer()
	return s.Auth
}

// EvidenceConfig configures evidence-bundle production.
type EvidenceConfig struct {
	// KeyID identifies which signing key the bundle was signed with.
	KeyID string
	// HMACKey is the symmetric signing key.
	HMACKey []byte
	// Runner identifies the executor producing the bundle.
	Runner evidence.Runner
}

// ProvenanceConfig configures workflow-provenance attestation production
// (slice 5 / k-impl-005). When nil, GET /v1/works/{id}/provenance returns
// 503 — the control plane refuses to mint attestations without a configured
// signer.
type ProvenanceConfig struct {
	// KeyID identifies which signing key the attestation was signed with.
	KeyID string
	// HMACKey is the symmetric signing key used to sign attestation envelopes.
	HMACKey []byte
}

// Routes returns an http.Handler with the public API mounted under /v1.
//
// Authentication model (slice 4 / k-impl-003):
//   - /v1/workers/enroll  — public; issues a short-lived HS256 JWT.
//   - /v1/leases/*        — requires Authorization: Bearer <enrollment-token>.
//   - /v1/workers/*       — requires Authorization: Bearer <enrollment-token>.
//     (Everything under that prefix except /enroll; currently /ready only.)
//   - /v1/works/* and /healthz remain unauthenticated (operator surface).
func (s *Server) Routes() http.Handler {
	s.ensureIssuer()
	mux := http.NewServeMux()
	mux.Handle("/v1/works", s.requireBearer(http.HandlerFunc(s.worksHandler)))           // POST = create, GET = list
	mux.HandleFunc("/v1/works/", s.workPathHandler)       // GET, POST .../cancel|queue, GET .../nodes/{n}/logs, GET .../evidence
	mux.HandleFunc("/v1/workers/enroll", s.enrollHandler) // unauthenticated; issues tokens
	// /v1/workers/ and /v1/leases/ are mounted through auth middleware.
	// We can't wrap an http.Handler with a HandleFunc, so we register the
	// mux's path under a small dispatcher that runs requireBearer first.
	mux.Handle("/v1/workers/", s.requireBearer(http.HandlerFunc(s.workersAuthHandler)))
	mux.Handle("/v1/leases", s.requireBearer(http.HandlerFunc(s.leasesPathHandler)))
	mux.Handle("/v1/leases/", s.requireBearer(http.HandlerFunc(s.leaseItemHandler)))
	mux.HandleFunc("/v1/runners/register", s.registerRunner) // POST runner identity
	mux.HandleFunc("/v1/runners/", s.runnerPathHandler)      // GET /v1/runners/{id}
	mux.Handle("/v1/audit-events", s.requireBearer(http.HandlerFunc(s.auditEventsHandler))) // GET = CloudEvents audit stream
	mux.Handle("/v1/dora", s.requireBearer(http.HandlerFunc(s.doraHandler)))                // GET = DORA metrics
	mux.HandleFunc("/healthz", s.healthz)
	// M1 (k-impl-018): GitHub webhook receiver. Unauthenticated by
	// design — HMAC signature is the security boundary. Operators
	// should firewall /v1/webhook on the listener if they want
	// extra defense-in-depth.
	mux.HandleFunc("/v1/webhook/github", s.githubWebhookHandler)
	if s.Metrics != nil {
		// GET /metrics — Prometheus exposition. Internal scrape only; not
		// behind requireBearer so Prometheus can scrape without an enroll
		// token. Production should firewall the listener.
		mux.Handle("/metrics", NewMetricsHandler(s.Metrics, s.MetricsCollector, s.Logger))
	}
	return s.recoverer(mux)
}

// workersAuthHandler dispatches authenticated requests under /v1/workers/.
// Only /ready is recognized today; anything else 404s. The requireBearer
// middleware has already validated the JWT and placed EnrollmentClaims on
// the request context by the time we get here.
func (s *Server) workersAuthHandler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/workers/ready" && r.Method == http.MethodGet:
		s.readyNodesHandler(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
}

// workPathHandler routes all paths under /v1/works/. It splits out:
//
//	/v1/works/{id}                       -> GET workItemHandler
//	/v1/works/{id}/cancel|queue          -> POST workItemHandler
//	/v1/works/{id}/evidence              -> GET workEvidenceHandler
//	/v1/works/{id}/provenance            -> GET workProvenanceHandler
//	/v1/works/{id}/nodes/{n}/logs        -> GET workLogsHandler
func (s *Server) workPathHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	// parts: [id] OR [id, action] OR [id, "nodes", nodeID, "logs"] OR [id, "evidence"] OR [id, "provenance"]
	if len(parts) >= 2 && parts[1] == "nodes" {
		s.workLogsHandler(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "evidence" {
		s.workEvidenceHandler(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "provenance" {
		s.workProvenanceHandler(w, r)
		return
	}
	s.workItemHandler(w, r)
}

// recoverer wraps a handler with a panic guard.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logf("panic: %v", rec)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errBody{Error: code, Message: msg})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// worksHandler handles POST /v1/works (create) and GET /v1/works (list).
func (s *Server) worksHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createWork(w, r)
	case http.MethodGet:
		s.listWorks(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
	}
}

// createWork handles POST /v1/works.
//
// If the request body includes `{"queue": true}`, the work is immediately
// transitioned CREATED -> QUEUED so workers can pick it up. Otherwise it
// stays in CREATED until an explicit /queue call.
func (s *Server) createWork(w http.ResponseWriter, r *http.Request) {
	type createBody struct {
		workgraph.Work
		Queue bool `json:"queue,omitempty"`
	}
	var body createBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	w_in := body.Work
	if w_in.ID == "" {
		w_in.ID = workgraph.NewID("wrk")
	}
	if w_in.State == "" {
		w_in.State = workgraph.StateCreated
	}
	if err := w_in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	// Per-node capability admission. Rejects works whose nodes declare
	// side effects or permissions outside the platform allow-list, and
	// fills safe defaults for missing capability fields.
	if err := manifest.ValidateAndEnrich(&w_in); err != nil {
		writeError(w, http.StatusBadRequest, "admission_rejected", manifest.FormatError(err))
		return
	}
	if err := s.Store.CreateWork(r.Context(), &w_in); err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
			return
		}
		s.logf("create work failed: %v", err)
		writeError(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	if body.Queue {
		if _, err := s.Store.UpdateState(r.Context(), w_in.ID, workgraph.StateQueued); err != nil {
			s.logf("auto-queue failed: %v", err)
		} else {
			w_in.State = workgraph.StateQueued
		}
	}
	writeJSON(w, http.StatusCreated, &w_in)
}

// listWorks handles GET /v1/works?limit=N.
func (s *Server) listWorks(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		var n int
		if _, err := fmt.Sscanf(l, "%d", &n); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	list, err := s.Store.ListWorks(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"works": list, "count": len(list)})
}

// workItemHandler routes /v1/works/{id}, /v1/works/{id}/cancel, /v1/works/{id}/queue.
func (s *Server) workItemHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		s.getWork(w, r, id)
	case r.Method == http.MethodPost && action == "cancel":
		s.cancelWork(w, r, id)
	case r.Method == http.MethodPost && action == "queue":
		s.queueWork(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
	}
}

func (s *Server) getWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.GetWork(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", id)
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

func (s *Server) cancelWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.UpdateState(r.Context(), id, workgraph.StateCancelled)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", id)
			return
		}
		writeError(w, http.StatusConflict, "cancel_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// queueWork transitions CREATED -> QUEUED. Returns 409 if the work is in a
// state that cannot transition to QUEUED.
func (s *Server) queueWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.UpdateState(r.Context(), id, workgraph.StateQueued)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", id)
			return
		}
		writeError(w, http.StatusConflict, "queue_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// readyNodesHandler is the scheduler poll endpoint.
//
// GET /v1/workers/ready?limit=N
//
// Returns a list of {work_id, node_id, run, env, timeout_s} entries that
// calling workers can pick up, along with the scheduler's explainability
// record for the assignment. Slice 4 (k-impl-009) wires the capability-
// aware scheduler in: each ready (work, node) is run through Select, and
// the chosen runner's identity + decision record is attached to the
// response so workers and operators can audit placement.
//
// Behavior when the runner registry is empty or no runner is eligible:
//   - If the registry is empty, the handler still returns the list of
//     ready nodes so existing workers can self-claim without a runner
//     identity record. The scheduler is bypassed (explanation empty).
//   - If the registry has runners but none are eligible for a particular
//     node, the node is omitted from the response (no point handing it
//     to a worker that can't execute it). The decision record is
//     surfaced under a top-level "unschedulable" field so operators
//     can see which node failed which constraint.
func (s *Server) readyNodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	limit := 25
	if l := r.URL.Query().Get("limit"); l != "" {
		var n int
		if _, err := fmt.Sscanf(l, "%d", &n); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	list, err := s.Store.ListWorks(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}

	// Snapshot the runner pool once per request. The scheduler is pure;
	// we don't need to hold the lock across the full scan.
	var pool []*scheduler.Runner
	if s.RunnerRegistry != nil {
		pool = runnersFromIdentities(s.RunnerRegistry.List())
	}

	type readyItem struct {
		WorkID     string                `json:"work_id"`
		NodeID     string                `json:"node_id"`
		Run        string                `json:"run"`
		Env        map[string]string     `json:"env,omitempty"`
		TimeoutS   int                   `json:"timeout_s,omitempty"`
		Image      string                `json:"image,omitempty"` // slice 5: docker image; empty = host subprocess
		Source     *workgraph.Source     `json:"source,omitempty"`
		Assignment *scheduler.Assignment `json:"assignment,omitempty"`
	}
	type unschedulable struct {
		WorkID     string         `json:"work_id"`
		NodeID     string         `json:"node_id"`
		Reason     string         `json:"reason"`
		Rejections map[string]int `json:"rejections,omitempty"`
	}
	var items []readyItem
	var skipped []unschedulable

	for _, work := range list {
		if work.State != workgraph.StateQueued && work.State != workgraph.StateRunning {
			continue
		}
		// Honor active leases: don't return a node another worker is leasing.
		active, err := s.Store.ActiveLeasesByWorkID(r.Context(), work.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "leases_failed", err.Error())
			return
		}
		for _, nid := range work.ReadyNodes(active) {
			n := work.Graph.Nodes[nid]
			item := readyItem{
				WorkID:   work.ID,
				NodeID:   nid,
				Run:      n.Run,
				Env:      n.Env,
				TimeoutS: n.TimeoutS,
				Image:    n.Runtime.Image,
				Source:   &work.Source,
			}

			// Skip the scheduler when no runners are registered. This
			// preserves slice-1/2 behavior so workers that haven't yet
			// enrolled can still poll. Production deployments will always
			// have at least one runner registered (worker startup blocks
			// on /v1/workers/enroll → /v1/runners/register).
			if len(pool) == 0 {
				items = append(items, item)
				if len(items) >= limit {
					break
				}
				continue
			}

			// Snapshot the node (value copy) so the scheduler sees an
			// immutable view even if the caller mutates later iterations.
			nodeCopy := n
			assignment, err := scheduler.Select(r.Context(), work, &nodeCopy, pool)
			if err != nil {
				// No eligible runner for this node. Record it under
				// unschedulable so operators see the failure cause,
				// and do NOT emit the item (a worker that picks it up
				// would fail at execution time anyway).
				skipped = append(skipped, unschedulable{
					WorkID:     work.ID,
					NodeID:     nid,
					Reason:     err.Error(),
					Rejections: assignment.RejectedConstraints,
				})
				continue
			}
			item.Assignment = assignment
			items = append(items, item)
			if len(items) >= limit {
				break
			}
		}
		if len(items) >= limit {
			break
		}
	}
	resp := map[string]any{"items": items, "count": len(items)}
	if len(skipped) > 0 {
		resp["unschedulable"] = skipped
	}
	writeJSON(w, http.StatusOK, resp)
}

// runnersFromIdentities converts the registered runner identities into
// the scheduler's internal Runner view. Only the fields the scheduler
// reads are copied; future dynamic-signal collectors should extend the
// mapping without touching internal/scheduler.
func runnersFromIdentities(ids []*runner.Identity) []*scheduler.Runner {
	out := make([]*scheduler.Runner, 0, len(ids))
	for _, id := range ids {
		if id == nil {
			continue
		}
		out = append(out, &scheduler.Runner{
			RunnerID:   id.RunnerID,
			Tenant:     runnerTenantFromSpiffe(id.SpiffeID),
			TrustClass: string(id.TrustClass),
			Lifecycle:  string(id.LifecycleState),
			OS:         append([]string(nil), id.Capabilities.OS...),
			Arch:       append([]string(nil), id.Capabilities.Arch...),
			CPUMilli:   id.Capabilities.CPUMilli,
			MemoryMiB:  id.Capabilities.MemoryMiB,
			GPU:        id.Capabilities.GPU,
			Toolchains: append([]string(nil), id.Capabilities.Toolchains...),
			Labels:     append([]string(nil), id.Capabilities.Labels...),
		})
	}
	return out
}

// runnerTenantFromSpiffe extracts the ns/<tenant> segment from a SPIFFE
// ID of the form spiffe://<domain>/ns/<tenant>/sa/<sa>. Empty input →
// empty output.
func runnerTenantFromSpiffe(spiffe string) string {
	const nsTag = "/ns/"
	i := strings.Index(spiffe, nsTag)
	if i < 0 {
		return ""
	}
	rest := spiffe[i+len(nsTag):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// workLogsHandler streams the artifact log for a (work_id, node_id) pair.
// Slice 2: serves the on-disk artifact file written by the worker. Slice 3
// will switch to true streaming-from-worker.
//
// Path: /v1/works/{id}/nodes/{n}/logs
func (s *Server) workLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	if s.ArtifactsDir == "" {
		writeError(w, http.StatusServiceUnavailable, "logs_unavailable", "ArtifactsDir not configured")
		return
	}
	// Path: /v1/works/{id}/nodes/{n}/logs
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[1] != "nodes" || parts[3] != "logs" {
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
		return
	}
	workID, nodeID := parts[0], parts[2]

	// Verify the work exists.
	if _, err := s.Store.GetWork(r.Context(), workID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "work_not_found", workID)
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	logPath := filepath.Join(s.ArtifactsDir, workID, nodeID+".log")
	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "logs_not_found", logPath)
			return
		}
		writeError(w, http.StatusInternalServerError, "stat_failed", err.Error())
		return
	}
	http.ServeFile(w, r, logPath)
}
