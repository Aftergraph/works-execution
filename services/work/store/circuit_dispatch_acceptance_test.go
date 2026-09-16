package store

import (
	"context"
	"errors"
	"testing"

	"github.com/JonasAbde/works-execution/internal/dispatch"
)

func createCircuitRunForCDA(t *testing.T, st *SQLiteStore, missionID string) string {
	t.Helper()
	ctx := context.Background()
	w := circuitMissionWork()
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	in := circuitInput(w.ID)
	in.MissionID = missionID
	run, err := st.CreateCircuitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func TestAcceptCircuitDispatchCommitsAcceptanceAndBindingTogether(t *testing.T) {
	st, _ := openBrainStore(t)
	runID := createCircuitRunForCDA(t, st, "mis_cda_success")
	d := bindableDispatch("mis_cda_success", "cda-success")
	accepted, binding, err := st.AcceptCircuitDispatch(context.Background(), runID, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	if binding.WorksExecutionID != accepted.WorksExecutionID || binding.EffectID != d.EffectID {
		t.Fatalf("binding mismatch: %+v %+v", accepted, binding)
	}
	if got, err := st.GetCircuitEffectBindingByExecutionID(context.Background(), accepted.WorksExecutionID); err != nil || got.ID != binding.ID {
		t.Fatalf("binding not durable: %+v %v", got, err)
	}
}
func TestAcceptCircuitDispatchRollsBackOnMissionMismatch(t *testing.T) {
	st, _ := openBrainStore(t)
	runID := createCircuitRunForCDA(t, st, "mis_cda_expected")
	d := bindableDispatch("mis_wrong", "cda-mismatch")
	_, _, err := st.AcceptCircuitDispatch(context.Background(), runID, d, 1)
	if !errors.Is(err, ErrCircuitEffectMissionMismatch) {
		t.Fatalf("got %v", err)
	}
	if got, err := st.DispatchAcceptanceStore().LoadByIdempotency(d.IdempotencyKey); err != nil || got != nil {
		t.Fatalf("acceptance leaked after rollback: %+v %v", got, err)
	}
}

func TestAcceptCircuitDispatchExactReplayIsIdempotent(t *testing.T) {
	st, _ := openBrainStore(t)
	runID := createCircuitRunForCDA(t, st, "mis_cda_replay")
	d := bindableDispatch("mis_cda_replay", "cda-replay")
	a1, b1, err := st.AcceptCircuitDispatch(context.Background(), runID, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	a2, b2, err := st.AcceptCircuitDispatch(context.Background(), runID, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a1.WorksExecutionID != a2.WorksExecutionID || b1.ID != b2.ID {
		t.Fatalf("replay changed identities: %+v %+v %+v %+v", a1, b1, a2, b2)
	}
}

func TestAcceptCircuitDispatchCausalMismatchFailsClosed(t *testing.T) {
	st, _ := openBrainStore(t)
	runID := createCircuitRunForCDA(t, st, "mis_cda_causal")
	d := bindableDispatch("mis_cda_causal", "cda-causal")
	if _, _, err := st.AcceptCircuitDispatch(context.Background(), runID, d, 1); err != nil {
		t.Fatal(err)
	}
	bad := d
	bad.CausalID = "causal/changed"
	if _, _, err := st.AcceptCircuitDispatch(context.Background(), runID, bad, 1); !errors.Is(err, dispatch.ErrCausalMismatch) {
		t.Fatalf("got %v", err)
	}
}
func TestAcceptCircuitDispatchRollsBackNewAcceptanceOnBindingConflict(t *testing.T) {
	st, _ := openBrainStore(t)
	runID := createCircuitRunForCDA(t, st, "mis_cda_conflict")
	first := bindableDispatch("mis_cda_conflict", "cda-first")
	if _, _, err := st.AcceptCircuitDispatch(context.Background(), runID, first, 1); err != nil {
		t.Fatal(err)
	}
	second := bindableDispatch("mis_cda_conflict", "cda-second")
	second.EffectID = first.EffectID
	_, _, err := st.AcceptCircuitDispatch(context.Background(), runID, second, 1)
	if !errors.Is(err, ErrCircuitEffectBindingConflict) {
		t.Fatalf("got %v", err)
	}
	if got, err := st.DispatchAcceptanceStore().LoadByIdempotency(second.IdempotencyKey); err != nil || got != nil {
		t.Fatalf("conflicting acceptance leaked: %+v %v", got, err)
	}
}

func TestLegacyDispatchAcceptanceRemainsAvailableWithoutCircuitBinding(t *testing.T) {
	st, _ := openBrainStore(t)
	d := bindableDispatch("mis_legacy", "legacy")
	accepted, err := dispatch.NewAcceptor(st.DispatchAcceptanceStore(), nil).Accept(d, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCircuitEffectBindingByExecutionID(context.Background(), accepted.WorksExecutionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy accept unexpectedly bound to circuit: %v", err)
	}
}

func TestAcceptCircuitDispatchRollsBackWhenCircuitRunMissing(t *testing.T) {
	st, _ := openBrainStore(t)
	d := bindableDispatch("mis_missing_run", "missing-run")
	_, _, err := st.AcceptCircuitDispatch(context.Background(), "crun_0123456789abcdef0123456789abcdef", d, 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if got, err := st.DispatchAcceptanceStore().LoadByIdempotency(d.IdempotencyKey); err != nil || got != nil {
		t.Fatalf("phantom acceptance: %+v %v", got, err)
	}
}

func TestAcceptCircuitDispatchRejectsStaleAuthorityWithoutPersistence(t *testing.T) {
	st, _ := openBrainStore(t)
	runID := createCircuitRunForCDA(t, st, "mis_stale_auth")
	d := bindableDispatch("mis_stale_auth", "stale-auth")
	d.AuthorityEpoch = 1
	_, _, err := st.AcceptCircuitDispatch(context.Background(), runID, d, 2)
	if !errors.Is(err, dispatch.ErrStaleAuthority) {
		t.Fatalf("got %v", err)
	}
	if got, err := st.DispatchAcceptanceStore().LoadByIdempotency(d.IdempotencyKey); err != nil || got != nil {
		t.Fatalf("stale authority persisted: %+v %v", got, err)
	}
}
