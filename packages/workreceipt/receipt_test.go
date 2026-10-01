package workreceipt

import (
	"testing"
	"time"
)

func baseReceipt() Receipt {
	return Receipt{
		WorkID:       "wrk_1",
		Executor:     Executor{Agent: "worker"},
		Intent:       "change source",
		AuthorityRef: "grant://1",
		Changes:      Changes{},
		EvidenceRefs: []string{"evidence://diff/1"},
		Verification: Verification{State: "pending"},
		Outcome:      "completed",
	}
}

func TestCompletedRequiresEvidence(t *testing.T) {
	r := baseReceipt()
	r.EvidenceRefs = nil
	if _, err := New(r, time.Unix(0, 0)); err == nil {
		t.Fatal("expected completed work without evidence to fail")
	}
}

func TestPassedVerificationRequiresIndependentVerifier(t *testing.T) {
	r := baseReceipt()
	r.Verification = Verification{State: "passed"}
	if _, err := New(r, time.Unix(0, 0)); err == nil {
		t.Fatal("expected passed verification without verifier ref to fail")
	}
}

func TestExecutionAndVerificationRemainSeparate(t *testing.T) {
	r := baseReceipt()
	got, err := New(r, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.Schema != Schema {
		t.Fatalf("schema = %q, want %q", got.Schema, Schema)
	}
	if got.Outcome != "completed" || got.Verification.State != "pending" {
		t.Fatalf("execution outcome and verification state collapsed: %#v", got)
	}
}
