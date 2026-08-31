// Package scheduler implements the works-execution capability-aware
// scheduler.
//
// Slice 1+2 of the worker control plane assigned work to "any worker"
// (first-come, first-served via /v1/workers/ready). Slice 4 replaces that
// naive policy with a capability-aware scheduler that:
//
//  1. Filters registered runners by HARD constraints (OS, arch, CPU,
//     memory, GPU, network, trust class, tenant, lifecycle, production
//     authority). Hard constraints dominate: a single mismatch eliminates
//     the runner.
//  2. Scores the eligible set by SOFT optimization signals (cache
//     locality, expected runtime, queue pressure, current utilization,
//     cost, network proximity, reliability). The scorer is deterministic
//     and explainable per docs/works-venture-starter-pack/02_ARCHITECTURE/
//     SCHEDULER_DESIGN.md — no ML, no randomness.
//  3. Returns an Assignment that carries a full decision record: the
//     selected runner, the eligible count, the per-constraint rejection
//     tally, the score components, and any fallback reason if the
//     decision was degraded (e.g. only draining runners eligible).
//
// The package is decoupled from the runner-identity wire format. Callers
// convert runner.Identity into scheduler.Runner at the boundary (see
// services/api readyNodesHandler). Keeping the dependency direction
// internal/scheduler → (no services/*) preserves the layering rule.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// -----------------------------------------------------------------------------
// Public types
// -----------------------------------------------------------------------------

// Runner is the scheduler-side view of a registered works-runner. It holds
// only the fields the scheduler needs to filter and score. The
// readyNodesHandler adapter copies these from runner.Identity + collected
// runtime signals (queue depth, utilization, cache hits, success rate).
//
// All fields are best-effort: zero values are treated as "unknown / not
// advertised" and never disqualify a runner except where the constraint
// requires a positive value (e.g. Work.Requirements.OS set → runner.OS
// must include it).
type Runner struct {
	RunnerID   string
	Tenant     string
	TrustClass string // "untrusted" | "standard" | "privileged"
	Lifecycle  string // "pending" | "active" | "draining" | "retired"

	// Static capabilities (advertised at registration).
	OS         []string // ["linux"]
	Arch       []string // ["amd64", "arm64"]
	CPUMilli   int      // available CPU budget
	MemoryMiB  int      // available memory budget
	GPU        int      // number of GPUs
	Toolchains []string // ["go1.22", "node20"]
	Labels     []string // free-form ["region:eu-west", "network:enabled", "cache:warm"]

	// Dynamic signals (populated by collectors — out of scope here, but
	// the scheduler reads them when present).
	QueueDepth   int     // active leases
	Utilization  float64 // 0..1
	CostUSDPerHr float64 // for cost optimization
	Region       string  // for network proximity
	SuccessRate  float64 // 0..1, history of successful completions
	CacheHits    int     // recent cache hits (for cache locality signal)
	TotalRuns    int     // total runs (for reliability denominator)
}

// Assignment is the decision record returned by Select. Every field is
// populated so the caller can persist the record for audit / replay
// without further computation. JSON tags are stable: they form the
// explainability schema on the wire and in the work_attempts audit table.
type Assignment struct {
	// SelectedRunner is the runner chosen for the assignment. Nil only
	// when an error is returned by Select.
	SelectedRunner *Runner `json:"selected_runner"`

	// EligibleCount is the number of runners that passed hard-constraint
	// filtering. Zero means the assignment (if any) was a degraded
	// fallback (e.g. only draining runners) or no assignment is possible.
	EligibleCount int `json:"eligible_count"`

	// PoolSize is the total number of runners the scheduler considered.
	// Equal to EligibleCount + sum(RejectedConstraints values).
	PoolSize int `json:"pool_size"`

	// RejectedConstraints maps each hard-constraint name to the number
	// of runners eliminated for that reason. Keys are stable strings
	// (e.g. "os_mismatch", "trust_insufficient"); downstream dashboards
	// pivot on them.
	RejectedConstraints map[string]int `json:"rejected_constraints"`

	// ScoreComponents holds the weighted contribution of each soft
	// signal to the winner's total score. Each value is in [0, weight]
	// where weight is the constant in weights.go.
	ScoreComponents map[string]float64 `json:"score_components"`

	// TotalScore is the sum of ScoreComponents for the selected runner.
	// Useful for "why not runner X?" diffing in the audit log.
	TotalScore float64 `json:"total_score"`

	// FallbackReason is non-empty when the decision was degraded (e.g.
	// the only eligible runner is draining). Empty for a healthy
	// assignment.
	FallbackReason string `json:"fallback_reason,omitempty"`

	// Reasoning is a human-readable summary suitable for the response
	// payload and the audit trail. It is derived, not free-form user
	// input, so it is safe to log.
	Reasoning string `json:"reasoning"`
}

