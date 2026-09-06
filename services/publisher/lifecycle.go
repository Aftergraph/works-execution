package publisher

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// LifecycleState is the durable WORKS publication lifecycle. queued and
// running are intermediate states; the remaining states are terminal for one
// attempt. A retry starts a new attempt at queued.
type LifecycleState string

const (
	LifecycleQueued    LifecycleState = "queued"
	LifecycleRunning   LifecycleState = "running"
	LifecycleSuccess   LifecycleState = "success"
	LifecycleFailure   LifecycleState = "failure"
	LifecycleCancelled LifecycleState = "cancelled"
)

var lifecycleSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)

// StatusEvent is the immutable identity + state transition published for one
// WORKS attempt. Repository+SHA is the external status binding; WorkID+Attempt
// is the durable retry identity.
type StatusEvent struct {
	WorkID     string
	Repository string
	SHA        string
	Attempt    int
	State      LifecycleState
	DetailsURL string
	ObservedAt time.Time
}

// ReconcileAction describes what a status reconciler should do with an event.
type ReconcileAction string

const (
	ReconcileApply     ReconcileAction = "apply"
	ReconcileDuplicate ReconcileAction = "duplicate"
	ReconcileStale     ReconcileAction = "stale"
)

// ReconcileDecision is deterministic and side-effect free. The caller owns
// persistence and external publication after receiving Apply.
type ReconcileDecision struct {
	Action ReconcileAction
	Reason string
}

// Validate checks the trust-boundary fields before an event can be persisted or
// published. A missing timestamp is invalid rather than silently filled in.
func (e StatusEvent) Validate() error {
	if strings.TrimSpace(e.WorkID) == "" {
		return errors.New("status event: WorkID is required")
	}
	if strings.TrimSpace(e.Repository) == "" || !strings.Contains(e.Repository, "/") {
		return errors.New("status event: Repository must be owner/name")
	}
	if !lifecycleSHA.MatchString(e.SHA) {
		return errors.New("status event: SHA must be 40 lowercase hex characters")
	}
	if e.Attempt < 1 {
		return errors.New("status event: Attempt must be >= 1")
	}
	if !validLifecycleState(e.State) {
		return fmt.Errorf("status event: invalid State %q", e.State)
	}
	if e.ObservedAt.IsZero() {
		return errors.New("status event: ObservedAt is required")
	}
	return nil
}

// CanTransition reports the allowed transitions within one attempt. A retry
// across attempts is handled separately by ReconcileStatus.
func CanTransition(from, to LifecycleState) bool {
	switch from {
	case "":
		return to == LifecycleQueued
	case LifecycleQueued:
		return to == LifecycleRunning || to == LifecycleFailure || to == LifecycleCancelled
	case LifecycleRunning:
		return to == LifecycleSuccess || to == LifecycleFailure || to == LifecycleCancelled
	default:
		return false
	}
}

// GitHubStatusState maps the richer WORKS lifecycle to the GitHub Statuses API
// enum. GitHub has no queued/running/cancelled status: intermediate states are
// pending and cancelled is failure with the lifecycle retained in description.
func GitHubStatusState(state LifecycleState) Conclusion {
	switch state {
	case LifecycleSuccess:
		return ConclusionSuccess
	case LifecycleFailure, LifecycleCancelled:
		return ConclusionFailure
	case LifecycleQueued, LifecycleRunning:
		return ConclusionPending
	default:
		return ""
	}
}

// ReconcileStatus compares one candidate event with the currently accepted
// event for the same Work. It rejects stale SHAs before state comparison,
// deduplicates equal state, and only permits a new attempt to restart at
// queued. Invalid transitions return an error and must not be published.
func ReconcileStatus(current *StatusEvent, next StatusEvent, currentHead string) (ReconcileDecision, error) {
	if err := next.Validate(); err != nil {
		return ReconcileDecision{}, err
	}
	if !lifecycleSHA.MatchString(currentHead) {
		return ReconcileDecision{}, errors.New("status reconcile: current head must be 40 lowercase hex characters")
	}
	if next.SHA != currentHead {
		return ReconcileDecision{Action: ReconcileStale, Reason: "candidate SHA does not match current head"}, nil
	}
	if current == nil {
		if next.State != LifecycleQueued {
			return ReconcileDecision{}, errors.New("status reconcile: first event must be queued")
		}
		return ReconcileDecision{Action: ReconcileApply, Reason: "first event for current head"}, nil
	}
	if err := current.Validate(); err != nil {
		return ReconcileDecision{}, fmt.Errorf("status reconcile: current event: %w", err)
	}
	if current.WorkID != next.WorkID || current.Repository != next.Repository {
		return ReconcileDecision{}, errors.New("status reconcile: WorkID and Repository cannot change")
	}
	if next.Attempt < current.Attempt || next.ObservedAt.Before(current.ObservedAt) {
		return ReconcileDecision{Action: ReconcileStale, Reason: "candidate is older than accepted event"}, nil
	}
	if next.Attempt > current.Attempt {
		if next.State != LifecycleQueued {
			return ReconcileDecision{}, errors.New("status reconcile: a retry must begin at queued")
		}
		return ReconcileDecision{Action: ReconcileApply, Reason: "new retry attempt"}, nil
	}
	if next.State == current.State {
		return ReconcileDecision{Action: ReconcileDuplicate, Reason: "same attempt and state already accepted"}, nil
	}
	if !CanTransition(current.State, next.State) {
		return ReconcileDecision{}, fmt.Errorf("status reconcile: invalid transition %s -> %s", current.State, next.State)
	}
	return ReconcileDecision{Action: ReconcileApply, Reason: "valid forward transition"}, nil
}

func validLifecycleState(state LifecycleState) bool {
	switch state {
	case LifecycleQueued, LifecycleRunning, LifecycleSuccess, LifecycleFailure, LifecycleCancelled:
		return true
	default:
		return false
	}
}
