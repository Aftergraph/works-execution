package circuitrun

import (
	"testing"
	"time"
)

func TestEffectReceiptTransitionsSeparateAppliedFromVerified(t *testing.T) {
	if !CanTransitionEffect(EffectDispatched, EffectApplied) {
		t.Fatal("DISPATCHED -> APPLIED must be valid")
	}
	if !CanTransitionEffect(EffectDispatched, EffectFailed) {
		t.Fatal("DISPATCHED -> FAILED must be valid")
	}
	if !CanTransitionEffect(EffectApplied, EffectCompensated) {
		t.Fatal("APPLIED -> COMPENSATED must be valid")
	}
	if CanTransitionEffect(EffectApplied, EffectFailed) {
		t.Fatal("APPLIED -> FAILED must not rewrite outcome")
	}
}

func TestNewEffectReceiptHasStableExactDigest(t *testing.T) {
	in := EffectReceiptInput{EffectBindingID: "ceff_0123456789abcdef0123456789abcdef", State: EffectApplied,
		ExecutorID: "runtime-1", EvidenceRef: "evidence://effect/1", RecordedAt: time.Unix(10, 0).UTC()}
	binding := EffectBinding{ID: in.EffectBindingID, CircuitRunID: "crun_0123456789abcdef0123456789abcdef",
		EffectID: "effect/restart", VerificationSubject: "execution-subject:" + string(make([]byte, 64)), DispatchSHA256: "a"}
	first, err := NewEffectReceipt(binding, 2, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEffectReceipt(binding, 2, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReceiptSHA256 != second.ReceiptSHA256 || len(first.ReceiptSHA256) != 64 {
		t.Fatalf("unstable digest: %+v %+v", first, second)
	}
}
