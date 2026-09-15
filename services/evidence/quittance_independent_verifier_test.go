package evidence

import (
	"errors"
	"testing"
	"time"
)

func verifierVerdict(result string) *VerificationVerdict {
	return &VerificationVerdict{
		Result:      result,
		VerifierID:  "verifier/independent-1",
		EvidenceRef: "evidence/verdict-1",
		VerifiedAt:  time.Date(2026, 9, 9, 4, 0, 0, 0, time.UTC),
	}
}

func TestSucceededExecutionWithoutVerifierVerdictCannotIssueQuittance(t *testing.T) {
	_, err := IssueQuittance(passedBundle("independent-1"), nil, Usage{}, nil, nil, time.Now())
	if !errors.Is(err, ErrQuittanceVerificationRequired) {
		t.Fatalf("SUCCEEDED execution self-upgraded to quittance: got %v, want verification-required", err)
	}
}

func TestSucceededExecutionCanBeRejectedByIndependentVerifier(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 0, 0, 0, time.UTC)
	q, err := IssueQuittance(
		passedBundle("independent-2"),
		verifierVerdict("failed"),
		Usage{ComputeEUR: 1.0, WallClockS: 60},
		nil,
		&FailureAttribution{Category: FailWrongAssumption, Detail: "acceptance criteria unmet", Driver: DriverAgent, At: now},
		now,
	)
	if err != nil {
		t.Fatalf("independent rejection of succeeded execution should produce failed quittance: %v", err)
	}
	if q.Verification != "failed" {
		t.Fatalf("verification=%q, want failed", q.Verification)
	}
	if q.PriceHint != nil {
		t.Fatal("failed independent verification must not carry a price")
	}
	if q.VerifierID != "verifier/independent-1" || q.VerifierEvidenceRef != "evidence/verdict-1" {
		t.Fatalf("verifier provenance lost: %+v", q)
	}
}

func TestFailedExecutionCannotReceivePassedVerifierVerdict(t *testing.T) {
	_, err := IssueQuittance(failedBundle("independent-3"), verifierVerdict("passed"), Usage{}, nil, nil, time.Now())
	if !errors.Is(err, ErrQuittanceVerdictConflict) {
		t.Fatalf("FAILED execution upgraded by passed verdict: got %v, want verdict conflict", err)
	}
}

func TestVerifierVerdictRequiresIndependentIdentityAndEvidence(t *testing.T) {
	cases := []VerificationVerdict{
		{Result: "passed", EvidenceRef: "evidence/x", VerifiedAt: time.Now()},
		{Result: "passed", VerifierID: "verifier/x", VerifiedAt: time.Now()},
		{Result: "passed", VerifierID: "verifier/x", EvidenceRef: "evidence/x"},
	}
	for i := range cases {
		_, err := IssueQuittance(passedBundle("independent-4"), &cases[i], Usage{}, nil, nil, time.Now())
		if !errors.Is(err, ErrQuittanceVerificationRequired) {
			t.Fatalf("case %d accepted incomplete verifier provenance: %v", i, err)
		}
	}
}

func TestVerdictParticipatesInIdempotency(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 0, 0, 0, time.UTC)
	v1 := verifierVerdict("passed")
	v2 := verifierVerdict("passed")
	v2.EvidenceRef = "evidence/verdict-2"
	q1, err := IssueQuittance(passedBundle("independent-5"), v1, Usage{ComputeEUR: 1}, nil, nil, now)
	if err != nil { t.Fatal(err) }
	q2, err := IssueQuittance(passedBundle("independent-5"), v2, Usage{ComputeEUR: 1}, nil, nil, now)
	if err != nil { t.Fatal(err) }
	if q1.Idempotency == q2.Idempotency {
		t.Fatal("different verifier evidence produced same quittance idempotency hash")
	}
}
