// k-billing-01 tests — settlement law over quittance.rules/1.0 +
// kernel.budget/1.0.
//
// Freeze law under test:
//   - L1: settlement only on STOPPED / PAUSED_WAITING_HUMAN (never RUNNING,
//     never unknown clock states)
//   - L2: consumed never exceeds ceiling (clamp preserved under drift)
//   - L3: late bills are evidence-class — summed separately, never folded
//     into consumption, never breach the ceiling
//   - L4: fail-closed on missing/inconsistent input (no work_id, negative
//     amounts, unknown clock state, quittance mismatch / bad idempotency)
//   - freeze compatibility: no mutation of the ledger, settlement survives
//     the ledger's own Consume/PauseClock/RecordLateBill semantics
package billing_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/billing"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

const (
	validIDem = "a3f2c4d5e6b7a8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3"
)

func ledger() *workgraph.BudgetLedger {
	return &workgraph.BudgetLedger{
		WorkID:     "work_01234567",
		Ceiling:    workgraph.BudgetCeiling{ComputeEUR: 10.0, WallClockH: 2.0},
		Reserved:   2.0,
		Consumed:   5.0,
		ClockState: billing.ClockStopped,
	}
}

func quittance() *billing.QuittanceRef {
	return &billing.QuittanceRef{
		BundleID:       "bundle_abc",
		QuittanceIDF:   "quit_001",
		WorkIDF:        "work_01234567",
		VerificationF:  "passed",
		IdempotencyHex: validIDem,
	}
}

// TestSettleL1ClockLaw — settlement only when metering is quiet.
func TestSettleL1ClockLaw(t *testing.T) {
	tests := []struct {
		name       string
		clockState string
		wantErr    error
		wantClock  string
	}{
		{name: "stopped settles", clockState: billing.ClockStopped, wantClock: billing.ClockStopped},
		{name: "paused_waiting_human settles", clockState: billing.ClockPausedWaitHuman, wantClock: billing.ClockPausedWaitHuman},
		{name: "running is law violation", clockState: billing.ClockRunning, wantErr: billing.ErrSettleWhileRunning},
		{name: "unknown clock fails closed", clockState: "FAST_FORWARD", wantErr: billing.ErrUnknownClockState},
		{name: "empty clock fails closed", clockState: "", wantErr: billing.ErrUnknownClockState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := ledger()
			l.ClockState = tt.clockState
			s, err := billing.Settle(l, quittance())
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("want err %v, got nil settlement", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("want err %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if s.ClockState != tt.wantClock {
				t.Fatalf("clock_state: want %q, got %q", tt.wantClock, s.ClockState)
			}
		})
	}
}

// TestSettleL2ClampLaw — consumed never exceeds ceiling.
func TestSettleL2ClampLaw(t *testing.T) {
	tests := []struct {
		name     string
		consumed float64
		ceiling  float64
		wantCon  float64
		wantStop string
	}{
		{name: "under ceiling keeps consumed", consumed: 5.0, ceiling: 10.0, wantCon: 5.0, wantStop: billing.HardStopNone},
		{name: "at ceiling keeps consumed", consumed: 10.0, ceiling: 10.0, wantCon: 10.0, wantStop: billing.HardStopNone},
		{name: "drifted past ceiling clamps", consumed: 15.0, ceiling: 10.0, wantCon: 10.0, wantStop: billing.HardStopCompute},
		{name: "zero consumed stays zero", consumed: 0, ceiling: 10.0, wantCon: 0, wantStop: billing.HardStopNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := ledger()
			l.Consumed = tt.consumed
			l.Ceiling.ComputeEUR = tt.ceiling
			s, err := billing.Settle(l, quittance())
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if s.Consumed > s.Ceiling {
				t.Fatalf("L2 violated: consumed %.2f > ceiling %.2f", s.Consumed, s.Ceiling)
			}
			if s.Consumed != tt.wantCon {
				t.Fatalf("consumed: want %.2f, got %.2f", tt.wantCon, s.Consumed)
			}
			if s.HardStop != tt.wantStop {
				t.Fatalf("hard_stop: want %q, got %q", tt.wantStop, s.HardStop)
			}
		})
	}
}