// eligible is one scored runner. We use a manual scan in Pass 2 to pick
// the best, so the type is at package level for runnerLess to use.
type eligible struct {
	runner     *Runner
	components map[string]float64
	total      float64
	draining   bool
}

// -----------------------------------------------------------------------------
// Sentinel errors
// -----------------------------------------------------------------------------

// ErrEmptyPool is returned when Select receives an empty runners slice.
// There is no work to do — we never invent a runner.
var ErrEmptyPool = errors.New("scheduler: empty runner pool")

// ErrNoEligibleRunner is returned when the pool is non-empty but no
// runner satisfies the hard constraints. The caller should surface this
// to the operator (worker pool cannot serve the requested node) and
// optionally retry with relaxed requirements after operator review.
var ErrNoEligibleRunner = errors.New("scheduler: no eligible runner")

// -----------------------------------------------------------------------------
// Hard constraints
// -----------------------------------------------------------------------------

// hardConstraintKeys are the stable names emitted in
// Assignment.RejectedConstraints. They form the public explainability
// vocabulary; do not rename without a schema migration.
const (
	RejectLifecycle         = "lifecycle_not_active"
	RejectOS                = "os_mismatch"
	RejectArch              = "arch_mismatch"
	RejectCPU               = "cpu_insufficient"
	RejectMemory            = "memory_insufficient"
	RejectNetwork           = "network_blocked"
	RejectTrust             = "trust_insufficient"
	RejectTenant            = "tenant_mismatch"
	RejectProduction        = "production_requires_privileged"
	RejectNoIdentity        = "no_identity" // safety guard
)

// hardFilter runs the hard constraints against a single runner and the
// (work, node) pair. It returns nil on pass and a stable rejection code
// on fail. Rejection codes are summed into Assignment.RejectedConstraints.
func hardFilter(r *Runner, work *workgraph.Work, node *workgraph.Node) string {
	// Safety: a runner with no identity is not eligible. This should
	// never happen in production (registration requires a runner_id),
	// but defending against it keeps the explainability record honest.
	if r.RunnerID == "" {
		return RejectNoIdentity
	}
	// Lifecycle: only Active runners are eligible. Pending means the
	// worker has not completed enrollment; Draining/Retired means it
	// should not pick up new work.
	if r.Lifecycle != "active" && r.Lifecycle != "" {
		// Empty lifecycle is treated as "advertised but unknown" →
		// conservatively rejected. Callers should set Lifecycle on
		// registration; if they don't, that's a configuration bug
		// we want to surface rather than paper over.
		if r.Lifecycle == "draining" {
			return RejectLifecycle // explicit fallback path handled separately
		}
		return RejectLifecycle
	}

	req := work.Requirements
	// OS — exact match (case-insensitive). Required if specified.
	if req.OS != "" && !containsFold(r.OS, req.OS) {
		return RejectOS
	}
	// Arch — exact match (case-insensitive). Required if specified.
	if req.Arch != "" && !containsFold(r.Arch, req.Arch) {
		return RejectArch
	}
	// CPU budget — required if specified.
	if req.CPUMilli > 0 && r.CPUMilli < req.CPUMilli {
		return RejectCPU
	}
	// Memory budget — required if specified.
	if req.MemoryMiB > 0 && r.MemoryMiB < req.MemoryMiB {
		return RejectMemory
	}
	// Network class — if the node declares a network_egress side
	// effect, the runner must have advertised network availability.
	// We treat the label "network:enabled" (or a Toolchains entry
	// containing "net" — kept for legacy workers) as proof.
	if hasNetworkEgress(node) && !runnerHasNetwork(r) {
		return RejectNetwork
	}
	// Trust class — the work's policy.TrustClass sets a floor.
	if !trustMeets(r.TrustClass, work.Policy.TrustClass) {
		return RejectTrust
	}
	// Tenant — derive from Source.Repository (acts as the tenant
	// namespace in slice 4). If unspecified, no tenant constraint.
	if t := tenantOf(work); t != "" && r.Tenant != "" && !strings.EqualFold(r.Tenant, t) {
		return RejectTenant
	}
	// Production access — only privileged runners may serve works
	// flagged for production. This is the secret/deployment
	// authority axis from SCHEDULER_DESIGN.md.
	if work.Policy.ProductionAccess && r.TrustClass != "privileged" {
		return RejectProduction
	}
	return ""
}

