// Package workgraph defines the durable Work primitive and its state machine.
//
// The Work object is the source of execution truth. Workers are disposable; the
// control plane owns state. See ADR-0001 and ADR-0002 in docs/adr/.
//
// This package has no IO. The store layer (services/work/store) persists Work
// values; the API layer (services/api) translates them to/from JSON.
package workgraph

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// State is the lifecycle state of a Work.
//
// CREATED -> PLANNING -> QUEUED -> RUNNING -> VERIFYING -> SUCCEEDED
// BLOCKED, FAILED, CANCELLED are terminal/side states.
type State string

const (
	StateCreated   State = "CREATED"
	StatePlanning  State = "PLANNING"
	StateQueued    State = "QUEUED"
	StateRunning   State = "RUNNING"
	StateVerifying State = "VERIFYING"
	StateSucceeded State = "SUCCEEDED"
	StateBlocked   State = "BLOCKED"
	StateFailed    State = "FAILED"
	StateCancelled State = "CANCELLED"
)

// IsTerminal returns true if the state is a terminal state (no further
// transitions allowed).
func (s State) IsTerminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	}
	return false
}

// validTransitions defines the allowed forward transitions from each state.
// Side states (FAILED, BLOCKED, CANCELLED) are reachable from any non-terminal
// state via explicit API actions, not listed here.
//
// FAILED is intentionally not in any forward path: a failed Work stays failed
// until a human or policy explicitly resets it (a future slice).
var validTransitions = map[State]map[State]bool{
	StateCreated:   {StatePlanning: true, StateQueued: true, StateCancelled: true, StateFailed: true, StateBlocked: true},
	StatePlanning:  {StateQueued: true, StateFailed: true, StateBlocked: true, StateCancelled: true},
	StateQueued:    {StateRunning: true, StateCancelled: true, StateFailed: true, StateBlocked: true},
	StateRunning:   {StateVerifying: true, StateFailed: true, StateCancelled: true},
	StateVerifying: {StateSucceeded: true, StateFailed: true, StateCancelled: true},
}