// TestSettleL2ClampPreservedByLedgerConsume — the clamp law holds when the
// ledger itself was driven to the ceiling via Consume (kernel semantics).
func TestSettleL2ClampPreservedByLedgerConsume(t *testing.T) {
	l := ledger()
	l.ClockState = billing.ClockRunning
	l.Consume(8.0)  // consumed 8, still RUNNING
	l.Consume(99.0) // clamps at 10, stops clock, hard stop = compute
	s, err := billing.Settle(l, quittance())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if s.Consumed != 10.0 {
		t.Fatalf("consumed: want 10.0 (clamped), got %.2f", s.Consumed)
	}
	if s.HardStop != billing.HardStopCompute {
		t.Fatalf("hard_stop: want compute, got %q", s.HardStop)
	}
	if s.ClockState != billing.ClockStopped {
		t.Fatalf("clock_state: want STOPPED, got %q", s.ClockState)
	}
}

// TestSettleL3LateBillEvidenceLaw — late bills are evidence-class only.
func TestSettleL3LateBillEvidenceLaw(t *testing.T) {
	tests := []struct {
		name      string
		lateBills []workgraph.LateBill
		wantTotal float64
	}{
		{
			name:      "no late bills",
			lateBills: nil,
			wantTotal: 0,
		},
		{
			name:      "single late bill summed separately",
			lateBills: []workgraph.LateBill{{AmountEUR: 3.5, Reason: "post-teardown provider bill"}},
			wantTotal: 3.5,
		},
		{
			name: "multiple late bills summed",
			lateBills: []workgraph.LateBill{
				{AmountEUR: 2.0, Reason: "gpu overage"},
				{AmountEUR: 1.25, Reason: "egress"},
			},
			wantTotal: 3.25,
		},
		{
			name: "late bills exceeding ceiling still do not touch consumed",
			lateBills: []workgraph.LateBill{
				{AmountEUR: 50.0, Reason: "provider invoiced after settlement window"},
			},
			wantTotal: 50.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := ledger()
			l.Consumed = 7.0
			l.Ceiling.ComputeEUR = 10.0
			l.LateBillEntries = tt.lateBills
			s, err := billing.Settle(l, quittance())
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if s.Consumed != 7.0 {
				t.Fatalf("L3: consumed must be untouched by late bills, got %.2f", s.Consumed)
			}
			if s.Consumed > s.Ceiling {
				t.Fatalf("L2 violated by late bills: %.2f > %.2f", s.Consumed, s.Ceiling)
			}
			if s.LateTotal != tt.wantTotal {
				t.Fatalf("late total: want %.2f, got %.2f", tt.wantTotal, s.LateTotal)
			}
			if len(s.LateBills) != len(tt.lateBills) {
				t.Fatalf("late bill count: want %d, got %d", len(tt.lateBills), len(s.LateBills))
			}
		})
	}
}

