// Package api: runner registration endpoints (slice 3 / k-impl-002).
//
// Two endpoints:
//
//	POST /v1/runners/register    body = runner.Identity (no runner_id)
//	                              -> server-minted runner_id, full record back
//	GET  /v1/runners/{id}        -> returns current identity or 404
//
// POST also accepts a fully-formed identity (with runner_id) and is
// idempotent on runner_id: re-registering an existing id returns the
// current stored record. This matches how the worker invokes us at startup
// (possibly with a freshly generated id; possibly with a restored one).
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JonasAbde/works-execution/internal/standards"
	"github.com/JonasAbde/works-execution/services/runner"
)

// ErrRunnerNotFound is returned when a runner_id is not in the registry.
var ErrRunnerNotFound = errors.New("runner not found")

// runnerRegistry is the in-process store for runner identities. Slice 3
// keeps it in-memory guarded by a mutex; persistence lives behind a future
// store interface (slice 4+).
type runnerRegistry struct {
	mu      sync.RWMutex
	byID    map[string]*runner.Identity
	bySpiffe map[string]string // spiffe_id -> runner_id for dedup
}

func newRunnerRegistry() *runnerRegistry {
	return &runnerRegistry{
		byID:     map[string]*runner.Identity{},
		bySpiffe: map[string]string{},
	}
}

func (r *runnerRegistry) get(id string) (*runner.Identity, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	got, ok := r.byID[id]
	if !ok {
		return nil, false
	}
	// Return a copy so callers can't mutate the stored record.
	cp := *got
	return &cp, true
}

func (r *runnerRegistry) put(id *runner.Identity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *id
	r.byID[id.RunnerID] = &cp
	if id.SpiffeID != "" {
		r.bySpiffe[id.SpiffeID] = id.RunnerID
	}
}

// List returns a snapshot of every registered identity. The slice and
// each element are independent copies, so callers (notably the scheduler
// adapter in services/api/api.go) can read without holding the registry
// lock. Order is not guaranteed; the scheduler sorts internally.
func (r *runnerRegistry) List() []*runner.Identity {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*runner.Identity, 0, len(r.byID))
	for _, id := range r.byID {
		cp := *id
		out = append(out, &cp)
	}
	return out
}

// runnerPathHandler routes /v1/runners/{id} -> getRunner
// (POST /v1/runners/register is registered separately because /register is
// not a path segment under {id}).
// defaultHeartbeatInterval mirrors the works-worker's default
// -heartbeat flag. The ready-handler treats a runner as stale when its
// LastHeartbeatAt is older than 3× this value (missed 3 beats).
const defaultHeartbeatInterval = 10 * time.Second

// listRunners handles GET /v1/runners — returns all registered runner
// identities (BYOC pool visibility). Query params:
//
//	pool=<name>  only runners labeled pool:<name>
//	alive=true   only runners with a recent heartbeat (not stale)
func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) {
	if s.RunnerRegistry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"runners": []any{}})
		return
	}
	all := s.RunnerRegistry.List()
	poolFilter := r.URL.Query().Get("pool")
	aliveOnly := r.URL.Query().Get("alive") == "true"
	staleCutoff := time.Now().Add(-3 * defaultHeartbeatInterval)

	out := make([]*runner.Identity, 0, len(all))
	for _, id := range all {
		if id == nil {
			continue
		}
		if aliveOnly && id.LastHeartbeatAt != nil && id.LastHeartbeatAt.Before(staleCutoff) {
			continue
		}
		if poolFilter != "" {
			found := false
			for _, l := range id.Capabilities.Labels {
				if l == "pool:"+poolFilter {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runners": out,
		"count":   len(out),
	})
}

func (s *Server) runnerPathHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/runners/")
	id := strings.TrimSuffix(path, "/")
	if id == "" {
		switch r.Method {
		case http.MethodGet:
			s.listRunners(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		}
		return
	}
	if strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getRunner(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
	}
}

// registerRunner handles POST /v1/runners/register.
//
// Body shape: a runner.Identity. The server enforces validation, mints a
// runner_id if the caller didn't supply one, and returns the canonical
// stored record. Re-registering the same runner_id is a no-op upsert.
func (s *Server) registerRunner(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	var in runner.Identity
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	// Caller may omit runner_id; mint one.
	if in.RunnerID == "" {
		in.RunnerID = runner.MintRunnerID()
	}
	// enrolled_at is server-stamped if absent (callers commonly don't know
	// the wall-clock, and the field is required by the schema).
	if in.EnrolledAt.IsZero() {
		in.EnrolledAt = time.Now().UTC()
	}

	// Enforce shape. Validate() checks the schema-shaped invariants.
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	// Cross-validate the full document against the embedded JSON schema.
	// This catches additionalProperties-style issues that the Go struct
	// can't see (e.g. unknown capability keys with values that violate
	// sub-schemas). It also pins the contract: any future change to the
	// schema breaks us at boot, not at audit time.
	b, _ := json.Marshal(in)
	if err := standards.ValidateBytes("runner-identity.schema.json", b); err != nil {
		writeError(w, http.StatusBadRequest, "schema_violation", err.Error())
		return
	}

	// Idempotent upsert with heartbeat refresh: if this runner_id is
	// already registered, update LastHeartbeatAt (the worker re-POSTs
	// the same registration as its heartbeat, RFC-0004) and refresh
	// capabilities in place (a worker may restart with new labels).
	// The record identity stays stable so pool membership and
	// scheduler assignments keep working across worker restarts.
	if s.RunnerRegistry == nil {
		s.RunnerRegistry = newRunnerRegistry()
	}
	now := time.Now().UTC()
	in.LastHeartbeatAt = &now
	if existing, ok := s.RunnerRegistry.get(in.RunnerID); ok && existing != nil {
		existing.LastHeartbeatAt = &now
		existing.Capabilities = in.Capabilities
		existing.TrustClass = in.TrustClass
		existing.LifecycleState = in.LifecycleState
		if in.SpiffeID != "" {
			existing.SpiffeID = in.SpiffeID
		}
		writeJSON(w, http.StatusOK, existing)
		return
	}

	// Default lifecycle to active on first successful registration unless
	// the caller explicitly chose pending (we honour their choice).
	if in.LifecycleState == "" {
		in.LifecycleState = runner.StateActive
	}
	if in.EnrolledAt.IsZero() {
		in.EnrolledAt = time.Now().UTC()
	}

	s.RunnerRegistry.put(&in)
	s.logf("runner registered: id=%s spiffe=%s trust=%s state=%s",
		in.RunnerID, in.SpiffeID, in.TrustClass, in.LifecycleState)
	writeJSON(w, http.StatusCreated, &in)
}

// getRunner handles GET /v1/runners/{id}.
func (s *Server) getRunner(w http.ResponseWriter, r *http.Request, id string) {
	if s.RunnerRegistry == nil {
		writeError(w, http.StatusNotFound, "not_found", id)
		return
	}
	got, ok := s.RunnerRegistry.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", id)
		return
	}
	writeJSON(w, http.StatusOK, got)
}