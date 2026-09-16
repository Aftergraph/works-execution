package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/circuitrun"
)

func createCircuitEffectForReceipt(t *testing.T, st *SQLiteStore, suffix string) *circuitrun.EffectBinding {
	t.Helper()
	runID := createCircuitRunForCDA(t, st, "mis_receipt_"+suffix)
	d := bindableDispatch("mis_receipt_"+suffix, "receipt-"+suffix)
	_, binding, err := st.AcceptCircuitDispatch(context.Background(), runID, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestCDASeedsDispatchedEffectReceiptAtomically(t *testing.T) {
	st, _ := openBrainStore(t)
	binding := createCircuitEffectForReceipt(t, st, "seed")
	receipt, err := st.GetLatestCircuitEffectReceipt(context.Background(), binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != circuitrun.EffectDispatched || receipt.Sequence != 1 || receipt.EffectBindingID != binding.ID {
		t.Fatalf("unexpected initial receipt: %+v", receipt)
	}
}

func TestRecordCircuitEffectOutcomeSeparatesAppliedFromVerified(t *testing.T) {
	st, _ := openBrainStore(t)
	binding := createCircuitEffectForReceipt(t, st, "applied")
	r, err := st.RecordCircuitEffectOutcome(context.Background(), circuitrun.EffectReceiptInput{
		EffectBindingID: binding.ID, State: circuitrun.EffectApplied, ExecutorID: "runtime-1",
		EvidenceRef: "evidence://effect/applied", RecordedAt: time.Unix(20, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.State != circuitrun.EffectApplied || r.Sequence != 2 {
		t.Fatalf("unexpected receipt: %+v", r)
	}
	if _, err := st.GetCircuitEffectVerdict(context.Background(), r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("APPLIED must not imply VERIFIED: %v", err)
	}
	_, err = st.RecordCircuitEffectOutcome(context.Background(), circuitrun.EffectReceiptInput{
		EffectBindingID: binding.ID, State: circuitrun.EffectFailed, ExecutorID: "runtime-1",
		EvidenceRef: "evidence://effect/late-failure", RecordedAt: time.Unix(21, 0).UTC(),
	})
	if !errors.Is(err, ErrCircuitEffectReceiptTransition) {
		t.Fatalf("expected transition error, got %v", err)
	}
}

func TestEffectReceiptAllowsCompensationAfterApplied(t *testing.T) {
	st, _ := openBrainStore(t)
	binding := createCircuitEffectForReceipt(t, st, "compensate")
	_, err := st.RecordCircuitEffectOutcome(context.Background(), circuitrun.EffectReceiptInput{EffectBindingID: binding.ID,
		State: circuitrun.EffectApplied, ExecutorID: "runtime-1", EvidenceRef: "evidence://applied", RecordedAt: time.Unix(30, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	r, err := st.RecordCircuitEffectOutcome(context.Background(), circuitrun.EffectReceiptInput{EffectBindingID: binding.ID,
		State: circuitrun.EffectCompensated, ExecutorID: "runtime-2", EvidenceRef: "evidence://compensated", RecordedAt: time.Unix(31, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if r.State != circuitrun.EffectCompensated || r.Sequence != 3 {
		t.Fatalf("unexpected compensation: %+v", r)
	}
}
