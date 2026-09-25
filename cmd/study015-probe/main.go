package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
)

type memoryStore struct {
	mu   sync.Mutex
	byK  map[string]*dispatch.Acceptance
	byID map[string]*dispatch.Acceptance
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		byK:  map[string]*dispatch.Acceptance{},
		byID: map[string]*dispatch.Acceptance{},
	}
}

func cloneAcceptance(in *dispatch.Acceptance) *dispatch.Acceptance {
	if in == nil {
		return nil
	}
	cp := *in
	if in.Verdict != nil {
		v := *in.Verdict
		cp.Verdict = &v
	}
	return &cp
}

func (m *memoryStore) LoadByIdempotency(key string) (*dispatch.Acceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneAcceptance(m.byK[key]), nil
}

func (m *memoryStore) LoadByExecution(id string) (*dispatch.Acceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneAcceptance(m.byID[id]), nil
}

func (m *memoryStore) AcceptIfAbsent(a *dispatch.Acceptance) (*dispatch.Acceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.byK[a.Dispatch.IdempotencyKey]; existing != nil {
		return cloneAcceptance(existing), nil
	}
	cp := cloneAcceptance(a)
	m.byK[a.Dispatch.IdempotencyKey] = cp
	m.byID[a.WorksExecutionID] = cp
	return cloneAcceptance(cp), nil
}

func (m *memoryStore) Save(a *dispatch.Acceptance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := cloneAcceptance(a)
	m.byK[a.Dispatch.IdempotencyKey] = cp
	m.byID[a.WorksExecutionID] = cp
	return nil
}

type receipt struct {
	Schema         string                 `json:"schema"`
	Component      string                 `json:"component"`
	SourceHead     string                 `json:"source_head"`
	ExecutionClass string                 `json:"execution_class"`
	NetworkUsed    bool                   `json:"network_used"`
	Mechanisms     map[string]bool        `json:"mechanisms"`
	Observations   map[string]interface{} `json:"observations"`
}

func gitHead() (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(out))
	if len(head) != 40 {
		return "", fmt.Errorf("unexpected git head %q", head)
	}
	return head, nil
}

func testDispatch(key string) dispatch.Dispatch {
	return dispatch.Dispatch{
		MissionID:         "mission/study015",
		AuthorityRef:      "authority/study015",
		AuthorityEpoch:    7,
		RuntimeDispatchID: "runtime/study015",
		AttemptID:         "attempt/" + key,
		EffectID:          "effect/" + key,
		IdempotencyKey:    key,
		BudgetRef:         "budget/study015",
		BudgetCeiling:     100,
		CheckpointID:      "checkpoint/study015",
		EvidenceRoot:      "evidence/study015",
		VerificationSubj:  "subject/study015",
		CausalID:          "causal/" + key,
	}
}

