package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	workstore "github.com/JonasAbde/works-execution/services/work/store"
)

func liveDispatchFixture() dispatch.Dispatch {
	return dispatch.Dispatch{
		MissionID: "mis_live", AuthorityRef: "auth_live", AuthorityEpoch: 4,
		RuntimeDispatchID: "rdisp_live", AttemptID: "attempt_live", EffectID: "effect_live",
		IdempotencyKey: "idem_live", BudgetRef: "budget_live", BudgetCeiling: 10,
		CheckpointID: "checkpoint_live", EvidenceRoot: "evidence_live",
		VerificationSubj: "subject_live", CausalID: "causal_live",
	}
}

func TestDispatchAcceptanceSurvivesStoreRestartWithoutDuplicateEffect(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "works.db")
	firstStore, err := workstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	first := dispatch.NewAcceptor(firstStore.DispatchAcceptanceStore(), func() time.Time {
		return time.Unix(1, 0).UTC()
	})
	input := liveDispatchFixture()
	accepted, err := first.Accept(input, input.AuthorityEpoch)
	if err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if err := first.ApplyEffect(accepted.WorksExecutionID, input.EffectID, true); err != nil {
		t.Fatalf("apply effect: %v", err)
	}
	if err := first.Spend(accepted.WorksExecutionID, 3); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if err := first.Complete(accepted.WorksExecutionID, "SUCCEEDED"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := first.RecordVerdict(accepted.WorksExecutionID, "verifier_live", input.VerificationSubj, true, true); err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restartedStore, err := workstore.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer restartedStore.Close()
	restarted := dispatch.NewAcceptor(restartedStore.DispatchAcceptanceStore(), func() time.Time {
		return time.Unix(2, 0).UTC()
	})
	replayed, err := restarted.Accept(input, input.AuthorityEpoch)
	if err != nil {
		t.Fatalf("replay accept after restart: %v", err)
	}
	if replayed.WorksExecutionID != accepted.WorksExecutionID {
		t.Fatalf("works execution changed across restart: got %q want %q", replayed.WorksExecutionID, accepted.WorksExecutionID)
	}
	if !replayed.EffectApplied {
		t.Fatal("effect-applied state was lost across restart")
	}
	if replayed.BudgetSpent != 3 || replayed.Outcome != "SUCCEEDED" || !replayed.Verified || replayed.VerifierID != "verifier_live" {
		t.Fatalf("durable acceptance state mismatch after restart: %+v", replayed)
	}
	if err := restarted.ApplyEffect(replayed.WorksExecutionID, input.EffectID, true); !errors.Is(err, dispatch.ErrEffectDuplicate) {
		t.Fatalf("duplicate effect after restart err=%v want ErrEffectDuplicate", err)
	}
}

func TestDispatchAcceptanceRejectsCausalReplayAfterStoreRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "works.db")
	firstStore, err := workstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	input := liveDispatchFixture()
	first := dispatch.NewAcceptor(firstStore.DispatchAcceptanceStore(), nil)
	if _, err := first.Accept(input, input.AuthorityEpoch); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	restartedStore, err := workstore.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer restartedStore.Close()
	conflicting := input
	conflicting.CausalID = "causal_other"
	restarted := dispatch.NewAcceptor(restartedStore.DispatchAcceptanceStore(), nil)
	_, err = restarted.Accept(conflicting, input.AuthorityEpoch)
	if !errors.Is(err, dispatch.ErrCausalMismatch) {
		t.Fatalf("unexpected replay result: %v", err)
	}
}