// TestSettleL4FailClosed — missing/inconsistent input yields no settlement.
func TestSettleL4FailClosed(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*workgraph.BudgetLedger, *billing.QuittanceRef)
		wantErr error
	}{
		{
			name:    "nil ledger",
			mutate:  func(l *workgraph.BudgetLedger, q *billing.QuittanceRef) { _ = l },
			wantErr: billing.ErrLedgerRequired,
		},
		{
			name:    "ledger without work_id",
			mutate:  func(l *workgraph.BudgetLedger, _ *billing.QuittanceRef) { l.WorkID = "" },
			wantErr: billing.ErrLedgerNoWorkID,
		},
		{
			name:    "negative consumed",
			mutate:  func(l *workgraph.BudgetLedger, _ *billing.QuittanceRef) { l.Consumed = -1 },
			wantErr: billing.ErrNegativeConsumed,
		},
		{
			name:    "negative reserved",
			mutate:  func(l *workgraph.BudgetLedger, _ *billing.QuittanceRef) { l.Reserved = -0.5 },
			wantErr: billing.ErrNegativeReserved,
		},
		{
			name:    "negative ceiling",
			mutate:  func(l *workgraph.BudgetLedger, _ *billing.QuittanceRef) { l.Ceiling.ComputeEUR = -10 },
			wantErr: billing.ErrNegativeCeiling,
		},
		{
			name: "negative late bill",
			mutate: func(l *workgraph.BudgetLedger, _ *billing.QuittanceRef) {
				l.LateBillEntries = []workgraph.LateBill{{AmountEUR: -2, Reason: "credit"}}
			},
			wantErr: billing.ErrNegativeLateBill,
		},
		{
			name: "nil quittance",
			mutate: func(_ *workgraph.BudgetLedger, q *billing.QuittanceRef) {
				*q = billing.QuittanceRef{}
				q.QuittanceIDF = ""
			},
			wantErr: billing.ErrQuittanceRequired,
		},
		{
			name:    "quittance work_id mismatch",
			mutate:  func(_ *workgraph.BudgetLedger, q *billing.QuittanceRef) { q.WorkIDF = "work_other" },
			wantErr: billing.ErrQuittanceWorkID,
		},
		{
			name:    "idempotency not sha256 hex",
			mutate:  func(_ *workgraph.BudgetLedger, q *billing.QuittanceRef) { q.IdempotencyHex = "not-a-hash" },
			wantErr: billing.ErrQuittanceIdempotency,
		},
		{
			name:    "idempotency wrong length",
			mutate:  func(_ *workgraph.BudgetLedger, q *billing.QuittanceRef) { q.IdempotencyHex = "abc123" },
			wantErr: billing.ErrQuittanceIdempotency,
		},
		{
			name: "idempotency uppercase hex rejected",
			mutate: func(_ *workgraph.BudgetLedger, q *billing.QuittanceRef) {
				q.IdempotencyHex = strings.ToUpper(validIDem)
			},
			wantErr: billing.ErrQuittanceIdempotency,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := ledger()
			q := quittance()
			if tt.name == "nil ledger" {
				var nilLedger *workgraph.BudgetLedger
				_, err := billing.Settle(nilLedger, q)
				if err != tt.wantErr {
					t.Fatalf("want err %v, got %v", tt.wantErr, err)
				}
				return
			}
			tt.mutate(l, q)
			// nil-quittance case needs an actual nil interface:
			if tt.name == "nil quittance" {
				_, err := billing.Settle(l, nil)
				if err != tt.wantErr {
					t.Fatalf("want err %v, got %v", tt.wantErr, err)
				}
				return
			}
			_, err := billing.Settle(l, q)
			if err != tt.wantErr {
				t.Fatalf("want err %v, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestSettleDoesNotMutateLedger — settlement is a pure read (freeze law: the
// ledger's clamp and clock semantics belong to the kernel, not to billing).
func TestSettleDoesNotMutateLedger(t *testing.T) {
	l := ledger()
	l.LateBillEntries = []workgraph.LateBill{{AmountEUR: 1.0, Reason: "overage"}}
	l.ClockState = billing.ClockPausedWaitHuman
	before := *l
	beforeSlices := make([]workgraph.LateBill, len(l.LateBillEntries))
	copy(beforeSlices, l.LateBillEntries)

	s, err := billing.Settle(l, quittance())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if l.WorkID != before.WorkID || l.Consumed != before.Consumed ||
		l.Reserved != before.Reserved || l.ClockState != before.ClockState ||
		l.HardStop != before.HardStop || l.Ceiling != before.Ceiling {
		t.Fatalf("ledger mutated by Settle")
	}
	for i := range beforeSlices {
		if l.LateBillEntries[i] != beforeSlices[i] {
			t.Fatalf("late bills mutated by Settle")
		}
	}
	if s.Consumed != before.Consumed || s.ClockState != before.ClockState {
		t.Fatalf("settlement does not mirror ledger state")
	}
}

// TestSettlePausedClockNeverMetersAndStillSettles — PAUSED_WAITING_HUMAN
// settles lawfully and Consume during pause never meters (freeze law from
// ADR-0009 fair billing).
func TestSettlePausedClockNeverMetersAndStillSettles(t *testing.T) {
	l := ledger()
	l.ClockState = billing.ClockRunning
	l.PauseClock()
	l.Consume(5.0) // must be a no-op while paused
	s, err := billing.Settle(l, quittance())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if s.ClockState != billing.ClockPausedWaitHuman {
		t.Fatalf("want PAUSED_WAITING_HUMAN, got %q", s.ClockState)
	}
	if s.Consumed != 5.0 {
		t.Fatalf("paused clock must never meter: want 5.0, got %.2f", s.Consumed)
	}
}

// TestSettleQuittanceBinding — settlement records the quittance binding.
func TestSettleQuittanceBinding(t *testing.T) {
	l := ledger()
	q := quittance()
	s, err := billing.Settle(l, q)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if s.QuittanceID != "quit_001" {
		t.Fatalf("quittance_id: want quit_001, got %q", s.QuittanceID)
	}
	if s.Verification != "passed" {
		t.Fatalf("verification: want passed, got %q", s.Verification)
	}
	if s.Idempotency != validIDem {
		t.Fatalf("idempotency: want %s, got %q", validIDem, s.Idempotency)
	}
	if s.WorkID != l.WorkID {
		t.Fatalf("work_id: want %q, got %q", l.WorkID, s.WorkID)
	}
}