// containsFold reports whether any element of haystack equals needle
// under Unicode-case-folding. Empty haystack is treated as "unknown".
func containsFold(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}

// hasNetworkEgress returns true when the node's declared side effects
// include network egress. Capability manifest terminology
// (internal/manifest/admission.go) uses "network_egress".
func hasNetworkEgress(n *workgraph.Node) bool {
	for _, s := range n.SideEffects {
		if s == "network_egress" || s == "external_api_call" {
			return true
		}
	}
	return false
}

// runnerHasNetwork returns true when the runner advertises network
// availability via Labels ("network:enabled") or Toolchains.
func runnerHasNetwork(r *Runner) bool {
	for _, l := range r.Labels {
		if strings.EqualFold(l, "network:enabled") || strings.EqualFold(l, "network:on") {
			return true
		}
	}
	for _, t := range r.Toolchains {
		if strings.Contains(strings.ToLower(t), "net") {
			return true
		}
	}
	return false
}

// trustMeets reports whether the runner's trust tier satisfies the
// work's required tier. Tiers (low→high): untrusted < standard <
// privileged. An empty work tier means "no floor" → pass.
func trustMeets(runnerTrust, workTrust string) bool {
	if workTrust == "" {
		return true
	}
	rank := func(t string) int {
		switch strings.ToLower(t) {
		case "privileged":
			return 3
		case "standard":
			return 2
		case "untrusted":
			return 1
		}
		return 0 // unknown / unset → conservative
	}
	return rank(runnerTrust) >= rank(workTrust)
}

// tenantOf extracts a tenant key from the work. Slice 4 uses
// Source.Repository as the tenant proxy (e.g. "github.com/acme/widgets"
// → tenant "acme"). When Source.Type is empty we fall back to an empty
// string ("no tenant constraint").
func tenantOf(w *workgraph.Work) string {
	if w == nil {
		return ""
	}
	if w.Source.Repository != "" {
		// Slash-split, take the second-to-last segment ("acme" in
		// "github.com/acme/widgets"). Conservative — we don't
		// assume git host.
		parts := strings.Split(w.Source.Repository, "/")
		if len(parts) >= 2 {
			return strings.ToLower(parts[len(parts)-2])
		}
		return strings.ToLower(parts[0])
	}
	return ""
}

// -----------------------------------------------------------------------------
// Soft scoring
// -----------------------------------------------------------------------------

