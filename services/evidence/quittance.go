// Package evidence — k-evid-01 additions (ADR-0011 + ADR-0024).
//
// Quittance + FailureAttribution: the mission receipt layer. A quittance is
// an EXTENSION of the evidence bundle (not a separate store row — one source
// of truth), content-addressed through the same canonicalization, with the
// kernel-negation law baked in:
//
//	verification=failed  ⇒  price_hint MUST be absent (no payment claim)
//	duplicate quittance  ⇒  same idempotency hash ⇒ same quittance
//	missing evidence     ⇒  no quittance at all (Produce is the only writer)
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
	Driver Driver `json:"driver"`
	FromSeq int64 `json:"from_seq"`
	ToSeq   int64 `json:"to_seq"`
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
	FailWrongAssumption   = "wrong_assumption"    // agent acted on a false premise
	FailModelRejection    = "model_rejection"     // model declined/refused a step
	FailEnvironment       = "environment"         // infra/provider/network
	FailBudgetExhausted   = "budget_exhausted"    // ceiling hard-stop (ADR-0009)
	FailCorruptState      = "corrupt_state"       // checkpoint/handoff corruption
	FailPermissionDenied  = "permission"          // policy token refused the action
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
// + quittance.rules/1.0). Tokens are optional; EUR and wall-clock are not.
type Usage struct {
	ComputeEUR float64 `json:"compute_eur"`
	WallClockS int64   `json:"wall_clock_s"`
	Tokens     int64   `json:"tokens,omitempty"`
}

// Quittance is the settlement-grade receipt for a mission completion.
// Content-addressed via the bundle it extends; Idempotency is the sha256 of
// (bundle_id + verification + usage canonical JSON) so billing intake can
// deduplicate without trusting any caller.
type Quittance struct {
	BundleID       string              `json:"bundle_id"`
	WorkID         string              `json:"work_id"`
	Verification   string              `json:"verification"`         // passed | failed
	PriceHint      *float64            `json:"price_hint,omitempty"` // nil ⇔ failed (kernel-negation)
	Usage          Usage               `json:"usage"`
	Idempotency    string              `json:"idempotency"` // sha256 hex (64)
	DriverSegments []DriverSegment     `json:"driver_segments,omitempty"`
	Failure        *FailureAttribution `json:"failure,omitempty"`
	IssuedAt       time.Time           `json:"issued_at"`
}

// ErrQuittanceConflict mirrors the kernel-negation rules.
var (
	ErrQuittanceNoEvidence   = errors.New("quittance requires an evidence bundle (missing evidence cannot yield quittance)")
	ErrQuittanceFailedPriced = errors.New("failed verification cannot carry a price hint (kernel-negation, quittance.rules/1.0)")
	ErrQuittanceInvalidState = errors.New("quittance requires a terminal work state")
)

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
	if q.Verification == "failed" && q.PriceHint != nil {
		return ErrQuittanceFailedPriced
	}
	if q.Failure != nil && !ValidFailureCategory(q.Failure.Category) {
		return fmt.Errorf("quittance.failure.category %q not in frozen set", q.Failure.Category)
	}
	raw, err := json.Marshal(struct {
		BundleID     string  `json:"bundle_id"`
		Verification string  `json:"verification"`
		Price        *float64 `json:"price_hint"`
		ComputeEUR   float64 `json:"compute_eur"`
		WallClockS   int64   `json:"wall_clock_s"`
		Tokens       int64   `json:"tokens"`
	}{q.BundleID, q.Verification, q.PriceHint, q.Usage.ComputeEUR, q.Usage.WallClockS, q.Usage.Tokens})
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
// IssueQuittance is the ONLY way a Quittance comes into existence: it
// requires an already-produced evidence bundle (missing evidence cannot
// yield quittance — freeze law). Verification follows the bundle's terminal
// summary: SUCCEEDED → passed (price allowed), FAILED/CANCELLED → failed
// (kernel-negation forbids a price). Failure attribution must carry a
// category from the frozen closed set.
func IssueQuittance(b *Bundle, usage Usage, segs []DriverSegment, failure *FailureAttribution, now time.Time) (*Quittance, error) {
	if b == nil || b.BundleID == "" {
		return nil, ErrQuittanceNoEvidence
	}
	q := &Quittance{
		BundleID:       b.BundleID,
		WorkID:         b.WorkID,
		Usage:          usage,
		DriverSegments: segs,
	}
	switch b.Summary.Result {
	case workgraph.StateSucceeded:
		q.Verification = "passed"
		if failure != nil {
			return nil, errors.New("passed quittance cannot carry failure attribution")
		}
	case workgraph.StateFailed, workgraph.StateCancelled:
		q.Verification = "failed"
		if failure == nil {
			failure = &FailureAttribution{
				Category: FailEnvironment,
				Detail:   "kernel-terminal failure without deeper attribution",
				Driver:   DriverAgent,
				At:       now,
			}
		}
		if !ValidFailureCategory(failure.Category) {
			return nil, fmt.Errorf("failure category %q not in frozen closed set", failure.Category)
		}
		failure.At = failure.At.UTC()
		if failure.At.IsZero() {
			failure.At = now
		}
		q.Failure = failure
	default:
		return nil, fmt.Errorf("%w: %s", ErrQuittanceInvalidState, b.Summary.Result)
	}
	if err := q.derive(); err != nil {
		return nil, err
	}
	return q, nil
}
