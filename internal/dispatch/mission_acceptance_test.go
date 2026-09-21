package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

type missionAcceptanceStore struct {
	*memoryStore
	subject *VerificationSubjectBinding
}

func newMissionAcceptanceStore() *missionAcceptanceStore {
	return &missionAcceptanceStore{memoryStore: newMemoryStore()}
}

func (m *missionAcceptanceStore) BindVerificationSubject(_ context.Context, in VerificationSubjectBinding) (*VerificationSubjectBinding, error) {
	if m.subject == nil {
		cp := in
		m.subject = &cp
	}
	cp := *m.subject
	return &cp, nil
}

func (m *missionAcceptanceStore) LoadVerificationSubject(_ context.Context, worksExecutionID string) (*VerificationSubjectBinding, error) {
	if m.subject == nil || m.subject.WorksExecutionID != worksExecutionID {
		return nil, nil
	}
	cp := *m.subject
	return &cp, nil
}

func seedV2MissionAcceptance(t *testing.T) (*Acceptor, *missionAcceptanceStore, *Acceptance, string) {
	t.Helper()
	store := newMissionAcceptanceStore()
	now := time.Date(2026, 9, 21, 15, 0, 0, 0, time.UTC)
	a := NewAcceptor(store, func() time.Time { return now })
	subject := "git:Aftergraph/runtime@" + "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	acc := &Acceptance{
		ContractVersion: "dispatch.acceptance/2.0",
		WorkID: "wrk_" + "1" + "1111111111111111111111111111111",
		WorksExecutionID: "wexec/p2-mission-acceptance",
		Dispatch: Dispatch{
			MissionID: "mis_p2",
			AuthorityRef: "auth_" + "4" + "4444444444444444444444444444444",
			RuntimeDispatchID: "rdisp/p2",
			AttemptID: "attempt/p2",
			EffectID: "effect/p2",
			IdempotencyKey: "idem/p2",
			CausalID: "causal/p2",
		},
		AcceptedAt: now,
		Outcome: "ACCEPTED",
	}
	if err := store.Save(acc); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindVerificationSubject(context.Background(), VerificationSubjectBinding{
		WorksExecutionID: acc.WorksExecutionID,
		WorkID: acc.WorkID,
		AttemptID: acc.Dispatch.AttemptID,
		EffectID: acc.Dispatch.EffectID,
		CausalID: acc.Dispatch.CausalID,
		Subject: subject,
		BoundAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return a, store, acc, subject
}

func TestFinalizeVerifiedSuccess_CommitsExactSubjectAtomicallyAndIdempotently(t *testing.T) {
	a, store, acc, subject := seedV2MissionAcceptance(t)
	evidence := "sentinel.receipt:" + "b2c3d4e5f60718293a4b5c6d7e8f90123456789a1b2c3d4e5f60718293a4b5c"

	if err := a.FinalizeVerifiedSuccess(acc.WorksExecutionID, "sentinel:exact-head", subject, true, true, evidence); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadByExecution(acc.WorksExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.EffectApplied || got.Outcome != "SUCCEEDED" || !got.Verified ||
		got.VerifierID != "sentinel:exact-head" || got.Verdict == nil ||
		got.Verdict.Result != "ACCEPT" || got.Verdict.Subject != subject ||
		got.Verdict.EvidenceRef != evidence {
		t.Fatalf("bad finalized acceptance: %+v", got)
	}

	// Exact replay is safe and does not create a second terminal truth.
	if err := a.FinalizeVerifiedSuccess(acc.WorksExecutionID, "sentinel:exact-head", subject, true, true, evidence); err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
}

func TestFinalizeVerifiedSuccess_FailsClosedBeforeSaveOnStaleOrConflictingEvidence(t *testing.T) {
	a, store, acc, subject := seedV2MissionAcceptance(t)
	before, _ := store.LoadByExecution(acc.WorksExecutionID)

	if err := a.FinalizeVerifiedSuccess(
		acc.WorksExecutionID,
		"sentinel:exact-head",
		"git:Aftergraph/runtime/"+"@"+"f",
		true,
		true,
		"sentinel.receipt:"+"c",
	); !errors.Is(err, ErrStaleSubject) {
		t.Fatalf("expected stale subject, got %v", err)
	}
	after, _ := store.LoadByExecution(acc.WorksExecutionID)
	if after.Verified != before.Verified || after.Outcome != before.Outcome || after.EffectApplied != before.EffectApplied {
		t.Fatalf("stale evidence mutated acceptance: before=%+v after=%+v", before, after)
	}

	evidenceA := "sentinel.receipt:" + "a"
	if err := a.FinalizeVerifiedSuccess(acc.WorksExecutionID, "sentinel:exact-head", subject, true, true, evidenceA); err != nil {
		t.Fatal(err)
	}
	if err := a.FinalizeVerifiedSuccess(acc.WorksExecutionID, "sentinel:exact-head", subject, true, true, "sentinel.receipt:"+"different"); !errors.Is(err, ErrVerdictConflict) {
		t.Fatalf("expected verdict conflict, got %v", err)
	}
}

func TestFinalizeVerifiedSuccess_RejectsSelfVerifierRevocationAndUnavailableVerifier(t *testing.T) {
	a, _, acc, subject := seedV2MissionAcceptance(t)
	if err := a.FinalizeVerifiedSuccess(acc.WorksExecutionID, acc.Dispatch.RuntimeDispatchID, subject, true, true, "evidence/1"); !errors.Is(err, ErrSelfVerification) {
		t.Fatalf("expected self verification rejection, got %v", err)
	}

	a2, _, acc2, subject2 := seedV2MissionAcceptance(t)
	if err := a2.Revoke(acc2.WorksExecutionID); err != nil {
		t.Fatal(err)
	}
	if err := a2.FinalizeVerifiedSuccess(acc2.WorksExecutionID, "sentinel:exact-head", subject2, true, true, "evidence/2"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("expected revoked rejection, got %v", err)
	}

	a3, _, acc3, subject3 := seedV2MissionAcceptance(t)
	if err := a3.FinalizeVerifiedSuccess(acc3.WorksExecutionID, "sentinel:exact-head", subject3, true, false, "evidence/3"); !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("expected verifier unavailable, got %v", err)
	}
}
