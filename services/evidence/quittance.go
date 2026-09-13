// Package evidence — k-evid-01 additions (ADR-0011 + ADR-0024).
//
// Quittance + FailureAttribution: the mission receipt layer. A quittance is
// an EXTENSION of the evidence bundle (not a separate store row — one source
// of truth), content-addressed through the same canonicalization, with the
// kernel-negation and independent-verification laws baked in:
//
//	execution success != verified outcome
//	verification=failed  ⇒  price_hint MUST be absent (no payment claim)
//	duplicate quittance  ⇒  same idempotency hash ⇒ same quittance
//	missing evidence     ⇒  no quittance at all (Produce is the only writer)
//	verifier == executor ⇒  refused (self-verification is not independence)
//	verdict for bundle A ⇒  never settles bundle B (assessed-bundle binding)
//	failed verdict on success ⇒ explicit attribution required (the kernel
//	                            cannot attribute a disagreement it denies)
//
// Driver attribution (ADR-0014): each segment of the work carries its
// driver (agent|human) so a quittance distinguishes machine work from
// human takeover — billing semantics read this, they never infer it.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Driver identifies who was in control during an execution segment
// (ADR-0014: kernel-logged handover feeds this — never self-reported).
type Driver string

const (
	DriverAgent Driver = "agent"
	DriverHuman Driver = "human"
)

// DriverSegment attributes a slice of the work's timeline to a driver.
type DriverSegment struct {
	Driver  Driver `json:"driver"`
	FromSeq int64  `json:"from_seq"`
	ToSeq   int64  `json:"to_seq"`
	WorkID  string `json:"work_id,omitempty"`
}

// FailureAttribution records WHY a work failed (kernel-diagnosed, not
// agent-claimed). Categories are the closed set from ADR-0011; the agent
// gets no authoring path here — attribution is derived from kernel state.
type FailureAttribution struct {
	Category string    `json:"category"` // see failure categories below
	Detail   string    `json:"detail"`   // human-readable explanation
	Driver   Driver    `json:"driver"`   // who was driving at failure time
	At       time.Time `json:"at"`
	Evidence string    `json:"evidence,omitempty"` // ref into bundle records
}

// Failure categories (closed set — CUAErrorBench-informed):
const (
	FailWrongAssumption  = "wrong_assumption" // agent acted on a false premise
	FailModelRejection   = "model_rejection"  // model declined/refused a step
	FailEnvironment      = "environment"      // infra/provider/network
	FailBudgetExhausted  = "budget_exhausted" // ceiling hard-stop (ADR-0009)
	FailCorruptState     = "corrupt_state"    // checkpoint/handoff corruption
	FailPermissionDenied = "permission"       // policy token refused the action
)

// ValidFailureCategory reports whether c is in the frozen closed set.
func ValidFailureCategory(c string) bool {
	switch c {
	case FailWrongAssumption, FailModelRejection, FailEnvironment,
		FailBudgetExhausted, FailCorruptState, FailPermissionDenied:
		return true
	}
	return false
}

// Usage is the measured cost side of a completed mission (kernel.budget/1.0
// + quittance.rules/1.1). Tokens are optional; EUR and wall-clock are not.
type Usage struct {
	ComputeEUR float64 `json:"compute_eur"`
	WallClockS int64   `json:"wall_clock_s"`
	Tokens     int64   `json:"tokens,omitempty"`
}

// VerificationVerdict is an independently produced assessment of ONE
// evidence bundle. Execution state is an input to verification, never the
// verification result itself. Provenance is mandatory so replay/settlement
// can prove which verifier assessed which bundle on which evidence.
// BundleID binds the verdict to the assessed bundle: a verdict travels
// with its subject and can never be replayed onto another bundle.
type VerificationVerdict struct {
	BundleID    string    `json:"bundle_id"`    // assessed bundle (must equal the issued bundle)
	Result      string    `json:"result"`       // passed | failed
	VerifierID  string    `json:"verifier_id"`  // stable independent verifier identity
	EvidenceRef string    `json:"evidence_ref"` // immutable evidence/verdict reference
	VerifiedAt  time.Time `json:"verified_at"`
}

