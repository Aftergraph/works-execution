package workreceipt

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const Schema = "aftergraph.work-receipt/v1"

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

var canonicalInvariants = []string{
	"receipt presence does not prove correctness",
	"execution success does not establish verified outcome",
	"authority is referenced, never inferred from tool possession",
	"evidence references must remain distinguishable from executor assertions",
}

var allowedStepKinds = map[string]struct{}{
	"plan": {}, "tool": {}, "file": {}, "command": {}, "network": {},
	"approval": {}, "test": {}, "build": {}, "commit": {}, "deploy": {}, "human": {},
}

var allowedStepStatuses = map[string]struct{}{
	"attempted": {}, "succeeded": {}, "failed": {}, "denied": {}, "skipped": {},
}

type Step struct {
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Ref        string    `json:"ref,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type Changes struct {
	Files        []string `json:"files,omitempty"`
	Commits      []string `json:"commits,omitempty"`
	PullRequests []string `json:"pull_requests,omitempty"`
}

type Usage struct {
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CachedTokens int64   `json:"cached_tokens,omitempty"`
	Cost         float64 `json:"cost,omitempty"`
	Currency     string  `json:"currency,omitempty"`
}

type Verification struct {
	State       string   `json:"state"`
	VerifierRef string   `json:"verifier_ref,omitempty"`
	Evaluations []string `json:"evaluations"`
}

type Executor struct {
	Agent       string `json:"agent"`
	Model       string `json:"model,omitempty"`
	RuntimeNode string `json:"runtime_node,omitempty"`
	SessionRef  string `json:"session_ref,omitempty"`
}

type Receipt struct {
	Schema       string       `json:"schema"`
	WorkID       string       `json:"work_id"`
	RecordedAt   time.Time    `json:"recorded_at"`
	TenantID     string       `json:"tenant_id,omitempty"`
	Executor     Executor     `json:"executor"`
	Intent       string       `json:"intent"`
	AuthorityRef string       `json:"authority_ref"`
	Steps        []Step       `json:"steps"`
	Changes      Changes      `json:"changes,omitempty"`
	Usage        *Usage       `json:"usage,omitempty"`
	Artifacts    []string     `json:"artifacts"`
	EvidenceRefs []string     `json:"evidence_refs"`
	Verification Verification `json:"verification"`
	Outcome      string       `json:"outcome"`
	Invariants   []string     `json:"invariants"`
}

func DefaultInvariants() []string {
	out := make([]string, len(canonicalInvariants))
	copy(out, canonicalInvariants)
	return out
}

func New(r Receipt, now time.Time) (Receipt, error) {
	r.Schema = Schema
	if r.RecordedAt.IsZero() {
		r.RecordedAt = now.UTC()
	}

	if err := requireTrimmed("work_id", r.WorkID, 256); err != nil {
		return Receipt{}, err
	}
	if err := requireTrimmed("intent", r.Intent, 4096); err != nil {
		return Receipt{}, err
	}
	if err := requireTrimmed("authority_ref", r.AuthorityRef, 512); err != nil {
		return Receipt{}, err
	}
	if err := requireTrimmed("executor.agent", r.Executor.Agent, 256); err != nil {
		return Receipt{}, err
	}
	if len(r.TenantID) > 256 || len(r.Executor.Model) > 256 ||
		len(r.Executor.RuntimeNode) > 256 || len(r.Executor.SessionRef) > 512 {
		return Receipt{}, errors.New("receipt metadata exceeds governance limits")
	}

	switch r.Outcome {
	case "completed", "partial", "failed", "denied", "not_executed":
	default:
		return Receipt{}, errors.New("invalid outcome")
	}

	switch r.Verification.State {
	case "not_run", "pending", "passed", "failed", "needs_review":
	default:
		return Receipt{}, errors.New("invalid verification state")
	}

	if r.Outcome == "completed" && len(r.EvidenceRefs) == 0 {
		return Receipt{}, errors.New("completed work requires evidence_refs")
	}
	if r.Verification.State == "passed" && strings.TrimSpace(r.Verification.VerifierRef) == "" {
		return Receipt{}, errors.New("passed verification requires verifier_ref")
	}

	for i, step := range r.Steps {
		if _, ok := allowedStepKinds[step.Kind]; !ok {
			return Receipt{}, fmt.Errorf("steps[%d].kind is invalid", i)
		}
		if _, ok := allowedStepStatuses[step.Status]; !ok {
			return Receipt{}, fmt.Errorf("steps[%d].status is invalid", i)
		}
		if len(step.Ref) > 512 || len(step.Summary) > 2048 {
			return Receipt{}, fmt.Errorf("steps[%d] exceeds governance limits", i)
		}
	}

	if r.Usage != nil {
		if r.Usage.InputTokens < 0 || r.Usage.OutputTokens < 0 || r.Usage.CachedTokens < 0 || r.Usage.Cost < 0 {
			return Receipt{}, errors.New("usage values must be non-negative")
		}
		if r.Usage.Currency != "" && !currencyRE.MatchString(r.Usage.Currency) {
			return Receipt{}, errors.New("usage.currency must be an uppercase ISO-style 3-letter code")
		}
	}

	if err := validateUnique("changes.files", r.Changes.Files, 1024); err != nil {
		return Receipt{}, err
	}
	if err := validateUnique("changes.commits", r.Changes.Commits, 256); err != nil {
		return Receipt{}, err
	}
	if err := validateUnique("changes.pull_requests", r.Changes.PullRequests, 512); err != nil {
		return Receipt{}, err
	}
	if err := validateUnique("artifacts", r.Artifacts, 1024); err != nil {
		return Receipt{}, err
	}
	if err := validateUnique("evidence_refs", r.EvidenceRefs, 1024); err != nil {
		return Receipt{}, err
	}
	if err := validateUnique("verification.evaluations", r.Verification.Evaluations, 512); err != nil {
		return Receipt{}, err
	}

	if !equalStrings(r.Invariants, canonicalInvariants) {
		return Receipt{}, errors.New("invariants must match the canonical work-receipt truth boundary")
	}

	return r, nil
}

func requireTrimmed(name, value string, max int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > max {
		return fmt.Errorf("%s exceeds governance limit", name)
	}
	return nil
}

func validateUnique(name string, values []string, max int) error {
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		if len(value) > max {
			return fmt.Errorf("%s[%d] exceeds governance limit", name, i)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s must contain unique items", name)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
