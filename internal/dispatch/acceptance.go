// Package dispatch implements the Runtime → WORKS dispatch acceptance seal
// (contract:dispatch.acceptance/1.0).
//
// Runtime dispatches; WORKS accepts durably. Acceptance is the exact seam
// where "Runtime sent" becomes "WORKS owns": at-most-once effect identity,
// authority freshness at accept time, budget ceiling binding, and a
// verification gate that never lets SUCCEEDED become VERIFIED without an
// independent verdict. Ambiguous consequential state resolves to
// INDETERMINATE, never to silent success.
package dispatch

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel failures. All are fail-closed: callers must not proceed with
// protected work when Accept or a transition returns one of these.
var (
	ErrMissingBinding      = errors.New("dispatch: missing mission/authority/dispatch/idempotency binding")
	ErrStaleAuthority      = errors.New("dispatch: authority epoch is stale")
	ErrCausalMismatch      = errors.New("dispatch: idempotency key already bound to a different causal identity")
	ErrUnknownAcceptance   = errors.New("dispatch: unknown works execution")
	ErrEffectDuplicate     = errors.New("dispatch: effect already applied")
	ErrEffectUnknown       = errors.New("dispatch: effect outcome unknown; INDETERMINATE")
	ErrBudgetExhausted     = errors.New("dispatch: budget ceiling exhausted; autonomous retry forbidden")
	ErrRevoked             = errors.New("dispatch: authority revoked mid-flight")
	ErrSelfVerification    = errors.New("dispatch: executor cannot verify itself")
	ErrStaleSubject        = errors.New("dispatch: verification subject is stale")
	ErrVerifierUnavailable = errors.New("dispatch: verifier unavailable; outcome stays UNVERIFIED")
)

// Dispatch is the Runtime-built envelope. WORKS never mints these identities;
// it only accepts and records them.
type Dispatch struct {
	MissionID         string
	AuthorityRef      string
	AuthorityEpoch    int64
	RuntimeDispatchID string
	AttemptID         string
	EffectID          string
	IdempotencyKey    string
	BudgetRef         string
	BudgetCeiling     int64
	CheckpointID      string
	EvidenceRoot      string
	VerificationSubj  string
	CausalID          string
}

// Acceptance is the durable WORKS-owned record.
type Acceptance struct {
	WorksExecutionID string
	Dispatch         Dispatch
	AcceptedAt       time.Time
	AuthorityEpochAt int64
	EffectApplied    bool
	BudgetSpent      int64
	Revoked          bool
	Outcome          string // ACCEPTED | SUCCEEDED | FAILED | INDETERMINATE
	Verified         bool
	VerifierID       string
}

// Store is the durability seam. Production uses SQLite; tests use memory.
type Store interface {
	LoadByIdempotency(key string) (*Acceptance, error)
	LoadByExecution(id string) (*Acceptance, error)
	Save(a *Acceptance) error
}

// Clock decouples expiry/freshness checks in tests.
type Clock func() time.Time

// Acceptor owns the seal. Authority / budget / verifier truth is supplied by
// callers at each transition — never cached across restarts.
type Acceptor struct {
	store Store
	clock Clock
	now   func() string
}

func NewAcceptor(store Store, clock Clock) *Acceptor {
	if clock == nil {
		clock = time.Now
	}
	return &Acceptor{store: store, clock: clock}
}

