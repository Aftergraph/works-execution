package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	"github.com/JonasAbde/works-execution/packages/circuitrun"
)

func bindableDispatch(missionID, suffix string) dispatch.Dispatch {
	return dispatch.Dispatch{
		MissionID: missionID, AuthorityRef: "auth/" + suffix, AuthorityEpoch: 1,
		RuntimeDispatchID: "rdisp/" + suffix, AttemptID: "dispatch-attempt/" + suffix,
		EffectID: "effect/" + suffix, IdempotencyKey: "idem/" + suffix,
		BudgetRef: "budget/" + suffix, BudgetCeiling: 10,
		CheckpointID: "checkpoint/" + suffix, EvidenceRoot: "evidence/" + suffix,
		VerificationSubj: "subject/" + suffix, CausalID: "causal/" + suffix,
	}
}

func createRunAndAcceptance(t *testing.T, st *SQLiteStore, suffix string) (*circuitrun.Run, *dispatch.Acceptance) {
	t.Helper()
	ctx := context.Background()
	w := circuitMissionWork()
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	in := circuitInput(w.ID)
	in.MissionID = "mis_" + suffix
	run, err := st.CreateCircuitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := dispatch.NewAcceptor(st.DispatchAcceptanceStore(), func() time.Time { return time.Date(2026, 9, 16, 16, 0, 0, 0, time.UTC) })
	accepted, err := acceptor.Accept(bindableDispatch(in.MissionID, suffix), 1)
	if err != nil {
		t.Fatal(err)
	}
	return run, accepted
}