func runProbe() (receipt, error) {
	head, err := gitHead()
	if err != nil {
		return receipt{}, err
	}
	store := newMemoryStore()
	clock := func() time.Time {
		return time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	}
	acceptor := dispatch.NewAcceptor(store, clock)

	d := testDispatch("study015-main")
	first, err := acceptor.Accept(d, 7)
	if err != nil {
		return receipt{}, fmt.Errorf("accept: %w", err)
	}
	if first.ExecutionContextID == "" || first.TraceID == "" {
		return receipt{}, errors.New("WORKS did not mint correlation identity")
	}
	replay, err := acceptor.Accept(d, 7)
	if err != nil {
		return receipt{}, fmt.Errorf("replay accept: %w", err)
	}
	if replay.ExecutionContextID != first.ExecutionContextID || replay.TraceID != first.TraceID {
		return receipt{}, errors.New("idempotent replay reminted correlation identity")
	}
	if _, err := acceptor.Accept(d, 8); !errors.Is(err, dispatch.ErrStaleAuthority) {
		return receipt{}, fmt.Errorf("stale epoch was not rejected: %v", err)
	}

	if err := acceptor.Spend(first.WorksExecutionID, 60); err != nil {
		return receipt{}, fmt.Errorf("initial spend: %w", err)
	}
	if err := acceptor.Spend(first.WorksExecutionID, 50); !errors.Is(err, dispatch.ErrBudgetExhausted) {
		return receipt{}, fmt.Errorf("budget overflow did not fail closed: %v", err)
	}
	if err := acceptor.ApplyEffect(first.WorksExecutionID, d.EffectID, true); err != nil {
		return receipt{}, fmt.Errorf("apply effect: %w", err)
	}
	if err := acceptor.ApplyEffect(first.WorksExecutionID, d.EffectID, true); !errors.Is(err, dispatch.ErrEffectDuplicate) {
		return receipt{}, fmt.Errorf("duplicate effect was not rejected: %v", err)
	}
	if err := acceptor.Complete(first.WorksExecutionID, "SUCCEEDED"); err != nil {
		return receipt{}, fmt.Errorf("complete: %w", err)
	}
	completed, _ := store.LoadByExecution(first.WorksExecutionID)
	if completed.Verified {
		return receipt{}, errors.New("completion silently promoted to verified")
	}
	if err := acceptor.RecordVerdict(
		first.WorksExecutionID,
		d.RuntimeDispatchID,
		d.VerificationSubj,
		true,
		true,
		"ACCEPT",
		"evidence/self",
	); !errors.Is(err, dispatch.ErrSelfVerification) {
		return receipt{}, fmt.Errorf("self-verification was not rejected: %v", err)
	}
	if err := acceptor.RecordVerdict(
		first.WorksExecutionID,
		"verifier/sentinel",
		d.VerificationSubj,
		true,
		true,
		"ACCEPT",
		"evidence/independent",
	); err != nil {
		return receipt{}, fmt.Errorf("independent verdict: %w", err)
	}
	verified, _ := store.LoadByExecution(first.WorksExecutionID)
	if !verified.Verified || verified.Verdict == nil || verified.Verdict.EvidenceRef != "evidence/independent" {
		return receipt{}, errors.New("independent verdict was not durably bound")
	}

	unknown := testDispatch("study015-unknown")
	unknownAcc, err := acceptor.Accept(unknown, 7)
	if err != nil {
		return receipt{}, err
	}
	if err := acceptor.ApplyEffect(unknownAcc.WorksExecutionID, unknown.EffectID, false); err != nil {
		return receipt{}, err
	}
	unknownState, _ := store.LoadByExecution(unknownAcc.WorksExecutionID)
	if unknownState.Outcome != "INDETERMINATE" {
		return receipt{}, fmt.Errorf("unknown effect state was %q, expected INDETERMINATE", unknownState.Outcome)
	}

	revokedDispatch := testDispatch("study015-revoked")
	revokedAcc, err := acceptor.Accept(revokedDispatch, 7)
	if err != nil {
		return receipt{}, err
	}
	if err := acceptor.Revoke(revokedAcc.WorksExecutionID); err != nil {
		return receipt{}, err
	}
	if err := acceptor.ApplyEffect(
		revokedAcc.WorksExecutionID,
		revokedDispatch.EffectID,
		true,
	); !errors.Is(err, dispatch.ErrRevoked) {
		return receipt{}, fmt.Errorf("revoked execution applied effect: %v", err)
	}

	return receipt{
		Schema:         "study015.probe/1.0",
		Component:      "works-execution",
		SourceHead:     head,
		ExecutionClass: "LOCAL_IMPLEMENTATION_PROBE",
		NetworkUsed:    false,
		Mechanisms: map[string]bool{
			"durable_dispatch_acceptance":        true,
			"stable_replay_correlation":          true,
			"stale_authority_rejected":           true,
			"budget_ceiling_fail_closed":         true,
			"effect_exactly_once":                true,
			"unknown_effect_is_indeterminate":    true,
			"completion_not_verification":        true,
			"self_verification_rejected":         true,
			"independent_evidence_bound_verdict": true,
			"revocation_fences_effects":          true,
		},
		Observations: map[string]interface{}{
			"works_execution_id":   first.WorksExecutionID,
			"execution_context_id": first.ExecutionContextID,
			"trace_id":             first.TraceID,
			"budget_spent":         verified.BudgetSpent,
			"verifier_id":          verified.VerifierID,
			"verdict_evidence_ref": verified.Verdict.EvidenceRef,
			"unknown_effect_state": unknownState.Outcome,
		},
	}, nil
}

func main() {
	out, err := runProbe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