// Accept durably records a Runtime dispatch. Same idempotency key + same
// causal identity returns the existing acceptance (safe retry / duplicate
// dispatch / Runtime-died-after-accept replay). Same key + different causal
// identity fails closed. Stale authority epoch fails closed.
func (a *Acceptor) Accept(d Dispatch, currentEpoch int64) (*Acceptance, error) {
	if d.MissionID == "" || d.AuthorityRef == "" || d.RuntimeDispatchID == "" || d.IdempotencyKey == "" {
		return nil, ErrMissingBinding
	}
	if d.AuthorityEpoch < currentEpoch {
		return nil, fmt.Errorf("%w: dispatch epoch %d < current %d", ErrStaleAuthority, d.AuthorityEpoch, currentEpoch)
	}
	existing, err := a.store.LoadByIdempotency(d.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Dispatch.CausalID != d.CausalID {
			return nil, fmt.Errorf("%w: key %q", ErrCausalMismatch, d.IdempotencyKey)
		}
		if existing.Dispatch.AuthorityEpoch != d.AuthorityEpoch ||
			existing.Dispatch.MissionID != d.MissionID {
			return nil, fmt.Errorf("%w: key %q", ErrCausalMismatch, d.IdempotencyKey)
		}
		return existing, nil
	}
	acc := &Acceptance{
		WorksExecutionID: "wexec/" + d.IdempotencyKey,
		Dispatch:         d,
		AcceptedAt:       a.clock(),
		AuthorityEpochAt: d.AuthorityEpoch,
		Outcome:          "ACCEPTED",
	}
	if err := a.store.Save(acc); err != nil {
		return nil, err
	}
	return acc, nil
}

// Revoke marks authority revoked mid-flight. Revoked executions cannot apply
// effects or verify afterwards.
func (a *Acceptor) Revoke(worksExecutionID string) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	acc.Revoked = true
	return a.store.Save(acc)
}

// Spend charges budget. Over-ceiling spend fails closed and can never
// autonomously retry around the ceiling: only a new dispatched budget
// reference (new idempotency key) can continue.
func (a *Acceptor) Spend(worksExecutionID string, amount int64) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	if acc.Revoked {
		return ErrRevoked
	}
	if acc.BudgetSpent+amount > acc.Dispatch.BudgetCeiling {
		return fmt.Errorf("%w: spent %d + %d > ceiling %d", ErrBudgetExhausted, acc.BudgetSpent, amount, acc.Dispatch.BudgetCeiling)
	}
	acc.BudgetSpent += amount
	return a.store.Save(acc)
}

// ApplyEffect records an externally visible effect exactly once. A second
// apply of the same effect id is a duplicate (rejected, not re-executed).
// An effect whose outcome cannot be established resolves INDETERMINATE.
func (a *Acceptor) ApplyEffect(worksExecutionID, effectID string, outcomeKnown bool) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	if acc.Revoked {
		return ErrRevoked
	}
	if effectID != acc.Dispatch.EffectID {
		return fmt.Errorf("%w: %q is not the accepted effect", ErrCausalMismatch, effectID)
	}
	if acc.EffectApplied {
		return fmt.Errorf("%w: %q", ErrEffectDuplicate, effectID)
	}
	if !outcomeKnown {
		acc.Outcome = "INDETERMINATE"
		return a.store.Save(acc)
	}
	acc.EffectApplied = true
	return a.store.Save(acc)
}

// Complete records execution outcome. SUCCEEDED here is execution-complete
// only — Verified stays false until RecordVerdict with independent evidence.
func (a *Acceptor) Complete(worksExecutionID, outcome string) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	if acc.Revoked {
		return ErrRevoked
	}
	switch outcome {
	case "SUCCEEDED", "FAILED":
		acc.Outcome = outcome
		return a.store.Save(acc)
	default:
		return fmt.Errorf("dispatch: unknown outcome %q", outcome)
	}
}

// RecordVerdict records an independent verifier verdict over the exact
// accepted verification subject. The executor can never verify itself, a
// stale subject fails closed, and an unavailable verifier leaves the outcome
// UNVERIFIED (never auto-promotes).
func (a *Acceptor) RecordVerdict(worksExecutionID, verifierID, subject string, subjectCurrent, verifierAvailable bool) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	if verifierID == "" || verifierID == acc.Dispatch.RuntimeDispatchID {
		return ErrSelfVerification
	}
	if !subjectCurrent || subject != acc.Dispatch.VerificationSubj {
		return fmt.Errorf("%w: %q", ErrStaleSubject, subject)
	}
	if !verifierAvailable {
		return ErrVerifierUnavailable
	}
	if acc.Revoked {
		return ErrRevoked
	}
	acc.Verified = true
	acc.VerifierID = verifierID
	return a.store.Save(acc)
}

func (a *Acceptor) get(id string) (*Acceptance, error) {
	acc, err := a.store.LoadByExecution(id)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAcceptance, id)
	}
	return acc, nil
}
