package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/JonasAbde/works-execution/packages/verifiedstate"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

var (
	// ErrReconciliationRequired means the checkpoint carries verified-state
	// truth and therefore cannot use the legacy blind-resume path, or a fresh
	// observation invalidated/staled at least one dependency.
	ErrReconciliationRequired = errors.New("resume reconciliation required")

	// ErrInvalidReconciliationCheckpoint means the reserved verified-state
	// checkpoint exists but is malformed or contradicts canonical proof.
	ErrInvalidReconciliationCheckpoint = errors.New("invalid resume reconciliation checkpoint")
)

func handoffNeedsVerifiedReconciliation(h *workgraph.Handoff) (bool, error) {
	if h == nil {
		return false, ErrNoHandoff
	}
	_, present, err := verifiedstate.CheckpointFromStateSnapshot(h.StateSnapshot)
	if err != nil {
		return true, fmt.Errorf("%w: %v", ErrInvalidReconciliationCheckpoint, err)
	}
	return present, nil
}

// ResumeFromCheckpointReconciled is the verification-aware resume path.
//
// current must come from a trusted live-world observer outside the worker/model.
// expectedCheckpointHash binds the observation to the exact persisted handoff
// the caller observed before doing external I/O. The internal resume path
// re-reads that hash immediately before the state transition, so a concurrent
// handoff change fails closed rather than applying an observation to another
// checkpoint.
//
// Any STALE or INVALIDATED impact blocks the RUNNING transition. The caller
// must selectively revalidate and create a fresh checkpoint before retrying.
func (s *SQLiteStore) ResumeFromCheckpointReconciled(
	ctx context.Context,
	id string,
	expectedCheckpointHash string,
	current verifiedstate.Snapshot,
) (*workgraph.Work, *workgraph.Handoff, verifiedstate.ReconcileResult, error) {
	var zero verifiedstate.ReconcileResult
	if expectedCheckpointHash == "" {
		return nil, nil, zero, fmt.Errorf("%w: checkpoint hash is required", ErrInvalidReconciliationCheckpoint)
	}

	rec, err := s.LatestHandoffRecord(ctx, id)
	if err != nil {
		return nil, nil, zero, err
	}
	if rec.PayloadHash != expectedCheckpointHash {
		return nil, nil, zero, fmt.Errorf("%w: expected checkpoint %s, current %s",
			ErrStaleHandoff, expectedCheckpointHash, rec.PayloadHash)
	}

	cp, present, err := verifiedstate.CheckpointFromStateSnapshot(rec.Handoff.StateSnapshot)
	if err != nil {
		return nil, nil, zero, fmt.Errorf("%w: %v", ErrInvalidReconciliationCheckpoint, err)
	}
	if !present {
		return nil, nil, zero, fmt.Errorf("%w: handoff has no %s checkpoint",
			ErrInvalidReconciliationCheckpoint, verifiedstate.CheckpointSchema)
	}

	result, err := cp.Reconcile(current)
	if err != nil {
		return nil, nil, zero, fmt.Errorf("%w: reconcile: %v", ErrInvalidReconciliationCheckpoint, err)
	}
	if len(result.Impacted) != 0 {
		return nil, nil, result, fmt.Errorf("%w: %d subject(s) stale or invalidated",
			ErrReconciliationRequired, len(result.Impacted))
	}

	w, h, err := s.resumeFromCheckpoint(ctx, id, true, expectedCheckpointHash)
	if err != nil {
		return nil, nil, result, err
	}
	return w, h, result, nil
}
