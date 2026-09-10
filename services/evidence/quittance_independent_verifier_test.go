package evidence

import (
	"errors"
	"testing"
	"time"
)

func verifierVerdict(bundleID, result string) *VerificationVerdict {
	return &VerificationVerdict{
		BundleID:    bundleID,
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
		verifierVerdict("independent-2", "failed"),
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
	_, err := IssueQuittance(failedBundle("independent-3"), verifierVerdict("independent-3", "passed"), Usage{}, nil, nil, time.Now())
	if !errors.Is(err, ErrQuittanceVerdictConflict) {
		t.Fatalf("FAILED execution upgraded by passed verdict: got %v, want verdict conflict", err)
	}
}

func TestVerifierVerdictRequiresIndependentIdentityAndEvidence(t *testing.T) {
	cases := []VerificationVerdict{
		{BundleID: "independent-4", Result: "passed", EvidenceRef: "evidence/x", VerifiedAt: time.Now()},
		{BundleID: "independent-4", Result: "passed", VerifierID: "verifier/x", VerifiedAt: time.Now()},
		{BundleID: "independent-4", Result: "passed", VerifierID: "verifier/x", EvidenceRef: "evidence/x"},
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
	v1 := verifierVerdict("independent-5", "passed")
	v2 := verifierVerdict("independent-5", "passed")
	v2.EvidenceRef = "evidence/verdict-2"
	q1, err := IssueQuittance(passedBundle("independent-5"), v1, Usage{ComputeEUR: 1}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	q2, err := IssueQuittance(passedBundle("independent-5"), v2, Usage{ComputeEUR: 1}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if q1.Idempotency == q2.Idempotency {
		t.Fatal("different verifier evidence produced same quittance idempotency hash")
	}
	v3 := verifierVerdict("independent-5", "passed")
	v3.VerifierID = "verifier/independent-9"
	q3, err := IssueQuittance(passedBundle("independent-5"), v3, Usage{ComputeEUR: 1}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if q1.Idempotency == q3.Idempotency {
		t.Fatal("different verifier identity produced same quittance idempotency hash")
	}
	v4 := verifierVerdict("independent-5", "passed")
	v4.VerifiedAt = now.Add(time.Hour)
	q4, err := IssueQuittance(passedBundle("independent-5"), v4, Usage{ComputeEUR: 1}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if q1.Idempotency == q4.Idempotency {
		t.Fatal("different verification instant produced same quittance idempotency hash")
	}
}

func TestExecutorCannotActAsItsOwnVerifier(t *testing.T) {
	b := passedBundle("independent-6")
	self := verifierVerdict("independent-6", "passed")
	self.VerifierID = b.Runner.ID
	if _, err := IssueQuittance(b, self, Usage{}, nil, nil, time.Now()); !errors.Is(err, ErrQuittanceSelfVerification) {
		t.Fatalf("executor self-verification accepted: got %v, want self-verification refusal", err)
	}
	runnerless := passedBundle("independent-7")
	runnerless.Runner = nil
	if _, err := IssueQuittance(runnerless, verifierVerdict("independent-7", "passed"), Usage{}, nil, nil, time.Now()); !errors.Is(err, ErrQuittanceRunnerRequired) {
		t.Fatalf("runner-less bundle accepted: got %v, want runner-required refusal", err)
	}
}

func TestVerdictIsBoundToTheBundleItAssesses(t *testing.T) {
	v := verifierVerdict("independent-8", "passed")
	v.BundleID = "bundle/some-other-bundle"
	if _, err := IssueQuittance(passedBundle("independent-8"), v, Usage{}, nil, nil, time.Now()); !errors.Is(err, ErrQuittanceVerdictBundleMismatch) {
		t.Fatalf("cross-bundle verdict reuse accepted: got %v, want bundle-mismatch refusal", err)
	}
}

func TestSucceededFailedVerdictRequiresExplicitAttribution(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 0, 0, 0, time.UTC)
	if _, err := IssueQuittance(passedBundle("independent-9"), verifierVerdict("independent-9", "failed"), Usage{}, nil, nil, now); !errors.Is(err, ErrQuittanceAttributionRequired) {
		t.Fatalf("unattributed rejection of succeeded execution accepted: got %v, want attribution-required refusal", err)
	}
}
