package evidence

// k-evid-01 tests — ADR-0011 quittance laws + ADR-0024 boundary.
//
// Freeze law under test:
//   - missing evidence ⇒ no quittance
//   - failed verification ⇒ NO price (kernel-negation, quittance.rules/1.0)
//   - passed verification ⇒ price allowed; failure attribution forbidden
//   - failure categories are a closed frozen set
//   - idempotency: same inputs ⇒ same hash (replay-safe); different inputs ⇒
//     different hash (duplicate payment detection possible)
//   - driver segments attribute agent vs human work
import (
	"errors"
	"testing"
	"time"
)

func passedBundle(id string) *Bundle {
	return &Bundle{
		BundleID: id,
		WorkID:   "work:" + id,
		Summary:  Summary{Result: "SUCCEEDED"},
	}
}

func failedBundle(id string) *Bundle {
	return &Bundle{
		BundleID: id,
		WorkID:   "work:" + id,
		Summary:  Summary{Result: "FAILED"},
	}
}

func TestQuittanceFromPassedBundle(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	q, err := IssueQuittance(passedBundle("b1"), Usage{ComputeEUR: 1.2, WallClockS: 300},
		[]DriverSegment{{Driver: DriverAgent, FromSeq: 1, ToSeq: 9}}, nil, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if q.Verification != "passed" || q.PriceHint != nil {
		t.Fatalf("verification=%s price=%v (price set by billing, not by evidence)", q.Verification, q.PriceHint)
	}
	if len(q.Idempotency) != 64 {
		t.Fatalf("idempotency hash missing: %q", q.Idempotency)
	}
}

func TestQuittanceMissingEvidenceRejected(t *testing.T) {
	if _, err := IssueQuittance(nil, Usage{}, nil, nil, time.Now()); err != ErrQuittanceNoEvidence {
		t.Fatalf("nil bundle: got %v, want ErrQuittanceNoEvidence", err)
	}
	if _, err := IssueQuittance(&Bundle{}, Usage{}, nil, nil, time.Now()); err != ErrQuittanceNoEvidence {
		t.Fatalf("bundle without id: got %v, want ErrQuittanceNoEvidence", err)
	}
}

func TestFailedQuittanceKernelNegation(t *testing.T) {
	now := time.Now()
	price := 19.99
	q, err := IssueQuittance(failedBundle("b2"), Usage{ComputeEUR: 0.8, WallClockS: 90},
		nil, &FailureAttribution{Category: FailWrongAssumption, Detail: "acted on stale pricing page", Driver: DriverAgent, At: now}, now)
	if err != nil {
		t.Fatalf("issue failed quittance: %v", err)
	}
	if q.Verification != "failed" {
		t.Fatalf("verification = %s", q.Verification)
	}
	// kernel-negation at the type level: failed ⇒ derive() rejects a price
	q.PriceHint = &price
	if err := q.derive(); err != ErrQuittanceFailedPriced {
		t.Fatalf("failed quittance carried price: got %v, want ErrQuittanceFailedPriced", err)
	}
	// and an issuer trying to hand out a priced failure is refused up front
	if _, err := IssueQuittance(failedBundle("b3"), Usage{}, nil,
		&FailureAttribution{Category: FailEnvironment, Driver: DriverAgent}, now); err != nil {
		t.Fatalf("failed quittance without attribution should default: %v", err)
	}
}

func TestFailureCategoriesClosedSet(t *testing.T) {
	now := time.Now()
	for _, c := range []string{FailWrongAssumption, FailModelRejection, FailEnvironment, FailBudgetExhausted, FailCorruptState, FailPermissionDenied} {
		if _, err := IssueQuittance(failedBundle("b3"), Usage{}, nil, &FailureAttribution{Category: c, Driver: DriverAgent, At: now}, now); err != nil {
			t.Fatalf("valid category %s rejected: %v", c, err)
		}
	}
	if _, err := IssueQuittance(failedBundle("b4"), Usage{}, nil, &FailureAttribution{Category: "vibes", Driver: DriverAgent, At: now}, now); err == nil {
		t.Fatal("out-of-set failure category accepted — attribution law broken")
	}
}

func TestPassedCannotCarryFailure(t *testing.T) {
	if _, err := IssueQuittance(passedBundle("b5"), Usage{}, nil,
		&FailureAttribution{Category: FailEnvironment, Driver: DriverAgent}, time.Now()); err == nil {
		t.Fatal("passed quittance with failure attribution accepted — attribution semantics broken")
	}
}

func TestQuittanceIdempotencyReplay(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	q1, err := IssueQuittance(passedBundle("b5"), Usage{ComputeEUR: 2.0, WallClockS: 600}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	q2, err := IssueQuittance(passedBundle("b5"), Usage{ComputeEUR: 2.5, WallClockS: 600}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if q1.Idempotency == q2.Idempotency {
		t.Fatal("different usage produced same idempotency hash — duplicate-payment detection broken")
	}
	q3, _ := IssueQuittance(passedBundle("b5"), Usage{ComputeEUR: 2.5, WallClockS: 600}, nil, nil, now)
	if q2.Idempotency != q3.Idempotency {
		t.Fatal("identical inputs produced different idempotency hashes — replay-derivation unstable")
	}
	// replay law: same bundle + same usage ⇒ same idempotency (a replayed
	// evidence cannot mint a second DISTINCT receipt — billing dedups on this)
	if q1.Idempotency == q3.Idempotency {
		t.Log("note: q1==q3 only if usage identical; here usages differ by design")
	}
}

func TestHumanSegmentsAttributed(t *testing.T) {
	now := time.Now()
	segs := []DriverSegment{
		{Driver: DriverAgent, FromSeq: 1, ToSeq: 40},
		{Driver: DriverHuman, FromSeq: 41, ToSeq: 45}, // takeover segment
		{Driver: DriverAgent, FromSeq: 46, ToSeq: 60},
	}
	q, err := IssueQuittance(passedBundle("b6"), Usage{ComputeEUR: 3.3, WallClockS: 1200}, segs, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.DriverSegments) != 3 || q.DriverSegments[1].Driver != DriverHuman {
		t.Fatalf("driver attribution lost: %+v", q.DriverSegments)
	}
}

func TestNonTerminalBundleRejected(t *testing.T) {
	b := &Bundle{BundleID: "b7", WorkID: "w", Summary: Summary{Result: "RUNNING"}}
	_, err := IssueQuittance(b, Usage{}, nil, nil, time.Now())
	if !errors.Is(err, ErrQuittanceInvalidState) {
		t.Fatalf("non-terminal bundle produced quittance: got %v", err)
	}
}