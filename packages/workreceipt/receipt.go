package workreceipt

import (
	"errors"
	"strings"
	"time"
)

const Schema = "aftergraph.work-receipt/v1"

type Step struct {
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Ref        string    `json:"ref,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type Changes struct {
	Files        []string `json:"files"`
	Commits      []string `json:"commits"`
	PullRequests []string `json:"pull_requests"`
}

type Usage struct {
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CachedTokens int64   `json:"cached_tokens,omitempty"`
	Cost         float64 `json:"cost,omitempty"`
	Currency     string  `json:"currency,omitempty"`
}

type Verification struct {
	State        string   `json:"state"`
	VerifierRef  string   `json:"verifier_ref,omitempty"`
	Evaluations  []string `json:"evaluations"`
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
	Changes      Changes      `json:"changes"`
	Usage        *Usage       `json:"usage,omitempty"`
	Artifacts    []string     `json:"artifacts"`
	EvidenceRefs []string     `json:"evidence_refs"`
	Verification Verification `json:"verification"`
	Outcome      string       `json:"outcome"`
}

func New(r Receipt, now time.Time) (Receipt, error) {
	r.Schema = Schema
	if r.RecordedAt.IsZero() {
		r.RecordedAt = now.UTC()
	}
	if strings.TrimSpace(r.WorkID) == "" {
		return Receipt{}, errors.New("work_id is required")
	}
	if strings.TrimSpace(r.Intent) == "" {
		return Receipt{}, errors.New("intent is required")
	}
	if strings.TrimSpace(r.AuthorityRef) == "" {
		return Receipt{}, errors.New("authority_ref is required")
	}
	if strings.TrimSpace(r.Executor.Agent) == "" {
		return Receipt{}, errors.New("executor.agent is required")
	}
	if r.Outcome == "completed" && len(r.EvidenceRefs) == 0 {
		return Receipt{}, errors.New("completed work requires evidence_refs")
	}
	if r.Verification.State == "passed" && strings.TrimSpace(r.Verification.VerifierRef) == "" {
		return Receipt{}, errors.New("passed verification requires verifier_ref")
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
	return r, nil
}
