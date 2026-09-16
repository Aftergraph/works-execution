package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/circuitrun"
)

func createAppliedEffectReceipt(t *testing.T, st *SQLiteStore, suffix string) *circuitrun.EffectReceipt {
	t.Helper()
	binding := createCircuitEffectForReceipt(t, st, suffix)
	r, err := st.RecordCircuitEffectOutcome(context.Background(), circuitrun.EffectReceiptInput{
		EffectBindingID: binding.ID, State: circuitrun.EffectApplied, ExecutorID: "runtime-1",
		EvidenceRef: "evidence://effect/" + suffix, RecordedAt: time.Unix(40, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCreateCircuitEffectVerdictBindsExactReceipt(t *testing.T) {
	st, _ := openBrainStore(t)
	r := createAppliedEffectReceipt(t, st, "verdict")
	v, err := st.CreateCircuitEffectVerdict(context.Background(), circuitrun.EffectVerdictInput{
		EffectReceiptID: r.ID, Result: "ACCEPT", VerifierID: "witness-1",
		EvidenceRef: "evidence://witness/verdict", VerifiedAt: time.Unix(50, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.ReceiptSHA256 != r.ReceiptSHA256 || v.VerificationSubject != r.VerificationSubject {
		t.Fatalf("verdict subject drift: %+v receipt=%+v", v, r)
	}
}
func TestCreateCircuitEffectVerdictRejectsExecutorAsVerifier(t *testing.T) {
	st, _ := openBrainStore(t)
	r := createAppliedEffectReceipt(t, st, "self")
	_, err := st.CreateCircuitEffectVerdict(context.Background(), circuitrun.EffectVerdictInput{
		EffectReceiptID: r.ID, Result: "ACCEPT", VerifierID: "runtime-1",
		EvidenceRef: "evidence://witness/self", VerifiedAt: time.Unix(51, 0).UTC(),
	})
	if !errors.Is(err, ErrCircuitEffectVerifierNotIndependent) {
		t.Fatalf("expected independent verifier failure, got %v", err)
	}
}

func TestCircuitEffectVerdictDoesNotPromoteCircuitVerdict(t *testing.T) {
	st, _ := openBrainStore(t)
	r := createAppliedEffectReceipt(t, st, "no-promotion")
	if _, err := st.CreateCircuitEffectVerdict(context.Background(), circuitrun.EffectVerdictInput{
		EffectReceiptID: r.ID, Result: "ACCEPT", VerifierID: "witness-1",
		EvidenceRef: "evidence://witness/no-promotion", VerifiedAt: time.Unix(52, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCircuitVerdict(context.Background(), r.CircuitRunID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("effect verdict must not auto-promote CircuitVerdict: %v", err)
	}
}
