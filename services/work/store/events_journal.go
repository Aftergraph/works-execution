package store

// Task 1 (docs/superpowers/plans/2026-09-01-works-conversation-v1.md):
// journal-owned mutation wrappers.
//
// Each wrapper runs the canonical mutation (existing transaction logic,
// untouched), and — only after that transaction has succeeded — appends
// the corresponding durable journal record. Journal emission failures are
// returned to the caller (joined with ErrEventEmission): V1 claims
// resumable events, so a dropped journal row must never look like success.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// CreateWorkEventful is the journal-owned wrapper around CreateWork used
// by V1 mutation paths. It emits work.created after the Work is durable.
func (s *SQLiteStore) CreateWorkEventful(ctx context.Context, w *workgraph.Work) error {
	if err := s.CreateWork(ctx, w); err != nil {
		return err
	}
	return s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: w.ID,
		Type:   EventWorkCreated,
		Data: map[string]any{
			"work_id": w.ID,
			"state":   string(w.State),
		},
	})
}

// UpdateStateEventful wraps UpdateState and emits work.state.changed with
// the from/to states after the canonical transition succeeds.
func (s *SQLiteStore) UpdateStateEventful(ctx context.Context, id string, to workgraph.State) (*workgraph.Work, error) {
	prev, err := s.GetWork(ctx, id)
	if err != nil {
		return nil, err
	}
	from := string(prev.State)
	w, err := s.UpdateState(ctx, id, to)
	if err != nil {
		return nil, err
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: id,
		Type:   EventWorkStateChanged,
		Data: map[string]any{
			"work_id": id,
			"state":   string(to),
			"from":    from,
		},
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// AppendAttemptEventful wraps AppendAttempt and emits
// activity.attempt.changed after the attempt row is durable.
func (s *SQLiteStore) AppendAttemptEventful(ctx context.Context, workID string, a workgraph.Attempt) (*workgraph.Work, error) {
	w, err := s.AppendAttempt(ctx, workID, a)
	if err != nil {
		return nil, err
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: workID,
		Type:   EventActivityAttempt,
		Data: map[string]any{
			"work_id":   workID,
			"attempt_id": a.ID,
			"node_id":   a.NodeID,
			"status":    a.Status,
		},
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// AppendEvidenceEventful wraps AppendEvidence and emits evidence.recorded.
func (s *SQLiteStore) AppendEvidenceEventful(ctx context.Context, workID string, e workgraph.Evidence) (*workgraph.Work, error) {
	w, err := s.AppendEvidence(ctx, workID, e)
	if err != nil {
		return nil, err
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: workID,
		Type:   EventEvidenceRecorded,
		Data: map[string]any{
			"work_id":    workID,
			"evidence_id": e.ID,
			"node_id":    e.NodeID,
			"type":       e.Type,
		},
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// AppendArtifactEventful wraps AppendArtifact and emits artifact.created.
func (s *SQLiteStore) AppendArtifactEventful(ctx context.Context, workID string, art workgraph.Artifact) (*workgraph.Work, error) {
	w, err := s.AppendArtifact(ctx, workID, art)
	if err != nil {
		return nil, err
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: workID,
		Type:   EventArtifactCreated,
		Data: map[string]any{
			"work_id":     workID,
			"artifact_id": art.ID,
			"node_id":     art.NodeID,
			"path":        art.Path,
		},
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// GrantLeaseEventful wraps GrantLease and emits worker.lease.granted.
func (s *SQLiteStore) GrantLeaseEventful(ctx context.Context, workID, nodeID, workerID string, ttl time.Duration) (*workgraph.Lease, *workgraph.Attempt, error) {
	lease, attempt, err := s.GrantLease(ctx, workID, nodeID, workerID, ttl)
	if err != nil {
		return nil, nil, err
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: workID,
		Type:   EventWorkerLeaseGranted,
		Data: map[string]any{
			"work_id":   workID,
			"lease_id":  lease.ID,
			"attempt_id": attempt.ID,
			"node_id":   nodeID,
			"worker_id": workerID,
		},
	}); err != nil {
		return nil, nil, err
	}
	return lease, attempt, nil
}

// RenewLeaseEventful wraps RenewLease and emits worker.lease.renewed.
func (s *SQLiteStore) RenewLeaseEventful(ctx context.Context, leaseID string, ttl time.Duration) (*workgraph.Lease, error) {
	lease, err := s.RenewLease(ctx, leaseID, ttl)
	if err != nil {
		return nil, err
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: lease.WorkID,
		Type:   EventWorkerLeaseRenewed,
		Data: map[string]any{
			"work_id":  lease.WorkID,
			"lease_id": lease.ID,
			"expires_at": lease.ExpiresAt.UTC().Format(time.RFC3339Nano),
		},
	}); err != nil {
		return nil, err
	}
	return lease, nil
}

// ReleaseLeaseEventful wraps ReleaseLease and emits worker.lease.released.
func (s *SQLiteStore) ReleaseLeaseEventful(ctx context.Context, leaseID, reason string) error {
	return s.transitionLeaseWithEvent(ctx, leaseID, EventWorkerLeaseRelease, s.ReleaseLease, reason)
}

// RevokeLeaseEventful wraps RevokeLease and emits worker.lease.revoked.
func (s *SQLiteStore) RevokeLeaseEventful(ctx context.Context, leaseID, reason string) error {
	return s.transitionLeaseWithEvent(ctx, leaseID, EventWorkerLeaseRevoke, s.RevokeLease, reason)
}

// transitionLeaseWithEvent runs the canonical lease transition and, only
// after it succeeds, emits the journal event. The lease row is kept after
// release/revoke (only its status changes), so GetLease resolves the Work
// ID for the journal record.
func (s *SQLiteStore) transitionLeaseWithEvent(ctx context.Context, leaseID, eventType string, transitionFn func(context.Context, string, string) error, reason string) error {
	if err := transitionFn(ctx, leaseID, reason); err != nil {
		return err
	}
	lease, err := s.GetLease(ctx, leaseID)
	if err != nil {
		return fmt.Errorf("%w: %s: resolve lease: %v", ErrEventEmission, eventType, err)
	}
	return s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: lease.WorkID,
		Type:   eventType,
		Data: map[string]any{
			"work_id":  lease.WorkID,
			"lease_id": lease.ID,
			"reason":   reason,
		},
	})
}

// CompleteLeaseEventful wraps CompleteLease and emits worker.lease.completed.
func (s *SQLiteStore) CompleteLeaseEventful(ctx context.Context, leaseID string, exitCode int, artifact *workgraph.Artifact, evidence []workgraph.Evidence) (*workgraph.Work, error) {
	w, err := s.CompleteLease(ctx, leaseID, exitCode, artifact, evidence)
	if err != nil {
		return nil, err
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: w.ID,
		Type:   EventWorkerLeaseDone,
		Data: map[string]any{
			"work_id":   w.ID,
			"lease_id":  leaseID,
			"exit_code": exitCode,
			"state":     string(w.State),
		},
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// SuspendWorkEventful wraps SuspendWork and emits work.waiting_human or
// work.suspended depending on the state the Work actually transitioned to.
func (s *SQLiteStore) SuspendWorkEventful(ctx context.Context, id string, to workgraph.State, h *workgraph.Handoff) (*workgraph.Work, error) {
	w, err := s.SuspendWork(ctx, id, to, h)
	if err != nil {
		return nil, err
	}
	eventType := EventWorkSuspended
	switch to {
	case workgraph.StateWaitingHuman:
		eventType = EventWorkWaitingHuman
	case workgraph.StateSuspended:
		eventType = EventWorkSuspended
	default:
		// SuspendWork only accepts these two states; anything else was
		// rejected by the canonical call above.
		eventType = EventWorkStateChanged
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: id,
		Type:   eventType,
		Data: map[string]any{
			"work_id": id,
			"state":   string(to),
		},
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// ResumeFromCheckpointEventful wraps ResumeFromCheckpoint and emits
// work.resumed after the Work is back in RUNNING.
func (s *SQLiteStore) ResumeFromCheckpointEventful(ctx context.Context, id string) (*workgraph.Work, *workgraph.Handoff, error) {
	w, h, err := s.ResumeFromCheckpoint(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if err := s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: id,
		Type:   EventWorkResumed,
		Data: map[string]any{
			"work_id": id,
			"state":   string(w.State),
		},
	}); err != nil {
		return nil, nil, err
	}
	return w, h, nil
}

// small helper so wrappers can build JSON payloads through mustJSON while
// keeping the raw map form for future richer payloads.
func journalPayload(v map[string]any) json.RawMessage {
	return json.RawMessage(mustJSON(v))
}

var _ = fmt.Sprintf // keep fmt import if unused by future edits