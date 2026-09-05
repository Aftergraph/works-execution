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
	"strings"
	"time"
)

// State is the lifecycle state of a Work.
//
// CREATED -> PLANNING -> QUEUED -> RUNNING -> VERIFYING -> SUCCEEDED
// BLOCKED, FAILED, CANCELLED are terminal/side states.
//
// k-mission-01 (ADR-0008/0009, work.schema/1.0): mission-contract Works add
// forward states a CI Work never reaches. They are only reachable when the
// Work carries a BudgetCeiling (see IsMission).
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

	// Forward states (work.schema/1.0, frozen). WaitingHuman pauses the
	// budget clock (ADR-0009). Suspended is the hard-stop/checkpoint state.
	// BudgetExhausted is terminal-for-current-budget: resumable only by a
	// human-granted budget increase — never by the runtime itself.
	StateWaitingHuman    State = "WAITING_HUMAN"
	StateSuspended       State = "SUSPENDED"
	StateBudgetExhausted State = "BUDGET_EXHAUSTED"
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
//
// k-mission-01: forward mission states (ADR-0009). Budget-gov transitions are
// only valid on mission Works (IsMission); the kernel enforces that at the
// transition site, not in this table, because the table has no Work context.
var validTransitions = map[State]map[State]bool{
	StateCreated:   {StatePlanning: true, StateQueued: true, StateCancelled: true, StateFailed: true, StateBlocked: true},
	StatePlanning:  {StateQueued: true, StateFailed: true, StateBlocked: true, StateCancelled: true},
	StateQueued:    {StateRunning: true, StateCancelled: true, StateFailed: true, StateBlocked: true},
	StateRunning:   {StateVerifying: true, StateFailed: true, StateCancelled: true, StateWaitingHuman: true, StateSuspended: true, StateBudgetExhausted: true},
	StateVerifying: {StateSucceeded: true, StateFailed: true, StateCancelled: true, StateWaitingHuman: true, StateSuspended: true},

	// From budget-governed pause states:
	//   WAITING_HUMAN -> RUNNING  (human approved the blocking syscall)
	//   WAITING_HUMAN -> CANCELLED/FAILED/BLOCKED (side paths, generic rule)
	//   SUSPENDED     -> RUNNING  (resumed from checkpoint after budget grant)
	//   BUDGET_EXHAUSTED -> SUSPENDED (human granted budget; checkpoint resume path)
	StateWaitingHuman:    {StateRunning: true, StateFailed: true, StateBlocked: true, StateCancelled: true},
	StateSuspended:       {StateRunning: true, StateWaitingHuman: true, StateFailed: true, StateBlocked: true, StateCancelled: true},
	StateBudgetExhausted: {StateSuspended: true, StateFailed: true, StateCancelled: true},
}

