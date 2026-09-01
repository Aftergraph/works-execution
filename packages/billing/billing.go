// Package billing implements the settlement law over the frozen
// quittance.rules/1.0 + kernel.budget/1.0 contracts (k-billing-01).
//
// Settlement is the moment a mission's budget clock is closed out against a
// quittance. The law it enforces:
//
//   - L1 (metering-quiet law): a settlement may only be issued while the
//     budget clock is STOPPED or PAUSED_WAITING_HUMAN. Issuing a settlement
//     while the clock is RUNNING would settle under active metering — a
//     violation. Unknown clock states fail closed.
//   - L2 (clamp law): settled consumed never exceeds the ceiling. The
//     ledger's Consume clamp is the source of truth; Settle preserves it
//     even if handed a ledger whose consumed field drifted past the ceiling
//     (the settlement clamps, it never amplifies the breach).
//   - L3 (late-bill evidence law): late bills are evidence-class data. They
//     are summed separately and never push settled consumption past the
//     ceiling — the operator absorbs them (ADR-0009).
//   - L4 (fail-closed law): missing or inconsistent input produces no
//     settlement — ledger without work_id, negative amounts anywhere,
//     unknown clock_state, quittance/ledger work_id mismatch.
package billing

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Clock states (kernel.budget/1.0 frozen enum).
const (
	ClockRunning         = "RUNNING"
	ClockPausedWaitHuman = "PAUSED_WAITING_HUMAN"
	ClockStopped         = "STOPPED"
)

// Hard stops (kernel.budget/1.0 frozen enum).
const (
	HardStopNone      = "none"
	HardStopWallClock = "wall_clock"
	HardStopCompute   = "compute"
)

// idempotencyHex matches the frozen quittance.rules/1.0 idempotency pattern
// (sha256 hex, 64 chars).
var idempotencyHex = regexp.MustCompile(`^[a-f0-9]{64}$`)

// QuittanceView is the minimal quittance surface settlement needs. It is
// satisfied by *evidence.Quittance (services/evidence) — billing depends on
// the shape, not on the evidence package (no import cycle, quittance.rules/1.0).
type QuittanceView interface {
	// QuittanceID is the quittance identifier (required by quittance.rules/1.0).
	QuittanceID() string
	// WorkID binds the quittance to one work (required, must equal ledger's).
	WorkID() string
	// Verification is "passed" | "failed" (required by quittance.rules/1.0).
	Verification() string
	// Idempotency is the content-addressed sha256 hex (64 chars, pattern law).
	Idempotency() string
}

// QuittanceRef is the plain-data QuittanceView: a reference to an already-
// issued quittance (services/evidence.Quittance shape) that settlement binds
// to. Billing never re-derives or re-prices a quittance — it only reads it.
type QuittanceRef struct {
	BundleID       string   `json:"bundle_id"`
	QuittanceIDF   string   `json:"quittance_id"`
	WorkIDF        string   `json:"work_id"`
	VerificationF  string   `json:"verification"`         // passed | failed
	PriceHint      *float64 `json:"price_hint,omitempty"` // nil ⇔ failed (kernel-negation)
	IdempotencyHex string   `json:"idempotency"`
}

func (r *QuittanceRef) QuittanceID() string  { return r.QuittanceIDF }
func (r *QuittanceRef) WorkID() string       { return r.WorkIDF }
func (r *QuittanceRef) Verification() string { return r.VerificationF }
func (r *QuittanceRef) Idempotency() string  { return r.IdempotencyHex }

// Settlement is the immutable record a Settle call produces. It mirrors the
// kernel.budget/1.0 view (consumed, ceiling, hard_stop, clock_state) plus the
// separately-summed evidence-class late bills.
type Settlement struct {
	WorkID     string               `json:"work_id"`
	Consumed   float64              `json:"consumed"`    // clamped at ceiling (L2)
	Ceiling    float64              `json:"ceiling"`     // compute_eur ceiling
	HardStop   string               `json:"hard_stop"`   // wall_clock | compute | none
	ClockState string               `json:"clock_state"` // STOPPED | PAUSED_WAITING_HUMAN
	LateBills  []workgraph.LateBill `json:"late_bill_entries,omitempty"`
	LateTotal  float64              `json:"late_bill_total"` // evidence-class sum (L3)
	// Quittance binding: the settlement is issued over exactly one quittance.
	QuittanceID  string `json:"quittance_id"`
	Verification string `json:"verification"` // passed | failed
	Idempotency  string `json:"idempotency"`  // quittance sha256 (64 hex)
}

