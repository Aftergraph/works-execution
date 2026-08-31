package api

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/runner"
)

// DecisionInput is the policy input shape. The API layer constructs this
// before each policy evaluation. See policies/lease_grant.rego for the
// authoritative schema and field comments.
type DecisionInput struct {
	Request  RequestContext         `json:"request"`
	Work     WorkView               `json:"work"`
	Evidence []workgraph.Evidence  `json:"evidence"`
	Runner   RunnerView             `json:"runner"`
}

// RequestContext identifies the verb + target of the action.
type RequestContext struct {
	Action   string `json:"action"`
	WorkID   string `json:"work_id"`
	NodeID   string `json:"node_id"`
	WorkerID string `json:"worker_id"`
}

// WorkView is the subset of workgraph.Work the policy engine needs.
type WorkView struct {
	ID     string           `json:"id"`
	Policy workgraph.Policy `json:"policy"`
	State  workgraph.State  `json:"state"`
}

// RunnerView is the subset of the runner identity record the policy engine
// needs. Mirrors services/runner/registry.go.
type RunnerView struct {
	RunnerID       string                `json:"runner_id"`
	TrustClass     runner.TrustClass     `json:"trust_class"`
	LifecycleState runner.LifecycleState `json:"lifecycle_state"`
}

// Decision is what the evaluator returns. Reason codes are stable strings
// defined in the Rego bundle (e.g. "missing_approved_evidence"); the API
// maps them to HTTP 403 error codes.
type Decision struct {
	Allow         bool     `json:"allow"`
	DenyReasons   []string `json:"deny_reasons"`
	RequiredTrust string   `json:"required_trust"`
	BundleVersion string   `json:"bundle_version"`
}

// Reason codes — exported so leases.go and any future caller can map a
// deny reason to a stable HTTP error code.
const (
	ReasonMissingApprovedEvidence  = "missing_approved_evidence"
	ReasonRunnerTrustBelowFloor    = "runner_trust_below_floor"
	ReasonRunnerNotActive          = "runner_not_active"
	ReasonProductionAccessDenied   = "production_access_denied"
)

// formatDenyReason maps a deny reason (from a Decision.DenyReasons entry)
// to a short, stable HTTP error code. Falls back to "policy_denied" when
// the reason is unrecognized (forward-compatibility for future bundles).
func formatDenyReason(reason string) string {
	switch reason {
	case ReasonMissingApprovedEvidence:
		return "policy_missing_approved_evidence"
	case ReasonRunnerTrustBelowFloor:
		return "policy_runner_trust_below_floor"
	case ReasonRunnerNotActive:
		return "policy_runner_not_active"
	case ReasonProductionAccessDenied:
		return "policy_production_access_denied"
	}
	return "policy_denied"
}

// Engine evaluates the lease_grant policy bundle. In V1 it is a direct Go
// translation of the Rego file at policies/lease_grant.rego (the file
// remains the authoritative source of policy logic for human review and
// future OPA migration). Slice 5+ may swap in github.com/open-policy-agent/opa
// without changing this public surface.
//
// The Engine is safe for concurrent use after construction; Evaluate holds
// no internal state.
type Engine struct {
	bundleVersion string
}

// NewEngine constructs an Engine. In V1 the bundle version is fixed at
// "v1.0.0" to match the published Rego bundle. A future flag or env var
// could be wired here to support bundle upgrades.
func NewEngine(_ string) (*Engine, error) {
	return &Engine{bundleVersion: "v1.0.0"}, nil
}

// LoadBundle reads the Rego file from disk and returns an Engine. The Rego
// source is intentionally not parsed in V1 — it is documentation; the
// runtime is the Go evaluator below. The presence of the file is checked
// so misconfigured deployments fail loudly.
func LoadBundle(path string) (*Engine, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("policy bundle not found at %s: %w", path, err)
	}
	return NewEngine("")
}

// BundleVersion returns the bundle version this Engine implements.
func (e *Engine) BundleVersion() string { return e.bundleVersion }

