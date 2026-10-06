package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// GrantPlacementLease is the governed Runtime-placement mutation.
//
// Unlike the legacy worker GrantLease path, this method accepts CREATED work
// and advances CREATED -> QUEUED -> RUNNING in the same SQLite transaction as
// Attempt + WorkerLease creation. No worker can observe a QUEUED scheduling
// window before the Runtime-selected worker is fenced in.
func (s *SQLiteStore) GrantPlacementLease(
	ctx context.Context,
	workID, nodeID, workerID string,
	ttl time.Duration,
) (*workgraph.Lease, *workgraph.Attempt, error) {
	if ttl <= 0 {
		ttl = 25 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var stateStr string
	if err := tx.QueryRowContext(ctx,
		`SELECT state FROM works WHERE id = ?`, workID,
	).Scan(&stateStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	state := workgraph.State(stateStr)
	if state.IsTerminal() {
		return nil, nil, fmt.Errorf("work is in terminal state %s", state)
	}
	switch state {
	case workgraph.StateWaitingHuman, workgraph.StateSuspended, workgraph.StateBudgetExhausted:
		return nil, nil, fmt.Errorf("%w: paused mission %s (%s) cannot lease", ErrLeaseConflict, workID, state)
	case workgraph.StateCreated, workgraph.StateQueued:
		// valid below
	default:
		return nil, nil, fmt.Errorf("%w: placement reservation requires CREATED or QUEUED work, got %s", ErrLeaseConflict, state)
	}

	var existingID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM work_leases WHERE work_id = ? AND node_id = ? AND status = ?`,
		workID, nodeID, string(workgraph.LeaseActive),
	).Scan(&existingID)
	if err == nil {
		return nil, nil, ErrLeaseConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}

	now := time.Now().UTC()
	originalState := state
	if state == workgraph.StateCreated {
		if !workgraph.CanTransition(state, workgraph.StateQueued) {
			return nil, nil, fmt.Errorf("invalid transition %s -> QUEUED", state)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE works SET state = ?, updated_at = ? WHERE id = ? AND state = ?`,
			string(workgraph.StateQueued), now.Format(time.RFC3339Nano),
			workID, string(workgraph.StateCreated),
		); err != nil {
			return nil, nil, err
		}
		state = workgraph.StateQueued
	}

	if !workgraph.CanTransition(state, workgraph.StateRunning) {
		return nil, nil, fmt.Errorf("invalid transition %s -> RUNNING", state)
	}

	attemptID := workgraph.NewID("att")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_attempts (id, work_id, node_id, worker_id, started_at, status, exit_code)
		VALUES (?, ?, ?, ?, ?, 'running', 0)
	`, attemptID, workID, nodeID, workerID, now.Format(time.RFC3339Nano)); err != nil {
		return nil, nil, fmt.Errorf("insert attempt: %w", err)
	}

	leaseID := workgraph.NewID("lse")
	expiresAt := now.Add(ttl)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_leases
			(id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, leaseID, workID, nodeID, workerID, attemptID,
		now.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), string(workgraph.LeaseActive)); err != nil {
		return nil, nil, fmt.Errorf("insert lease: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE work_attempts SET lease_id = ? WHERE id = ?`, leaseID, attemptID,
	); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE works SET state = ?, updated_at = ? WHERE id = ?`,
		string(workgraph.StateRunning), now.Format(time.RFC3339Nano), workID,
	); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}

	if originalState == workgraph.StateCreated {
		_ = s.journalWorkEvent(ctx, journalEvent{
			ID: workgraph.NewID("evt"), WorkID: workID, Type: EventWorkStateChanged,
			Data: map[string]any{"work_id": workID, "from": string(workgraph.StateCreated), "state": string(workgraph.StateQueued)},
		})
	}
	_ = s.journalWorkEvent(ctx, journalEvent{
		ID: workgraph.NewID("evt"), WorkID: workID, Type: EventWorkStateChanged,
		Data: map[string]any{"work_id": workID, "from": string(workgraph.StateQueued), "state": string(workgraph.StateRunning)},
	})

	return &workgraph.Lease{
		ID: leaseID, WorkID: workID, NodeID: nodeID, WorkerID: workerID,
		AttemptID: attemptID, GrantedAt: now, ExpiresAt: expiresAt,
		LastBeatAt: now, Status: workgraph.LeaseActive,
	}, &workgraph.Attempt{
		ID: attemptID, NodeID: nodeID, WorkerID: workerID,
		StartedAt: now, Status: "running",
	}, nil
}
