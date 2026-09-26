package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

type subjectStore struct {
	acceptance *Acceptance
	binding    *VerificationSubjectBinding
}

func (s *subjectStore) LoadByIdempotency(string) (*Acceptance, error) { return s.acceptance, nil }
func (s *subjectStore) LoadByExecution(id string) (*Acceptance, error) {
	if s.acceptance != nil && s.acceptance.WorksExecutionID == id {
		return cloneAcceptance(s.acceptance), nil
	}
	return nil, nil
}
func (s *subjectStore) AcceptIfAbsent(a *Acceptance) (*Acceptance, error) {
	s.acceptance = cloneAcceptance(a)
	return cloneAcceptance(a), nil
}
func (s *subjectStore) Save(a *Acceptance) error {
	s.acceptance = cloneAcceptance(a)
	return nil
}
func (s *subjectStore) BindVerificationSubject(_ context.Context, b VerificationSubjectBinding) (*VerificationSubjectBinding, error) {
	if s.binding == nil {
		b.BoundAt = time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
		s.binding = &b
	}
	cp := *s.binding
	return &cp, nil
}
func (s *subjectStore) LoadVerificationSubject(_ context.Context, id string) (*VerificationSubjectBinding, error) {
	if s.binding == nil || s.binding.WorksExecutionID != id {
		return nil, nil
	}
	cp := *s.binding
	return &cp, nil
}

func v2SubjectAcceptor() (*Acceptor, *subjectStore) {
	st := &subjectStore{
		acceptance: &Acceptance{
			ContractVersion:  "dispatch.acceptance/2.0",
			WorkID:           "wrk_11111111111111111111111111111111",
			WorksExecutionID: "wexec/1",
			Dispatch: Dispatch{
				MissionID:         "mis_example",
				AuthorityRef:      "auth_44444444444444444444444444444444",
				RuntimeDispatchID: "rdisp/1",
				AttemptID:         "attempt/1",
				EffectID:          "effect/1",
				IdempotencyKey:    "idem/1",
				BudgetRef:         "budget/1",
				BudgetCeiling:     10,
				CheckpointID:      "checkpoint/1",
				EvidenceRoot:      "evidence/1",
				CausalID:          "causal/1",
			},
			Outcome: "ACCEPTED",
		},
	}
	return NewAcceptor(st, func() time.Time {
		return time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	}), st
}

func TestV2VerdictFailsBeforeObservedSubjectIsBound(t *testing.T) {
	a, _ := v2SubjectAcceptor()
	if err := a.Complete("wexec/1", "SUCCEEDED"); err != nil { t.Fatal(err) }
	err := a.RecordVerdict(
		"wexec/1", "sentinel/independent",
		"git:Aftergraph/repo@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		true, true, "ACCEPT", "evidence/1",
	)
	if !errors.Is(err, ErrSubjectNotBound) {
		t.Fatalf("expected ErrSubjectNotBound, got %v", err)
	}
}

func TestV2SubjectBindThenExactVerdict(t *testing.T) {
	a, _ := v2SubjectAcceptor()
	subject := "git:Aftergraph/repo@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	bound, err := a.BindVerificationSubject(
		context.Background(), "wexec/1",
		"wrk_11111111111111111111111111111111",
		"attempt/1", "effect/1", "causal/1", subject,
	)
	if err != nil { t.Fatal(err) }
	if bound.Subject != subject { t.Fatalf("subject=%q", bound.Subject) }

	if err := a.Complete("wexec/1", "SUCCEEDED"); err != nil { t.Fatal(err) }
	if err := a.RecordVerdict(
		"wexec/1", "sentinel/independent", subject,
		true, true, "ACCEPT", "evidence/exact",
	); err != nil {
		t.Fatalf("exact bound verdict failed: %v", err)
	}
}

func TestV2DifferentSubjectRebindFailsClosed(t *testing.T) {
	a, _ := v2SubjectAcceptor()
	first := "git:Aftergraph/repo@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second := "git:Aftergraph/repo@cccccccccccccccccccccccccccccccccccccccc"
	if _, err := a.BindVerificationSubject(
		context.Background(), "wexec/1",
		"wrk_11111111111111111111111111111111",
		"attempt/1", "effect/1", "causal/1", first,
	); err != nil { t.Fatal(err) }
	if _, err := a.BindVerificationSubject(
		context.Background(), "wexec/1",
		"wrk_11111111111111111111111111111111",
		"attempt/1", "effect/1", "causal/1", second,
	); !errors.Is(err, ErrSubjectConflict) {
		t.Fatalf("expected ErrSubjectConflict, got %v", err)
	}
}
