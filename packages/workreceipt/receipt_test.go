package workreceipt

import (
	"encoding/json"
	"testing"
	"time"
)

func conformantReceipt() Receipt {
	return Receipt{
		WorkID:       "wrk_1",
		Executor:     Executor{Agent: "worker"},
		Intent:       "change source",
		AuthorityRef: "grant://1",
		Steps: []Step{
			{Kind: "tool", Status: "succeeded", Ref: "tool://example"},
		},
		Changes:      Changes{},
		Artifacts:    []string{},
		EvidenceRefs: []string{"evidence://diff/1"},
		Verification: Verification{State: "pending", Evaluations: []string{}},
		Outcome:      "completed",
		Invariants:   DefaultInvariants(),
	}
}

func TestReceiptCarriesCanonicalTruthBoundaryInvariants(t *testing.T) {
	r := conformantReceipt()
	got, err := New(r, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	want := DefaultInvariants()
	if len(got.Invariants) != len(want) {
		t.Fatalf("invariants=%v want=%v", got.Invariants, want)
	}
	for i := range want {
		if got.Invariants[i] != want[i] {
			t.Fatalf("invariant[%d]=%q want %q", i, got.Invariants[i], want[i])
		}
	}
}

func TestReceiptJSONContainsRequiredCanonicalFields(t *testing.T) {
	got, err := New(conformantReceipt(), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{
		"schema",
		"work_id",
		"recorded_at",
		"executor",
		"intent",
		"authority_ref",
		"steps",
		"artifacts",
		"evidence_refs",
		"verification",
		"outcome",
		"invariants",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("required governance field %q missing from receipt JSON: %s", key, raw)
		}
	}
}

func TestReceiptRejectsMissingCanonicalInvariants(t *testing.T) {
	r := conformantReceipt()
	r.Invariants = nil
	if _, err := New(r, time.Unix(0, 0)); err == nil {
		t.Fatal("expected receipt without canonical invariants to fail")
	}
}

func TestReceiptPreservesExecutionVerificationSeparation(t *testing.T) {
	r := conformantReceipt()
	r.Verification = Verification{State: "pending", Evaluations: []string{}}

	got, err := New(r, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.Outcome != "completed" || got.Verification.State != "pending" {
		t.Fatalf("execution outcome and verification state collapsed: %#v", got)
	}
}

func TestPassedVerificationRequiresIndependentVerifier(t *testing.T) {
	r := conformantReceipt()
	r.Verification = Verification{State: "passed", Evaluations: []string{}}
	if _, err := New(r, time.Unix(0, 0)); err == nil {
		t.Fatal("expected passed verification without verifier_ref to fail")
	}
}

func TestCompletedRequiresEvidence(t *testing.T) {
	r := conformantReceipt()
	r.EvidenceRefs = nil
	if _, err := New(r, time.Unix(0, 0)); err == nil {
		t.Fatal("expected completed work without evidence_refs to fail")
	}
}

func TestReceiptRejectsUnknownStepKindAndStatus(t *testing.T) {
	for name, mutate := range map[string]func(*Receipt){
		"kind": func(r *Receipt) { r.Steps[0].Kind = "arbitrary" },
		"status": func(r *Receipt) { r.Steps[0].Status = "verified" },
	} {
		t.Run(name, func(t *testing.T) {
			r := conformantReceipt()
			mutate(&r)
			if _, err := New(r, time.Unix(0, 0)); err == nil {
				t.Fatal("expected governance enum violation to fail")
			}
		})
	}
}