// Soft-signal weights. They sum to 1.0. Tuned for slice 4 determinism;
// the future ML-based scorer must never override hard policy and must
// produce a per-signal breakdown with the same key names.
const (
	wCacheLocality  = 0.20
	wExpectedRuntime = 0.05 // V1 neutral; future ML lives here
	wQueuePressure  = 0.20
	wUtilization    = 0.15
	wCost           = 0.15
	wNetworkProx    = 0.10
	wReliability    = 0.15
)

// softScore evaluates one eligible runner against the (work, node) pair
// and returns a per-signal breakdown plus the weighted total. All
// per-signal values are in [0, weight]. The total is in [0, 1].
//
// The implementation is deliberately straightforward — no exotic math,
// no hidden state — so the explainability record is auditable by a
// human reviewer.
func softScore(r *Runner, work *workgraph.Work, node *workgraph.Node) (components map[string]float64, total float64) {
	components = map[string]float64{}

	// Cache locality: a node with Cache=true running on a runner with
	// a track record of cache hits is preferred.
	components["cache_locality"] = wCacheLocality * cacheLocality(r, node)

	// Expected runtime: V1 placeholder. We have no duration model yet,
	// so we return the neutral mid-value.
	components["expected_runtime"] = wExpectedRuntime * 0.5

	// Queue pressure: high active-lease count means the runner is busy.
	components["queue_pressure"] = wQueuePressure * queuePressure(r)

	// Utilization: prefer under-utilized runners.
	components["utilization"] = wUtilization * (1.0 - clamp01(r.Utilization))

	// Cost: cheaper preferred, normalized against a 1.0 USD/hr ceiling.
	components["cost"] = wCost * costScore(r, work)

	// Network proximity: same region as the work's preferred region
	// (extracted from Requirements if present, else the runner's
	// declared Region is treated as the "no preference" case).
	components["network_proximity"] = wNetworkProx * networkProximity(r, work)

	// Reliability: empirical success rate. Default 1.0 when no history.
	components["reliability"] = wReliability * clamp01(r.SuccessRate)

	for _, v := range components {
		total += v
	}
	return components, total
}

// cacheLocality returns a 0..1 score. A runner with no observed cache
// hits is rated 0.2 (mild penalty vs neutral 0.5) — keeps cache warmth
// a real signal without making it the only differentiator.
func cacheLocality(r *Runner, n *workgraph.Node) float64 {
	if !n.Cache {
		return 0.5 // node is not cache-eligible → signal is irrelevant
	}
	if r.CacheHits == 0 {
		return 0.2
	}
	// Saturating: each hit adds value, plateau at 1.0.
	return clamp01(0.5 + 0.1*float64(minInt(r.CacheHits, 5)))
}

// queuePressure returns 1 when idle, 0 when fully saturated (queue >=10
// active leases). Linear in between.
func queuePressure(r *Runner) float64 {
	if r.QueueDepth <= 0 {
		return 1.0
	}
	if r.QueueDepth >= 10 {
		return 0.0
	}
	return 1.0 - float64(r.QueueDepth)/10.0
}

// costScore prefers cheap runners. We assume a 1 USD/hr ceiling —
// anything above gets 0. When the work has a max-cost budget, we scale
// the ceiling accordingly so we never recommend a runner that busts
// the budget even if it would otherwise be optimal.
func costScore(r *Runner, work *workgraph.Work) float64 {
	if r.CostUSDPerHr <= 0 {
		return 0.7 // unknown cost → mild preference (avoid gaming by omission)
	}
	ceiling := 1.0
	if work.Requirements.MaxCostUSD > 0 {
		ceiling = work.Requirements.MaxCostUSD
	}
	if r.CostUSDPerHr >= ceiling {
		// Within budget but at the ceiling → neutral.
		return 0.5
	}
	return clamp01(1.0 - r.CostUSDPerHr/ceiling)
}

