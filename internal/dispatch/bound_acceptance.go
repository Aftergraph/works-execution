package dispatch

import (
	"errors"
	"fmt"
	"strings"
)

// ErrBindingMismatch means an idempotency key is already durably bound to a
// different execution-context/dispatch subject. It is distinct from authority:
// WORKS is rejecting a conflicting durable identity, not deciding permission.
var ErrBindingMismatch = errors.New("dispatch: idempotency key already bound to different immutable execution bindings")

// BoundDispatch is the Platform V2.1 dispatch shape after WORKS has already
// materialized execution-context/1.0. AuthorityRef is derived from that
// immutable context by the API layer; there is intentionally no scalar
// authority epoch in this path.
type BoundDispatch struct {
	MissionID           string
	AuthorityLeaseID    string
	RuntimeDispatchID   string
	AttemptID           string
	EffectID            string
	IdempotencyKey      string
	BudgetRef           string
	BudgetCeiling       int64
	CheckpointID        string
	EvidenceRoot        string
	VerificationSubject string
	CausalID            string
}

func (d BoundDispatch) validate() error {
	required := []string{
		d.MissionID,
		d.AuthorityLeaseID,
		d.RuntimeDispatchID,
		d.AttemptID,
		d.EffectID,
		d.IdempotencyKey,
		d.BudgetRef,
		d.CheckpointID,
		d.EvidenceRoot,
		d.VerificationSubject,
		d.CausalID,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return ErrMissingBinding
		}
	}
	if d.BudgetCeiling < 0 {
		return ErrInvalidBudget
	}
	return nil
}

func dispatchFromBound(d BoundDispatch) Dispatch {
	return Dispatch{
		MissionID:         d.MissionID,
		AuthorityRef:      d.AuthorityLeaseID,
		AuthorityEpoch:    0, // legacy-only field; not consulted by AcceptBound
		RuntimeDispatchID: d.RuntimeDispatchID,
		AttemptID:         d.AttemptID,
		EffectID:          d.EffectID,
		IdempotencyKey:    d.IdempotencyKey,
		BudgetRef:         d.BudgetRef,
		BudgetCeiling:     d.BudgetCeiling,
		CheckpointID:      d.CheckpointID,
		EvidenceRoot:      d.EvidenceRoot,
		VerificationSubj:  d.VerificationSubject,
		CausalID:          d.CausalID,
	}
}

func matchesBound(a *Acceptance, d Dispatch, executionContextID, traceID string) bool {
	if a == nil {
		return false
	}
	got := a.Dispatch
	return got.MissionID == d.MissionID &&
		got.AuthorityRef == d.AuthorityRef &&
		got.RuntimeDispatchID == d.RuntimeDispatchID &&
		got.AttemptID == d.AttemptID &&
		got.EffectID == d.EffectID &&
		got.IdempotencyKey == d.IdempotencyKey &&
		got.BudgetRef == d.BudgetRef &&
		got.BudgetCeiling == d.BudgetCeiling &&
		got.CheckpointID == d.CheckpointID &&
		got.EvidenceRoot == d.EvidenceRoot &&
		got.VerificationSubj == d.VerificationSubj &&
		got.CausalID == d.CausalID &&
		a.ExecutionContextID == executionContextID &&
		a.TraceID == traceID
}

// AcceptBound durably binds a Runtime dispatch to an already-materialized
// WORKS execution-context/1.0.
//
// It deliberately performs NO authority decision. The context proves
// correlation to an earlier admission/AuthorityLease/WorkerLease binding, not
// that the AuthorityLease remains valid now. Consequential execution still
// requires Trust Gateway to load this context and run live AIE revalidation
// immediately before the effect.
func (a *Acceptor) AcceptBound(
	d BoundDispatch,
	executionContextID string,
	traceID string,
) (*Acceptance, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(executionContextID) == "" || strings.TrimSpace(traceID) == "" {
		return nil, ErrMissingBinding
	}

	legacy := dispatchFromBound(d)

	// Recovery first: an existing durable winner is read back, never
	// reinterpreted as a new authorization event.
	existing, err := a.store.LoadByIdempotency(d.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if !matchesBound(existing, legacy, executionContextID, traceID) {
			return nil, fmt.Errorf("%w: key %q", ErrBindingMismatch, d.IdempotencyKey)
		}
		return existing, nil
	}

	acc := &Acceptance{
		WorksExecutionID:   "wexec/" + d.IdempotencyKey,
		Dispatch:           legacy,
		AcceptedAt:         a.clock(),
		AuthorityEpochAt:   0,
		Outcome:            "ACCEPTED",
		ExecutionContextID: executionContextID,
		TraceID:            traceID,
	}
	accepted, err := a.store.AcceptIfAbsent(acc)
	if err != nil {
		return nil, err
	}
	if accepted == nil {
		return nil, errors.New("dispatch: store returned nil acceptance")
	}
	// A concurrent first-accept race may have inserted another winner after
	// our pre-read. AcceptIfAbsent is the concurrency gate; validate the
	// returned winner against every immutable binding.
	if !matchesBound(accepted, legacy, executionContextID, traceID) {
		return nil, fmt.Errorf("%w: key %q", ErrBindingMismatch, d.IdempotencyKey)
	}
	return accepted, nil
}
