package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/circuitrun"
)

func TestGoldenCircuitEndToEnd(t *testing.T) {
	subject := os.Getenv("GOLDEN_VERIFICATION_SUBJECT")
	if subject == "" {
		t.Skip("GOLDEN_VERIFICATION_SUBJECT not set")
	}
	ctx := context.Background()
	st, _ := openBrainStore(t)
	runID := createCircuitRunForCDA(t, st, "mis_golden_circuit")
	d := bindableDispatch("mis_golden_circuit", "golden-circuit")
	d.VerificationSubj = subject
	d.AuthorityEpoch = 7
	accepted, binding, err := st.AcceptCircuitDispatch(ctx, runID, d, 7)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := st.GetLatestCircuitEffectReceipt(ctx, binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seed.State != circuitrun.EffectDispatched {
		t.Fatalf("seed=%+v", seed)
	}
	applied, err := st.RecordCircuitEffectOutcome(ctx, circuitrun.EffectReceiptInput{
		EffectBindingID: binding.ID,
		State:           circuitrun.EffectApplied,
		ExecutorID:      "runtime-golden",
		EvidenceRef:     "evidence://golden/effect-applied",
		RecordedAt:      time.Unix(100, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := st.CreateCircuitEffectVerdict(ctx, circuitrun.EffectVerdictInput{
		EffectReceiptID: applied.ID,
		Result:          "ACCEPT",
		VerifierID:      "witness-golden",
		EvidenceRef:     "evidence://golden/witness",
		VerifiedAt:      time.Unix(110, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if verdict.VerificationSubject != subject {
		t.Fatalf("subject drift: %q != %q", verdict.VerificationSubject, subject)
	}

	out := map[string]any{
		"schema_version":        "golden-circuit-works-evidence/0.1",
		"circuit_run_id":        runID,
		"works_execution_id":    accepted.WorksExecutionID,
		"effect_binding_id":     binding.ID,
		"effect_id":             binding.EffectID,
		"verification_subject":  subject,
		"effect_receipt_id":     applied.ID,
		"effect_receipt_sha256": applied.ReceiptSHA256,
		"effect_state":          applied.State,
		"witness_result":        verdict.Result,
		"verifier_id":           verdict.VerifierID,
	}
	if path := os.Getenv("GOLDEN_EVIDENCE_OUT"); path != "" {
		payload, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		payload = append(payload, '\n')
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