// Quittance is the settlement-grade receipt for a mission completion.
// Content-addressed via the bundle it extends; Idempotency includes verifier
// provenance so a different verifier decision/evidence cannot alias an old
// settlement receipt.
type Quittance struct {
	BundleID            string              `json:"bundle_id"`
	WorkID              string              `json:"work_id"`
	Verification        string              `json:"verification"`          // passed | failed
	VerifierID          string              `json:"verifier_id"`           // independent verifier identity
	VerifierEvidenceRef string              `json:"verifier_evidence_ref"` // immutable verdict evidence ref
	VerifiedAt          time.Time           `json:"verified_at"`
	PriceHint           *float64            `json:"price_hint,omitempty"` // nil ⇔ failed (kernel-negation)
	Usage               Usage               `json:"usage"`
	Idempotency         string              `json:"idempotency"` // sha256 hex (64)
	DriverSegments      []DriverSegment     `json:"driver_segments,omitempty"`
	Failure             *FailureAttribution `json:"failure,omitempty"`
	IssuedAt            time.Time           `json:"issued_at"`
}

// ErrQuittanceConflict mirrors the kernel-negation and verification laws.
var (
	ErrQuittanceNoEvidence            = errors.New("quittance requires an evidence bundle (missing evidence cannot yield quittance)")
	ErrQuittanceFailedPriced          = errors.New("failed verification cannot carry a price hint (kernel-negation, quittance.rules/1.1)")
	ErrQuittanceInvalidState          = errors.New("quittance requires a terminal work state")
	ErrQuittanceVerificationRequired  = errors.New("quittance requires an independent verifier verdict with assessed bundle, identity, evidence, and timestamp")
	ErrQuittanceVerdictConflict       = errors.New("verifier verdict conflicts with kernel terminal execution state")
	ErrQuittanceVerdictBundleMismatch = errors.New("verifier verdict does not assess this bundle (cross-bundle verdict reuse refused)")
	ErrQuittanceRunnerRequired        = errors.New("quittance requires a bundle runner identity (independence is unverifiable without an executor)")
	ErrQuittanceSelfVerification      = errors.New("verifier must be independent of the bundle executor (self-verification refused)")
	ErrQuittanceAttributionRequired   = errors.New("failed verdict on succeeded execution requires explicit failure attribution (the kernel cannot attribute a disagreement it denies)")
)

func validateVerdict(v *VerificationVerdict) error {
	if v == nil || v.BundleID == "" || v.VerifierID == "" || v.EvidenceRef == "" || v.VerifiedAt.IsZero() {
		return ErrQuittanceVerificationRequired
	}
	switch v.Result {
	case "passed", "failed":
		return nil
	default:
		return fmt.Errorf("%w: result must be passed|failed, got %q", ErrQuittanceVerificationRequired, v.Result)
	}
}

// QuittanceID derives the content-addressed id from the canonical quittance.
// Replay-safety: identical inputs always derive the identical id.
func (q *Quittance) derive() error {
	if q.BundleID == "" {
		return ErrQuittanceNoEvidence
	}
	switch q.Verification {
	case "passed", "failed":
	default:
		return fmt.Errorf("quittance.verification must be passed|failed, got %q", q.Verification)
	}
	if q.VerifierID == "" || q.VerifierEvidenceRef == "" || q.VerifiedAt.IsZero() {
		return ErrQuittanceVerificationRequired
	}
	if q.Verification == "failed" && q.PriceHint != nil {
		return ErrQuittanceFailedPriced
	}
	if q.Failure != nil && !ValidFailureCategory(q.Failure.Category) {
		return fmt.Errorf("quittance.failure.category %q not in frozen set", q.Failure.Category)
	}
	raw, err := json.Marshal(struct {
		BundleID            string   `json:"bundle_id"`
		Verification        string   `json:"verification"`
		VerifierID          string   `json:"verifier_id"`
		VerifierEvidenceRef string   `json:"verifier_evidence_ref"`
		VerifiedAt          string   `json:"verified_at"`
		Price               *float64 `json:"price_hint"`
		ComputeEUR          float64  `json:"compute_eur"`
		WallClockS          int64    `json:"wall_clock_s"`
		Tokens              int64    `json:"tokens"`
	}{
		BundleID:            q.BundleID,
		Verification:        q.Verification,
		VerifierID:          q.VerifierID,
		VerifierEvidenceRef: q.VerifierEvidenceRef,
		VerifiedAt:          q.VerifiedAt.UTC().Format(time.RFC3339Nano),
		Price:               q.PriceHint,
		ComputeEUR:          q.Usage.ComputeEUR,
		WallClockS:          q.Usage.WallClockS,
		Tokens:              q.Usage.Tokens,
	})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	q.Idempotency = hex.EncodeToString(sum[:])
	if q.IssuedAt.IsZero() {
		q.IssuedAt = time.Now().UTC()
	}
	return nil
}