// Evaluate applies the lease_grant rules to the input. The rules are a
// direct Go translation of policies/lease_grant.rego:
//
//	default allow = false
//	default deny_reasons = []
//	required_trust := input.work.policy.trust_class
//	allow if input.work.policy.production_access == false
//	allow if all of:
//	  production_access == true
//	  any_approved_evidence
//	  trust_meets_floor(runner, floor)
//	  runner_is_active
//	deny_reasons contains "missing_approved_evidence" if production_access && !any_approved_evidence
//	deny_reasons contains "runner_trust_below_floor" if production && approved && !trust_ok
//	deny_reasons contains "runner_not_active" if production && approved && trust_ok && !active
//	deny_reasons contains "production_access_denied" (catch-all) if production && no reasons yet
func (e *Engine) Evaluate(_ context.Context, in DecisionInput) (Decision, error) {
	dec := Decision{
		BundleVersion: e.bundleVersion,
		RequiredTrust: string(in.Work.Policy.TrustClass),
	}

	if !in.Work.Policy.ProductionAccess {
		dec.Allow = true
		return dec, nil
	}

	// Production path.
	approved := anyApprovedEvidence(in.Evidence)
	trustOK := trustMeetsFloor(string(in.Runner.TrustClass), string(in.Work.Policy.TrustClass))
	active := in.Runner.LifecycleState == runner.StateActive

	// Build deny reasons in order.
	if !approved {
		dec.DenyReasons = append(dec.DenyReasons, "missing_approved_evidence")
	} else {
		if !trustOK {
			dec.DenyReasons = append(dec.DenyReasons, "runner_trust_below_floor")
		} else if !active {
			dec.DenyReasons = append(dec.DenyReasons, "runner_not_active")
		}
	}

	if len(dec.DenyReasons) == 0 {
		dec.Allow = true
	} else {
		dec.DenyReasons = append(dec.DenyReasons, "production_access_denied")
	}
	return dec, nil
}

// EvaluateOrError maps a deny decision to a structured error suitable for
// writeError(..., 403, ...).
func (e *Engine) EvaluateOrError(ctx context.Context, in DecisionInput) (Decision, error) {
	dec, err := e.Evaluate(ctx, in)
	if err != nil {
		return dec, err
	}
	if !dec.Allow {
		return dec, fmt.Errorf("policy denied: %s", strings.Join(dec.DenyReasons, ", "))
	}
	return dec, nil
}

// ----------------------------------------------------------------------------
// Helpers (translated from policies/lease_grant.rego §3)
// ----------------------------------------------------------------------------

// anyApprovedEvidence mirrors `any_approved_evidence` in the Rego bundle:
// true iff at least one evidence record has result == "pass".
func anyApprovedEvidence(evs []workgraph.Evidence) bool {
	for _, e := range evs {
		if e.Result == "pass" {
			return true
		}
	}
	return false
}

// trustMeetsFloor mirrors `trust_meets_floor` in the Rego bundle.
// trustOrder is the rank of each trust class; lower index = lower privilege.
// Unknown classes get rank -1 and always fail the floor check.
var trustOrder = map[string]int{
	string(runner.TrustUntrusted): 0,
	string(runner.TrustStandard):  1,
	string(runner.TrustPrivileged): 2,
}

func trustMeetsFloor(runnerClass, floorClass string) bool {
	rRank, rok := trustOrder[runnerClass]
	if !rok {
		rRank = -1
	}
	fRank, fok := trustOrder[floorClass]
	if !fok {
		// Unknown floor is treated as "highest required" so we always fail.
		fRank = 999
	}
	return rRank >= fRank
}

// IsAllowed is a convenience for callers that want a one-line check.
func (d Decision) IsAllowed() bool { return d.Allow }

// FirstDenyReason returns the first deny reason or "" if allowed.
func (d Decision) FirstDenyReason() string {
	if len(d.DenyReasons) == 0 {
		return ""
	}
	return d.DenyReasons[0]
}

// ReasonCode maps a deny reason to a stable HTTP error code for the API
// layer. Empty string means "allowed".
func (d Decision) ReasonCode() string {
	if d.Allow {
		return ""
	}
	if len(d.DenyReasons) == 0 {
		return "policy_denied"
	}
	switch d.DenyReasons[0] {
	case "missing_approved_evidence":
		return "missing_approved_evidence"
	case "runner_trust_below_floor":
		return "runner_trust_below_floor"
	case "runner_not_active":
		return "runner_not_active"
	case "production_access_denied":
		return "production_access_denied"
	}
	return "policy_denied"
}