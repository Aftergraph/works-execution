package audit

// ADR-0024 evidence/event boundary wiring (k-052, event side).
//
// This file projects CloudEvent values onto the frozen obslaw kernel
// schema (packages/obslaw) as *event* records: kind=event, signed=false
// always. Events are observability, not claims -- the law refuses a
// signed event, and this wiring asserts that at the emission choke
// point so a future "signed events" refactor fails fast instead of
// silently blurring the boundary.

import (
	"errors"
	"fmt"

	"github.com/JonasAbde/works-execution/packages/obslaw"
)

// LawRecord projects e onto the ADR-0024 obslaw schema shape as an
// event record via obslaw.NewEvent -- the kernel's illegal-states-
// unrepresentable constructor (kind=event, signed=false forced,
// trimmable=true by convention).
//
// CitesHash is intentionally empty: the kernel requires a full 64-hex
// sha256 for a non-empty cites_hash, while CloudEvent IDs are
// "evt_" + 32 random hex (not a content digest at all). The empty
// string is legal on event records; the event's own ID/correlation
// fields remain the trace links. Even a 64-hex cites_hash on an event
// would be documented as informational, never a trust claim.
func LawRecord(e *CloudEvent) (*obslaw.Record, error) {
	if e == nil {
		return nil, errors.New("audit: nil event has no obslaw record")
	}
	rec, err := obslaw.NewEvent("")
	if err != nil {
		return nil, fmt.Errorf("audit: obslaw event projection: %w", err)
	}
	return rec, nil
}

// CheckEvent is the fail-fast boundary law assertion for one event:
// project it and validate the projection against the kernel.
//
// The projection reads no event fields, so CheckEvent returns nil for
// every non-nil event today and cannot change Emit's behavior for any
// current caller. Its teeth are against future drift: if someone wires
// a Signed=true flag from the CloudEvent into the projection (a
// "signed events" refactor), Validate rejects it here with
// obslaw.ErrEventCannotBeSigned and the event never reaches the audit
// table.
func CheckEvent(e *CloudEvent) error {
	rec, err := LawRecord(e)
	if err != nil {
		return err
	}
	if err := rec.Validate(); err != nil {
		return fmt.Errorf("audit: obslaw boundary law violated: %w", err)
	}
	return nil
}