// IssueQuittance is the ONLY way a Quittance comes into existence. It
// requires both an already-produced evidence bundle and an independent
// verifier verdict. Kernel execution state constrains what the verifier may
// assert, but never self-issues verification:
//
//	SUCCEEDED + passed verdict -> verified settlement
//	SUCCEEDED + failed verdict -> failed quittance (no price)
//	FAILED/CANCELLED + failed verdict -> failed quittance (no price)
//	FAILED/CANCELLED + passed verdict -> conflict, refused
//
// Failure attribution must carry a category from the frozen closed set.
func IssueQuittance(b *Bundle, verdict *VerificationVerdict, usage Usage, segs []DriverSegment, failure *FailureAttribution, now time.Time) (*Quittance, error) {
	if b == nil || b.BundleID == "" {
		return nil, ErrQuittanceNoEvidence
	}
	if err := validateVerdict(verdict); err != nil {
		return nil, err
	}
	if verdict.BundleID != b.BundleID {
		return nil, fmt.Errorf("%w: verdict assesses %q, bundle is %q", ErrQuittanceVerdictBundleMismatch, verdict.BundleID, b.BundleID)
	}
	if b.Runner == nil || b.Runner.ID == "" {
		return nil, ErrQuittanceRunnerRequired
	}
	if verdict.VerifierID == b.Runner.ID {
		return nil, fmt.Errorf("%w: verifier %q executed bundle %q", ErrQuittanceSelfVerification, verdict.VerifierID, b.BundleID)
	}

	q := &Quittance{
		BundleID:            b.BundleID,
		WorkID:              b.WorkID,
		Verification:        verdict.Result,
		VerifierID:          verdict.VerifierID,
		VerifierEvidenceRef: verdict.EvidenceRef,
		VerifiedAt:          verdict.VerifiedAt.UTC(),
		Usage:               usage,
		DriverSegments:      segs,
	}

	switch b.Summary.Result {
	case workgraph.StateSucceeded:
		if verdict.Result == "passed" {
			if failure != nil {
				return nil, errors.New("passed quittance cannot carry failure attribution")
			}
		} else {
			if failure == nil {
				return nil, ErrQuittanceAttributionRequired
			}
			q.Failure = failure
		}
	case workgraph.StateFailed, workgraph.StateCancelled:
		if verdict.Result == "passed" {
			return nil, ErrQuittanceVerdictConflict
		}
		if failure == nil {
			failure = &FailureAttribution{
				Category: FailEnvironment,
				Detail:   "kernel-terminal failure without deeper attribution",
				Driver:   DriverAgent,
				At:       now,
			}
		}
		q.Failure = failure
	default:
		return nil, fmt.Errorf("%w: %s", ErrQuittanceInvalidState, b.Summary.Result)
	}

	if q.Failure != nil {
		if !ValidFailureCategory(q.Failure.Category) {
			return nil, fmt.Errorf("failure category %q not in frozen closed set", q.Failure.Category)
		}
		q.Failure.At = q.Failure.At.UTC()
		if q.Failure.At.IsZero() {
			q.Failure.At = now.UTC()
		}
	}

	if err := q.derive(); err != nil {
		return nil, err
	}
	return q, nil
}