func TestCreateCircuitEffectBindingDerivesDispatchIdentity(t *testing.T) {
	st, _ := openBrainStore(t)
	run, accepted := createRunAndAcceptance(t, st, "effect_a")
	got, err := st.CreateCircuitEffectBinding(context.Background(), circuitrun.EffectBindingInput{
		CircuitRunID: run.ID, WorksExecutionID: accepted.WorksExecutionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkID != run.WorkID || got.MissionID != run.MissionID {
		t.Fatalf("run identity mismatch: %+v", got)
	}
	if got.EffectID != accepted.Dispatch.EffectID || got.VerificationSubject != accepted.Dispatch.VerificationSubj {
		t.Fatalf("dispatch identity mismatch: %+v", got)
	}
	if got.RuntimeDispatchID != accepted.Dispatch.RuntimeDispatchID || got.CausalID != accepted.Dispatch.CausalID {
		t.Fatalf("dispatch provenance mismatch: %+v", got)
	}
	if len(got.DispatchSHA256) != 64 {
		t.Fatalf("dispatch digest length=%d", len(got.DispatchSHA256))
	}
}
func TestCreateCircuitEffectBindingRejectsMissionMismatch(t *testing.T) {
	st, _ := openBrainStore(t)
	run, _ := createRunAndAcceptance(t, st, "mission_a")
	acceptor := dispatch.NewAcceptor(st.DispatchAcceptanceStore(), nil)
	other, err := acceptor.Accept(bindableDispatch("mis_other", "mission_b"), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateCircuitEffectBinding(context.Background(), circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: other.WorksExecutionID})
	if !errors.Is(err, ErrCircuitEffectMissionMismatch) {
		t.Fatalf("got %v", err)
	}
}

func TestCreateCircuitEffectBindingIsIdempotentAndRejectsRebind(t *testing.T) {
	st, _ := openBrainStore(t)
	run, accepted := createRunAndAcceptance(t, st, "idem_a")
	ctx := context.Background()
	in := circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: accepted.WorksExecutionID}
	first, err := st.CreateCircuitEffectBinding(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateCircuitEffectBinding(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !first.BoundAt.Equal(second.BoundAt) {
		t.Fatalf("retry changed binding")
	}
	otherWork := circuitMissionWork()
	if err := st.CreateWork(ctx, otherWork); err != nil {
		t.Fatal(err)
	}
	otherInput := circuitInput(otherWork.ID)
	otherInput.MissionID = run.MissionID
	otherRun, err := st.CreateCircuitRun(ctx, otherInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: otherRun.ID, WorksExecutionID: accepted.WorksExecutionID})
	if !errors.Is(err, ErrCircuitEffectBindingConflict) {
		t.Fatalf("got %v", err)
	}
}
func TestCircuitRunMayBindMultipleDistinctDispatchEffects(t *testing.T) {
	st, _ := openBrainStore(t)
	run, firstAccepted := createRunAndAcceptance(t, st, "multi")
	acceptor := dispatch.NewAcceptor(st.DispatchAcceptanceStore(), nil)
	secondAccepted, err := acceptor.Accept(bindableDispatch(run.MissionID, "multi_second"), 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: firstAccepted.WorksExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: secondAccepted.WorksExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.EffectID == second.EffectID {
		t.Fatalf("distinct effects collapsed")
	}
}

func TestCircuitEffectBindingRejectsStoredDispatchDrift(t *testing.T) {
	st, _ := openBrainStore(t)
	run, accepted := createRunAndAcceptance(t, st, "drift")
	ctx := context.Background()
	binding, err := st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: accepted.WorksExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE dispatch_acceptances SET acceptance_json = replace(acceptance_json, 'effect/drift', 'effect/tampered') WHERE works_execution_id = ?`, accepted.WorksExecutionID); err != nil {
		t.Fatal(err)
	}
	_, err = st.GetCircuitEffectBinding(ctx, binding.ID)
	if !errors.Is(err, ErrCircuitEffectSubjectMismatch) {
		t.Fatalf("got %v", err)
	}
}
func TestCircuitEffectBindingFailsClosedForUnknownOrIncompleteRecords(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	_, err := st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: "crun_0123456789abcdef0123456789abcdef", WorksExecutionID: "wexec/missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
	run, _ := createRunAndAcceptance(t, st, "complete_base")
	acceptor := dispatch.NewAcceptor(st.DispatchAcceptanceStore(), nil)
	bad := bindableDispatch(run.MissionID, "incomplete")
	bad.EffectID = ""
	accepted, err := acceptor.Accept(bad, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: accepted.WorksExecutionID})
	if !errors.Is(err, ErrCircuitEffectDispatchIncomplete) {
		t.Fatalf("incomplete: %v", err)
	}
}

func TestListCircuitEffectBindingsByRun(t *testing.T) {
	st, _ := openBrainStore(t)
	run, firstAccepted := createRunAndAcceptance(t, st, "list")
	acceptor := dispatch.NewAcceptor(st.DispatchAcceptanceStore(), nil)
	secondAccepted, err := acceptor.Accept(bindableDispatch(run.MissionID, "list_second"), 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, execID := range []string{firstAccepted.WorksExecutionID, secondAccepted.WorksExecutionID} {
		if _, err := st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: execID}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListCircuitEffectBindingsByRunID(ctx, run.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("list=%+v err=%v", got, err)
	}
}
func TestCircuitEffectBindingPersistsAcrossRestart(t *testing.T) {
	st, path := openBrainStore(t)
	run, accepted := createRunAndAcceptance(t, st, "restart")
	ctx := context.Background()
	created, err := st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: accepted.WorksExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetCircuitEffectBinding(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.DispatchSHA256 != created.DispatchSHA256 || got.EffectID != created.EffectID {
		t.Fatalf("restart changed binding: got=%+v want=%+v", got, created)
	}
}
func TestCircuitEffectBindingRejectsDuplicateEffectWithinRun(t *testing.T) {
	st, _ := openBrainStore(t)
	run, firstAccepted := createRunAndAcceptance(t, st, "same_effect")
	secondDispatch := bindableDispatch(run.MissionID, "same_effect_second")
	secondDispatch.EffectID = firstAccepted.Dispatch.EffectID
	acceptor := dispatch.NewAcceptor(st.DispatchAcceptanceStore(), nil)
	secondAccepted, err := acceptor.Accept(secondDispatch, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: firstAccepted.WorksExecutionID}); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: secondAccepted.WorksExecutionID})
	if !errors.Is(err, ErrCircuitEffectBindingConflict) {
		t.Fatalf("got %v", err)
	}
}

func TestCircuitEffectBindingRetryRejectsStoredBindingDrift(t *testing.T) {
	st, _ := openBrainStore(t)
	run, accepted := createRunAndAcceptance(t, st, "retry_drift")
	ctx := context.Background()
	created, err := st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: accepted.WorksExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE circuit_effect_bindings SET effect_id = 'effect/tampered' WHERE id = ?`, created.ID); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateCircuitEffectBinding(ctx, circuitrun.EffectBindingInput{CircuitRunID: run.ID, WorksExecutionID: accepted.WorksExecutionID})
	if !errors.Is(err, ErrCircuitEffectSubjectMismatch) {
		t.Fatalf("got %v", err)
	}
}