// Settlement errors (fail-closed — L4).
var (
	ErrLedgerRequired       = errors.New("billing: ledger is required")
	ErrLedgerNoWorkID       = errors.New("billing: ledger.work_id is required")
	ErrNegativeConsumed     = errors.New("billing: ledger.consumed is negative")
	ErrNegativeReserved     = errors.New("billing: ledger.reserved is negative")
	ErrNegativeCeiling      = errors.New("billing: ledger.ceiling.compute_eur is negative")
	ErrNegativeLateBill     = errors.New("billing: late bill amount is negative")
	ErrUnknownClockState    = errors.New("billing: unknown clock_state")
	ErrSettleWhileRunning   = errors.New("billing: settlement under active metering (clock RUNNING) is a law violation")
	ErrQuittanceRequired    = errors.New("billing: quittance is required for settlement")
	ErrQuittanceWorkID      = errors.New("billing: quittance work_id does not match ledger work_id")
	ErrQuittanceIdempotency = errors.New("billing: quittance idempotency must be a 64-char sha256 hex (quittance.rules/1.0)")
)

// Settle closes out a mission's budget ledger against its quittance.
//
// Settle is a pure read over the ledger: it never mutates the ledger, never
// re-opens a stopped clock, and never lets late bills breach the ceiling.
// It fails closed on any missing or inconsistent input.
func Settle(ledger *workgraph.BudgetLedger, q QuittanceView) (*Settlement, error) {
	// --- L4: fail-closed input validation ---
	if ledger == nil {
		return nil, ErrLedgerRequired
	}
	if ledger.WorkID == "" {
		return nil, ErrLedgerNoWorkID
	}
	if ledger.Consumed < 0 {
		return nil, ErrNegativeConsumed
	}
	if ledger.Reserved < 0 {
		return nil, ErrNegativeReserved
	}
	if ledger.Ceiling.ComputeEUR < 0 {
		return nil, ErrNegativeCeiling
	}
	for _, lb := range ledger.LateBillEntries {
		if lb.AmountEUR < 0 {
			return nil, ErrNegativeLateBill
		}
	}

	// --- L1: settle only when metering is quiet ---
	switch ledger.ClockState {
	case ClockStopped, ClockPausedWaitHuman:
		// lawful settlement states
	case ClockRunning:
		return nil, ErrSettleWhileRunning
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownClockState, ledger.ClockState)
	}

	// --- quittance binding (quittance.rules/1.0 required fields) ---
	if q == nil {
		return nil, ErrQuittanceRequired
	}
	if q.WorkID() != ledger.WorkID {
		return nil, ErrQuittanceWorkID
	}
	idem := q.Idempotency()
	if !idempotencyHex.MatchString(idem) {
		return nil, ErrQuittanceIdempotency
	}

	// --- L2: clamp law — settled consumed never exceeds ceiling ---
	consumed := ledger.Consumed
	hardStop := ledger.HardStop
	if hardStop == "" {
		hardStop = HardStopNone
	}
	if consumed > ledger.Ceiling.ComputeEUR {
		// The ledger clamp (Consume) should have prevented this; if input
		// drifted, the settlement still honors the operator's committed
		// ceiling and flags the compute hard stop.
		consumed = ledger.Ceiling.ComputeEUR
		hardStop = HardStopCompute
	}

	// --- L3: late bills are evidence-class — summed separately, never
	// folded into consumed, never breaching the ceiling ---
	var lateTotal float64
	for _, lb := range ledger.LateBillEntries {
		lateTotal += lb.AmountEUR
	}

	return &Settlement{
		WorkID:       ledger.WorkID,
		Consumed:     consumed,
		Ceiling:      ledger.Ceiling.ComputeEUR,
		HardStop:     hardStop,
		ClockState:   ledger.ClockState,
		LateBills:    ledger.LateBillEntries,
		LateTotal:    lateTotal,
		QuittanceID:  q.QuittanceID(),
		Verification: q.Verification(),
		Idempotency:  idem,
	}, nil
}