// missionOnlyStates may only be entered by Works carrying a full mission
// contract (ADR-0008). CI Works must never emit them (freeze law).
var missionOnlyStates = map[State]bool{
	StateWaitingHuman:    true,
	StateSuspended:       true,
	StateBudgetExhausted: true,
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
// RuntimeSpec declares an OCI container image for the node. When
// set, the worker executes the node's `run` command inside the
// container via the internal/sandbox.Docker backend (slice 5). When
// empty, the worker falls back to the slice-1+2 host-subprocess
// path with hermetic defaults from internal/sandbox.Hermetic.
//
// The Docker backend enforces the same hermetic guarantees as the
// host path: --read-only, --cap-drop=ALL, --network=none, no-new-
// privileges, memory + CPU + PIDs caps. See internal/sandbox/docker.go.
type RuntimeSpec struct {
	// Image is the OCI image reference (e.g. "alpine:3.20",
	// "ghcr.io/org/app@sha256:..."). The worker calls `docker pull`
	// before creating the container.
	Image string `json:"image,omitempty"`
}

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

	// Runtime is the optional OCI container spec. Slice 5 added this
	// so a node can declare an image and have the worker run it
	// hermetically in Docker instead of on the host.
	Runtime RuntimeSpec `json:"runtime,omitempty"`

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
//
// M1 (k-impl-018/019/021) extends this with explicit SHA / clone /
// HTML fields that the worker uses to check out the exact commit
// and the publisher uses to mint Check Runs. ProductionAccess
// flows through Policy, not Source — keep these two surfaces
// separate.
type Source struct {
	Type       string `json:"type"` // cli, github_pull_request, github_push, schedule, api
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Actor      string `json:"actor,omitempty"`

	// M1 — GitHub-source provenance (k-impl-018 / k-impl-019).
	// All optional; legacy Works that predate M1 have none of these.
	Branch  string `json:"branch,omitempty"`  // refs/heads/main → main
	SHA     string `json:"sha,omitempty"`      // 40-char commit SHA
	HTMLURL string `json:"html_url,omitempty"` // browser-friendly repo URL
	CloneURL string `json:"clone_url,omitempty"` // git clone URL (head fork for PRs)

	// PR-specific (k-impl-018). Zero for push events.
	PRNumber int `json:"pr_number,omitempty"`
	PRAction string `json:"pr_action,omitempty"` // opened, synchronize, reopened
	PRHead   string `json:"pr_head,omitempty"`   // source branch
	PRBase   string `json:"pr_base,omitempty"`   // target branch
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

	// Pool (BYOC, RFC-0004): when set, only runners whose labels
	// contain "pool:<Pool>" may execute this work. This is the
	// bring-your-own-compute isolation boundary: a design partner's
	// work runs exclusively on workers they registered. Empty pool =
	// no constraint (any active runner is eligible).
	Pool string `json:"pool,omitempty"`
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
	// G1: integrity-hash (SHA-256 over identitet+udfald, Details ekskluderet).
	// Saettes af Seal(); tom = unsealed (legacy). Verificeres af TG consumers.
	Hash        string         `json:"hash,omitempty"`
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

// MissionContract carries the frozen ADR-0008 fields that make a Work a
// mission: a priced, verifiable, purpose-bound process. All fields are
// optional on the wire for CI Works; ValidateMissionWork requires them.
type BudgetCeiling struct {
	ComputeEUR float64 `json:"compute_eur"`   // hard compute-cost stop (EUR)
	WallClockH float64 `json:"wall_clock_h"`  // hard wall-clock stop (hours)
}

type VerificationCriterion struct {
	Criterion string `json:"criterion"`                  // human/CI-checkable statement
	Kind      string `json:"kind,omitempty"`             // deterministic | human_review
}

type MissionContract struct {
	BudgetCeiling   *BudgetCeiling          `json:"budget_ceiling,omitempty"`
	Verification    []VerificationCriterion `json:"verification,omitempty"`
	PurposeBindings []string                `json:"purpose_bindings,omitempty"`
	KillSwitch      string                  `json:"kill_switch,omitempty"` // always | policy
}

// Handoff is the frozen 5-layer checkpoint payload (ADR-0010,
// handoff.schema/1.0). PayloadSchema versions the content; the kernel owns
// the shape, agents fill it in. A handoff is written only on kernel-
// recognized suspend/wait/fail transitions — never free-floating.
type Handoff struct {
	StateSnapshot map[string]any `json:"state_snapshot"` // typed, validated current values
	Narrative     string         `json:"narrative"`      // why the state looks like this
	DecisionLog   []string       `json:"decision_log"`   // what was decided, deferred, why
	PriorityQueue []string       `json:"priority_queue"` // next session's first/second/third
	Warnings      []string       `json:"warnings"`       // gotchas: rate limits, env issues
	PayloadSchema string         `json:"payload_schema"` // e.g. "handoff/1.0"
}

// HandoffVersion is the frozen payload-schema version this kernel writes.
const HandoffVersion = "handoff/1.0"

// ValidateHandoff checks the 5-layer shape (fail-closed: an invalid handoff
// is never persisted and never silently resumed from — ADR-0010).
func ValidateHandoff(h *Handoff) error {
	if h == nil {
		return errors.New("handoff is required")
	}
	if h.StateSnapshot == nil {
		return errors.New("handoff.state_snapshot is required")
	}
	if strings.TrimSpace(h.Narrative) == "" {
		return errors.New("handoff.narrative is required")
	}
	if h.PayloadSchema == "" {
		h.PayloadSchema = HandoffVersion
	}
	if h.PayloadSchema != HandoffVersion {
		return fmt.Errorf("handoff.payload_schema %q unsupported (kernel speaks %s)", h.PayloadSchema, HandoffVersion)
	}
	return nil
}

// BudgetLedger is the kernel.metering view of a mission Work's budget
// (kernel.budget/1.0): reserved at lease time, consumed continuously,
// clock paused exactly when the Work is WAITING_HUMAN (human-syscall wait),
// stopped on hard stop. Late provider bills after teardown are recorded but
// never push user-visible consumption past the ceiling (operator absorbs).
type BudgetLedger struct {
	WorkID         string        `json:"work_id"`
	Ceiling        BudgetCeiling `json:"ceiling"`
	Reserved       float64       `json:"reserved"`
	Consumed       float64       `json:"consumed"`
	ClockState     string        `json:"clock_state"` // RUNNING | PAUSED_WAITING_HUMAN | STOPPED
	HardStop       string        `json:"hard_stop,omitempty"` // wall_clock | compute | none
	LateBillEntries []LateBill   `json:"late_bill_entries,omitempty"`
}

type LateBill struct {
	AmountEUR float64 `json:"amount_eur"`
	Reason    string  `json:"reason"`
}

// CanReserve reports whether reserving delta on top of current reservations
// keeps the mission within its ceiling (ADR-0009: sum(reserved) <= ceiling).
func (b *BudgetLedger) CanReserve(delta float64) bool {
	return b.Reserved+delta <= b.Ceiling.ComputeEUR+1e-9
}

// Consume applies actual usage. It clamps at the ceiling and flags the hard
// stop — it never reports consumption beyond what the operator committed to.
// A stopped clock never meters (hard-stop race law). A paused clock
// (WAITING_HUMAN) never meters either — but cannot trigger a hard stop.
func (b *BudgetLedger) Consume(delta float64) (exceeded bool) {
	switch b.ClockState {
	case "STOPPED":
		return false
	case "PAUSED_WAITING_HUMAN":
		return false // fair billing: clock paused, no metering at all
	}
	if b.Consumed+delta >= b.Ceiling.ComputeEUR {
		b.Consumed = b.Ceiling.ComputeEUR
		b.ClockState = "STOPPED"
		b.HardStop = "compute"
		return true
	}
	b.Consumed += delta
	return false
}

// PauseClock pauses the metering clock for WAITING_HUMAN (ADR-0009: fair
// billing — the budget clock runs only under active execution). Only the
// kernel calls this on the kernel- recognized transition; an agent's own
// claim of waiting has no clock effect (anti-abuse law from the freeze).
func (b *BudgetLedger) PauseClock() {
	if b.ClockState == "RUNNING" {
		b.ClockState = "PAUSED_WAITING_HUMAN"
	}
}

// ResumeClock restarts metering after a human syscall approval.
func (b *BudgetLedger) ResumeClock() {
	if b.ClockState == "PAUSED_WAITING_HUMAN" {
		b.ClockState = "RUNNING"
	}
}

// RecordLateBill registers provider billing that arrived after teardown.
// It is evidence-class data; it never re-opens the clock or breaches the
// operator's committed ceiling.
func (b *BudgetLedger) RecordLateBill(amount float64, reason string) {
	b.LateBillEntries = append(b.LateBillEntries, LateBill{AmountEUR: amount, Reason: reason})
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

	// k-mission-01 (ADR-0008): mission contract fields. Empty Mission ==
	// legacy CI Work with frozen behavior.
	Mission *MissionContract `json:"mission,omitempty"`
}

// SuspendWithHandoff atomically moves a mission Work into `to` (WAITING_HUMAN
// or SUSPENDED) and records the checkpoint handoff in one step. Only the
// kernel (store layer) may call this; the handoff IS the state transition's
// evidence — a suspend without a handoff cannot happen (ADR-0010).
//
//go:noinline // kept as a method so callers read: work.SuspendWithHandoff(...)
func (w *Work) SuspendFields(to State) error {
	if to != StateWaitingHuman && to != StateSuspended {
		return fmt.Errorf("%w: handoff suspend requires %s or %s, got %s",
			ErrInvalidTransition, StateWaitingHuman, StateSuspended, to)
	}
	return w.ValidateTransition(to)
}

// IsMission reports whether this Work carries the mission contract
// (ADR-0008: Mission = Work with contract fields filled).
func (w *Work) IsMission() bool {
	return w.Mission != nil && w.Mission.BudgetCeiling != nil
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

// ValidateMissionWork enforces the ADR-0008 fail-closed rule: a Work that
// declares itself a mission (or tries to enter mission-only states) must
// carry the complete contract. CI Works (nil Mission) keep frozen behavior.
func (w *Work) ValidateMissionWork() error {
	if !w.IsMission() {
		// A CI Work must never enter mission-only states (freeze law).
		if missionOnlyStates[w.State] {
			return fmt.Errorf("work %s: state %s requires a mission contract (ADR-0008)", w.ID, w.State)
		}
		return nil
	}
	m := w.Mission
	if m.BudgetCeiling.ComputeEUR <= 0 && m.BudgetCeiling.WallClockH <= 0 {
		return errors.New("mission.budget_ceiling must set compute_eur or wall_clock_h")
	}
	if m.BudgetCeiling.ComputeEUR < 0 || m.BudgetCeiling.WallClockH < 0 {
		return errors.New("mission.budget_ceiling values must be non-negative")
	}
	if len(m.Verification) == 0 {
		return errors.New("mission.verification must contain at least one criterion")
	}
	for i, v := range m.Verification {
		if v.Criterion == "" {
			return fmt.Errorf("mission.verification[%d]: criterion is required", i)
		}
		switch v.Kind {
		case "", "deterministic", "human_review":
		default:
			return fmt.Errorf("mission.verification[%d]: unknown kind %q", i, v.Kind)
		}
	}
	switch m.KillSwitch {
	case "always", "policy":
	default:
		return errors.New("mission.kill_switch must be \"always\" or \"policy\"")
	}
	return nil
}

// ValidateTransition checks that moving w.State from `to` is permitted.
func (w *Work) ValidateTransition(to State) error {
	if !CanTransition(w.State, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, w.State, to)
	}
	// Freeze law: mission-only states require the mission contract.
	if missionOnlyStates[to] && !w.IsMission() {
		return fmt.Errorf("%w: %s -> %s requires a mission contract (ADR-0008)",
			ErrInvalidTransition, w.State, to)
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