// CanTransition reports whether moving from `from` to `to` is allowed by the
// state machine. CANCELLED is reachable from any non-terminal state.
func CanTransition(from, to State) bool {
	if from.IsTerminal() {
		return false
	}
	if to == StateCancelled {
		return true
	}
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// ErrInvalidTransition is returned when a state transition violates the state
// machine. The caller should treat this as a permanent failure for that
// transition attempt; no retry will succeed.
var ErrInvalidTransition = errors.New("invalid state transition")

// Node is a single executable step inside a Work's graph.
//
// Capability fields (Permissions, SideEffects, Retries, Cache) describe the
// per-node action-manifest contract. They are optional on the wire; admission
// (internal/manifest) fills safe defaults and rejects undeclared side effects
// or permissions before persistence.
type Node struct {
	ID       string            `json:"id"`
	Run      string            `json:"run"`
	Needs    []string          `json:"needs,omitempty"`
	Cache    bool              `json:"cache,omitempty"`
	Evidence EvidenceSpec      `json:"evidence,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	TimeoutS int               `json:"timeout_s,omitempty"`

	// --- Capability manifest (action-manifest.schema.json) ---

	// Permissions declares what the node needs at runtime. Allowed values
	// are defined in internal/manifest/admission.go and mirror the schema
	// enum: read, write, execute, network, secrets, privileged.
	Permissions []string `json:"permissions,omitempty"`

	// SideEffects declares what the node intends to mutate externally.
	// Anything outside the allow-list is rejected at admission time.
	// Allowed values: network_egress, filesystem_write, deployment,
	// secret_consumption, external_api_call, state_mutation.
	SideEffects []string `json:"side_effects,omitempty"`

	// Retries is a pointer so admission can distinguish "unset" from
	// "explicit zero" (max_attempts=0 is invalid per schema; min=1).
	Retries *RetrySpec `json:"retries,omitempty"`

	// Cache is a pointer for the same reason; cache.enabled=false is
	// the safe default but is distinct from "caller didn't say".
	CacheSpec *CacheSpec `json:"cache_spec,omitempty"`
}

// RetrySpec declares retry policy. Mirrors action-manifest.schema.json
// `retries` object.
type RetrySpec struct {
	MaxAttempts int      `json:"max_attempts,omitempty"`
	Backoff     string   `json:"backoff,omitempty"` // none | linear | exponential
	RetryOn     []string `json:"retry_on,omitempty"`
}

// CacheSpec declares cache policy. Mirrors action-manifest.schema.json
// `cache` object.
type CacheSpec struct {
	Enabled   bool     `json:"enabled,omitempty"`
	KeyInputs []string `json:"key_inputs,omitempty"`
	Scope     string   `json:"scope,omitempty"` // worker-local | organization | global
}

// EvidenceSpec declares what evidence a successful node run must produce.
type EvidenceSpec struct {
	Required bool     `json:"required,omitempty"`
	Types    []string `json:"types,omitempty"` // build, test, typecheck, lint, security_scan, artifact
}

// Source identifies what triggered the Work.
type Source struct {
	Type       string `json:"type"` // cli, github_pull_request, github_push, schedule, api
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Actor      string `json:"actor,omitempty"`
}

// Objective describes what outcome the Work is requesting.
type Objective struct {
	Type        string         `json:"type"` // verify_change, build, test, deploy, custom
	Description string         `json:"description,omitempty"`
	Constraints map[string]any `json:"constraints,omitempty"`
}

// Requirements declare hard execution constraints.
type Requirements struct {
	OS         string  `json:"os,omitempty"`        // linux
	Arch       string  `json:"arch,omitempty"`      // amd64, arm64
	CPUMilli   int     `json:"cpu_milli,omitempty"` // 1000 = 1 CPU
	MemoryMiB  int     `json:"memory_mib,omitempty"`
	Confidence string  `json:"confidence,omitempty"` // development, staging, production
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"`
}

// Policy declares authority and access.
type Policy struct {
	ProductionAccess bool     `json:"production_access,omitempty"`
	SecretsScope     []string `json:"secrets_scope,omitempty"`
	ForkPolicy       string   `json:"fork_policy,omitempty"` // deny, allow-list, allow
	TrustClass       string   `json:"trust_class,omitempty"` // untrusted, standard, privileged
}

// Attempt is one execution attempt of one node. Immutable after completion
// (see ADR for rationale).
type Attempt struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	WorkerID    string    `json:"worker_id,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	ExitCode    int       `json:"exit_code"`
	Status      string    `json:"status"` // running, succeeded, failed, cancelled, timed_out
	LogRef      string    `json:"log_ref,omitempty"`
	ArtifactIDs []string  `json:"artifact_ids,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// Artifact is a content-addressed output of a node run.
type Artifact struct {
	ID       string `json:"id"`        // matches sha256 of bytes
	MimeType string `json:"mime_type"` // text/plain, application/json, ...
	Size     int64  `json:"size"`
	NodeID   string `json:"node_id"`
	Path     string `json:"path"` // local fs path for V1; CAS key later
}

// Evidence is a structured verification record bound to a node attempt.
type Evidence struct {
	ID          string         `json:"id"`
	NodeID      string         `json:"node_id"`
	AttemptID   string         `json:"attempt_id"`
	Type        string         `json:"type"`   // build, test, typecheck, lint, security_scan, artifact, policy
	Result      string         `json:"result"` // pass, fail, warn, skip
	RecordedAt  time.Time      `json:"recorded_at"`
	ArtifactID  string         `json:"artifact_id,omitempty"`
	Signer      string         `json:"signer,omitempty"`
	Environment string         `json:"environment,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// LeaseStatus is the lifecycle state of a Lease.
type LeaseStatus string

const (
	LeaseActive   LeaseStatus = "ACTIVE"
	LeaseExpired  LeaseStatus = "EXPIRED"  // terminal — reaper detected timeout
	LeaseRevoked  LeaseStatus = "REVOKED"  // terminal — explicit cancellation
	LeaseReleased LeaseStatus = "RELEASED" // terminal — worker voluntarily gave it back
)

// IsTerminal reports whether the lease is in a terminal state.
func (s LeaseStatus) IsTerminal() bool {
	switch s {
	case LeaseExpired, LeaseRevoked, LeaseReleased:
		return true
	}
	return false
}

// Lease is the time-bounded authorization for a worker to execute a node.
// While ACTIVE, only the worker holding the lease may transition the node.
// On expiry or revocation, the node becomes ready for re-leasing.
//
// See RFC-0001 (docs/rfcs/RFC-0001-slice-2-leases-and-recovery.md) for the
// full state machine and TTL math.
type Lease struct {
	ID         string      `json:"id"`
	WorkID     string      `json:"work_id"`
	NodeID     string      `json:"node_id"`
	WorkerID   string      `json:"worker_id"`
	AttemptID  string      `json:"attempt_id"`
	GrantedAt  time.Time   `json:"granted_at"`
	ExpiresAt  time.Time   `json:"expires_at"`
	LastBeatAt time.Time   `json:"last_beat_at"`
	Status     LeaseStatus `json:"status"`
}

// ValidateLeaseTransition reports whether moving from `from` to `to` is
// permitted. The only allowed transitions out of ACTIVE are to terminal
// states (EXPIRED, REVOKED, RELEASED). All other transitions are denied.
func ValidateLeaseTransition(from, to LeaseStatus) bool {
	if from == to {
		return false
	}
	if from.IsTerminal() {
		return false
	}
	switch to {
	case LeaseExpired, LeaseRevoked, LeaseReleased:
		return true
	}
	return false
}

// Work is the durable execution object. It is the source of execution truth.
type Work struct {
	ID             string       `json:"id"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	Source         Source       `json:"source"`
	Objective      Objective    `json:"objective"`
	Graph          Graph        `json:"graph"`
	Requirements   Requirements `json:"requirements"`
	Policy         Policy       `json:"policy"`
	State          State        `json:"state"`
	Attempts       []Attempt    `json:"attempts,omitempty"`
	Artifacts      []Artifact   `json:"artifacts,omitempty"`
	Evidence       []Evidence   `json:"evidence,omitempty"`
	IdempotencyKey string       `json:"idempotency_key,omitempty"`
	CorrelationID  string       `json:"correlation_id,omitempty"`
}

// Graph is the execution DAG. Nodes is a map keyed by node ID for cheap lookup;
// Edges is derived from Node.Needs.
type Graph struct {
	Nodes map[string]Node `json:"nodes"`
}

// NewID returns a new prefixed ID using crypto/rand. 16 bytes of entropy
// encoded as hex — 32 chars after the prefix — is collision-safe for any
// realistic workload in V1.
func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is a system-level failure; panic is correct.
		panic(fmt.Sprintf("workgraph: crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Validate checks structural invariants on a Work value. It does not check
// state transitions; for that, use ValidateTransition.
func (w *Work) Validate() error {
	if w.ID == "" {
		return errors.New("work.id is required")
	}
	if w.Objective.Type == "" {
		return errors.New("work.objective.type is required")
	}
	if len(w.Graph.Nodes) == 0 {
		return errors.New("work.graph.nodes must contain at least one node")
	}
	for id, n := range w.Graph.Nodes {
		if n.Run == "" {
			return fmt.Errorf("node %q: run is required", id)
		}
		for _, dep := range n.Needs {
			if _, ok := w.Graph.Nodes[dep]; !ok {
				return fmt.Errorf("node %q: needs %q which is not declared", id, dep)
			}
		}
	}
	return nil
}

// ValidateTransition checks that moving w.State from `to` is permitted.
func (w *Work) ValidateTransition(to State) error {
	if !CanTransition(w.State, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, w.State, to)
	}
	return nil
}

// ReadyNodes returns the IDs of nodes in the graph that are ready to execute:
// all their dependencies are SUCCEEDED (or the node has no dependencies), the
// Work is in QUEUED or RUNNING, the node has no in-flight attempt, and no
// active lease exists for the node.
//
// This is the scheduler's primary input.
func (w *Work) ReadyNodes(activeLeases map[string]bool) []string {
	if w.State != StateQueued && w.State != StateRunning {
		return nil
	}
	// Build a set of nodes that already have a successful attempt.
	succeeded := map[string]bool{}
	for _, a := range w.Attempts {
		if a.Status == "succeeded" {
			succeeded[a.NodeID] = true
		}
	}
	// Nodes currently in flight (running attempt, not yet terminal).
	inFlight := map[string]bool{}
	for _, a := range w.Attempts {
		if a.Status == "running" {
			inFlight[a.NodeID] = true
		}
	}

	var ready []string
	for id, n := range w.Graph.Nodes {
		if succeeded[id] || inFlight[id] {
			continue
		}
		// A node with an active lease is NOT ready — another worker owns it.
		// The lease holder must complete or release before this node re-queues.
		if activeLeases[id] {
			continue
		}
		depsOK := true
		for _, dep := range n.Needs {
			if !succeeded[dep] {
				depsOK = false
				break
			}
		}
		if depsOK {
			ready = append(ready, id)
		}
	}
	return ready
}

// ReadyNodesNoLeases is a convenience wrapper for callers that don't have a
// lease view (tests, slice-1-style introspection). Equivalent to
// ReadyNodes(nil).
func (w *Work) ReadyNodesNoLeases() []string {
	return w.ReadyNodes(nil)
}