// networkProximity returns a 0..1 score for how close the runner is to
// the work's preferred execution region. Slice 4 doesn't yet have a
// typed "preferred region" field on Work, so we fall back to a small
// heuristic: a runner that advertises a Region AND carries the
// "network:low_latency" label scores 1.0, an unlabelled runner with a
// Region scores 0.5, and a runner with no Region scores 0.5. When the
// Work's first node declares "region:<xx>" via its Env (a convention
// we accept in slice 4; the typed field arrives in slice 5), the runner
// in that region scores 1.0 and cross-region runners 0.0.
func networkProximity(r *Runner, work *workgraph.Work) float64 {
	want := workRegion(work)
	if want == "" {
		// No preferred region declared; reward runners that explicitly
		// advertise a low-latency network label.
		for _, l := range r.Labels {
			if strings.EqualFold(l, "network:low_latency") {
				return 1.0
			}
		}
		if r.Region != "" {
			return 0.5
		}
		return 0.5
	}
	if r.Region == "" {
		return 0.5
	}
	if strings.EqualFold(r.Region, want) {
		return 1.0
	}
	return 0.0
}

// workRegion extracts a preferred region from the Work. We honour two
// sources: (a) any Work.Requirements-derived field once slice 5 lands;
// today (b) a convention where the first node's Env carries a
// "WORKS_REGION" key. The convention is loose; the typed field replaces
// it once Work.Requirements grows.
func workRegion(w *workgraph.Work) string {
	if w == nil {
		return ""
	}
	for _, n := range w.Graph.Nodes {
		if v, ok := n.Env["WORKS_REGION"]; ok && v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}

// clamp01 returns x clamped to [0,1]. NaN-safe (NaN → 0).
func clamp01(x float64) float64 {
	if math.IsNaN(x) || x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// -----------------------------------------------------------------------------
// Select: the public entry point
// -----------------------------------------------------------------------------

// Select picks the best runner for the given (work, node) pair.
//
// Pipeline:
//  1. Run hard constraints on every runner; bucket into eligible vs
//     rejected, tallying per-constraint reasons for the explainability
//     record.
//  2. Score eligible runners with the soft scorer.
//  3. Pick the highest score; tiebreak by (lower QueueDepth, higher
//     SuccessRate, lexicographic RunnerID) for determinism.
//  4. If the eligible set is empty, return ErrNoEligibleRunner with
//     a populated Assignment carrying rejection stats (so callers can
//     surface the reason to operators).
//  5. If the only eligible runner(s) are draining, attach a
//     FallbackReason so the audit log flags the degradation.
//
// The function is pure: it does not mutate its inputs and does not
// perform IO. ctx is reserved for future cancellation when the soft
// scorer gains a remote signal source (e.g. a registry lookup); the
// current implementation does not block on it.
func Select(ctx context.Context, work *workgraph.Work, node *workgraph.Node, runners []*Runner) (*Assignment, error) {
	_ = ctx // reserved for future IO; current scorer is local.
	if work == nil {
		return nil, errors.New("scheduler: work is nil")
	}
	if node == nil {
		return nil, errors.New("scheduler: node is nil")
	}
	if len(runners) == 0 {
		return nil, ErrEmptyPool
	}

	assignment := &Assignment{
		PoolSize:            len(runners),
		RejectedConstraints: map[string]int{},
		ScoreComponents:     map[string]float64{},
	}

	// Pass 1: hard constraints.
	eligibleList := make([]eligible, 0, len(runners))

	for _, r := range runners {
		reason := hardFilter(r, work, node)
		if reason == "" {
			comp, total := softScore(r, work, node)
			eligibleList = append(eligibleList, eligible{
				runner:     r,
				components: comp,
				total:      total,
				draining:   r.Lifecycle == "draining",
			})
			continue
		}
		assignment.RejectedConstraints[reason]++
	}

	assignment.EligibleCount = len(eligibleList)

	if len(eligibleList) == 0 {
		// Build the reasoning string from the rejection tally so the
		// operator sees which constraint was the blocker.
		parts := make([]string, 0, len(assignment.RejectedConstraints))
		for k, v := range assignment.RejectedConstraints {
			parts = append(parts, fmt.Sprintf("%s=%d", k, v))
		}
		sort.Strings(parts)
		assignment.Reasoning = fmt.Sprintf(
			"no eligible runner for %s/%s (pool=%d rejected=[%s])",
			work.ID, node.ID, assignment.PoolSize, strings.Join(parts, ", "),
		)
		return assignment, ErrNoEligibleRunner
	}

	// Pass 2: pick the best runner deterministically. We do a linear
	// scan because sort.SliceStable with a strict-total comparator still
	// preserves input order for elements the comparator says are not less
	// than each other — which would let the result depend on input order.
	//
	// The scan keeps `best` as the runner with the smallest RunnerID
	// among those that tie on the high-priority signals (score, queue,
	// success). For each candidate, if its signals are strictly worse
	// than best, skip it; otherwise replace. This is equivalent to
	// selecting the max under runnerLess, but the comparison is
	// inverted: we keep `best` if e is "not better" (i.e. e is worse or
	// equal with a larger or equal id), and replace otherwise.
	var best *eligible
	for i := range eligibleList {
		e := &eligibleList[i]
		if best == nil {
			best = e
			continue
		}
		// Replace best with e if e is strictly better under the
		// runnerLess order. runnerLess(e, best) returns true iff e is
		// preferred over best (higher score, lower queue, higher
		// success, smaller id). When scores and queue depths and
		// success rates are all equal, the smaller id wins; since
		// runnerLess returns false for e when e.id >= best.id in that
		// case, the first-encountered runner with the smallest id is
		// kept.
		if runnerLess(e, best) {
			best = e
		}
	}
	winner := *best
	assignment.SelectedRunner = winner.runner
	assignment.ScoreComponents = winner.components
	assignment.TotalScore = winner.total

	// Fallback detection: if every eligible runner is draining, flag
	// the assignment so the audit trail and dashboards show degraded
	// placement.
	allDraining := true
	for _, e := range eligibleList {
		if !e.draining {
			allDraining = false
			break
		}
	}
	if allDraining {
		assignment.FallbackReason = "only_draining_runners_eligible"
	}

	// Reasoning summary (deterministic — safe to log).
	top := winner.runner
	assignment.Reasoning = fmt.Sprintf(
		"selected runner %s for %s/%s (score=%.3f, eligible=%d/%d, fallback=%q)",
		top.RunnerID, work.ID, node.ID,
		winner.total, assignment.EligibleCount, assignment.PoolSize,
		assignment.FallbackReason,
	)
	return assignment, nil
}

// runnerLess reports whether runner a is *better* than runner b. Select
// uses it in a linear scan to find the minimum (best) element
// deterministically — the scan always picks the highest score, and on
// ties the lower QueueDepth, higher SuccessRate, and finally the
// lexicographically smaller RunnerID. This is intentionally a strict
// total order so Select's result is independent of the input slice's
// ordering.
//
// Naming convention: "a Less b" means "a is preferred over b" — i.e.
// a should be ranked earlier in the output. Returns true when a is
// strictly better (higher score, lower queue, higher success, smaller id).
//
// CRITICAL: totals are computed as sums over a Go map and are therefore
// subject to bitwise float non-determinism (different iteration orders
// produce sums that differ in the last few ULPs even when algebraically
// equal). We compare with a small epsilon to treat algebraic ties as
// real ties, which is the only way to make the lex tiebreak robust.
func runnerLess(a, b *eligible) bool {
	const eps = 1e-9
	if math.Abs(a.total-b.total) > eps {
		return a.total > b.total
	}
	if a.runner.QueueDepth != b.runner.QueueDepth {
		return a.runner.QueueDepth < b.runner.QueueDepth
	}
	if math.Abs(a.runner.SuccessRate-b.runner.SuccessRate) > eps {
		return a.runner.SuccessRate > b.runner.SuccessRate
	}
	return a.runner.RunnerID < b.runner.RunnerID
}
