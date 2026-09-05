package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/classifier"
)

// ErrLeaseConflict is returned when a lease cannot be granted because the
// node already has an active lease from another worker.
var ErrLeaseConflict = errors.New("node already leased")

// ErrLeaseNotActive is returned when an operation requires an ACTIVE lease
// but the lease is in a terminal state.
var ErrLeaseNotActive = errors.New("lease not active")

// GrantLease atomically:
//  1. Validates the work exists and the node is in a non-terminal state.
//  2. Checks no ACTIVE lease exists for the (work_id, node_id) pair.
//  3. Creates a new attempt row with status=running.
//  4. Creates the lease row.
//  5. Transitions the work to RUNNING if it was still QUEUED.
//
// Returns the lease and the attempt (with the same attempt_id).
func (s *SQLiteStore) GrantLease(ctx context.Context, workID, nodeID, workerID string, ttl time.Duration) (*workgraph.Lease, *workgraph.Attempt, error) {
	if ttl <= 0 {
		ttl = 25 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	// Verify work exists.
	var stateStr string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM works WHERE id = ?`, workID).Scan(&stateStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	state := workgraph.State(stateStr)
	if state.IsTerminal() {
		return nil, nil, fmt.Errorf("work is in terminal state %s", state)
	}
	// k-mission-02 authority law (ADR-0009/0010 freeze invariant): a paused
	// mission (WAITING_HUMAN / SUSPENDED / BUDGET_EXHAUSTED) may never lease
	// nodes — leases are the runtime's path to execution, and granting one
	// would be an indirect agent-self-resume. Resume goes ONLY through
	// ResumeFromCheckpoint after kernel-authorized budget/human approval.
	switch state {
	case workgraph.StateWaitingHuman, workgraph.StateSuspended, workgraph.StateBudgetExhausted:
		return nil, nil, fmt.Errorf("%w: paused mission %s (%s) cannot lease; resume via kernel authorization only",
			ErrLeaseConflict, workID, state)
	}

	// Check for existing active lease on this node.
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

	// Create attempt with status=running.
	attemptID := workgraph.NewID("att")
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO work_attempts (id, work_id, node_id, worker_id, started_at, status, exit_code)
        VALUES (?, ?, ?, ?, ?, 'running', 0)
    `, attemptID, workID, nodeID, workerID, now.Format(time.RFC3339Nano)); err != nil {
		return nil, nil, fmt.Errorf("insert attempt: %w", err)
	}

	// Create lease.
	leaseID := workgraph.NewID("lse")
	expiresAt := now.Add(ttl)
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO work_leases (id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, leaseID, workID, nodeID, workerID, attemptID,
		now.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		string(workgraph.LeaseActive)); err != nil {
		return nil, nil, fmt.Errorf("insert lease: %w", err)
	}

	// Link attempt to lease.
	if _, err := tx.ExecContext(ctx, `UPDATE work_attempts SET lease_id = ? WHERE id = ?`, leaseID, attemptID); err != nil {
		return nil, nil, err
	}

	// Transition work to RUNNING if QUEUED.
	if state == workgraph.StateQueued {
		if !workgraph.CanTransition(state, workgraph.StateRunning) {
			return nil, nil, fmt.Errorf("invalid transition %s -> RUNNING", state)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE works SET state = ?, updated_at = ? WHERE id = ?`,
			string(workgraph.StateRunning), now.Format(time.RFC3339Nano), workID); err != nil {
			return nil, nil, err
		}
	}
	// Always bump updated_at.
	if _, err := tx.ExecContext(ctx, `UPDATE works SET updated_at = ? WHERE id = ?`,
		now.Format(time.RFC3339Nano), workID); err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}

	// Live timeline (Conversation V1 mirror): every canonical state
	// transition must appear in the durable journal — the AVC conversation
	// worker mirrors work.state.changed into the live execution timeline.
	// Emission happens AFTER commit and is best-effort: a journal row that
	// fails to append must never roll the lease back (the work IS running);
	// the mirror converges on the next poll via the cursor.
	if state == workgraph.StateQueued {
		_ = s.journalWorkEvent(ctx, journalEvent{
			ID:     workgraph.NewID("evt"),
			WorkID: workID,
			Type:   EventWorkStateChanged,
			Data: map[string]any{
				"work_id": workID,
				"state":   string(workgraph.StateRunning),
				"from":    string(workgraph.StateQueued),
			},
		})
	}

	return &workgraph.Lease{
		ID:         leaseID,
		WorkID:     workID,
		NodeID:     nodeID,
		WorkerID:   workerID,
		AttemptID:  attemptID,
		GrantedAt:  now,
		ExpiresAt:  expiresAt,
		LastBeatAt: now,
		Status:     workgraph.LeaseActive,
	}, &workgraph.Attempt{
		ID:        attemptID,
		NodeID:    nodeID,
		WorkerID:  workerID,
		StartedAt: now,
		Status:    "running",
	}, nil
}

// RenewLease extends ExpiresAt by ttl if the lease is ACTIVE. Returns
// ErrLeaseNotActive if the lease is in a terminal state.
func (s *SQLiteStore) RenewLease(ctx context.Context, leaseID string, ttl time.Duration) (*workgraph.Lease, error) {
	if ttl <= 0 {
		ttl = 25 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var statusStr string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM work_leases WHERE id = ?`, leaseID).Scan(&statusStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if workgraph.LeaseStatus(statusStr) != workgraph.LeaseActive {
		return nil, ErrLeaseNotActive
	}

	now := time.Now().UTC()
	newExpires := now.Add(ttl)
	if _, err := tx.ExecContext(ctx, `
        UPDATE work_leases SET expires_at = ?, last_beat_at = ? WHERE id = ? AND status = ?
    `, newExpires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), leaseID, string(workgraph.LeaseActive)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetLease(ctx, leaseID)
}

// CompleteLease marks the lease RELEASED and finalizes the underlying
// attempt with the given exit code. If exit code is 0 the attempt is
// 'succeeded', otherwise 'failed'. Also persists any artifact + evidence
// rows the worker reported.
//
// After committing the attempt, this method also calls
// MaybeFinalizeWork — if all nodes in the work have a successful attempt
// and the work is RUNNING, it transitions to VERIFYING then SUCCEEDED.
func (s *SQLiteStore) CompleteLease(ctx context.Context, leaseID string, exitCode int, artifact *workgraph.Artifact, evidence []workgraph.Evidence) (*workgraph.Work, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var workID, attemptID, statusStr string
	if err := tx.QueryRowContext(ctx, `SELECT work_id, attempt_id, status FROM work_leases WHERE id = ?`, leaseID).Scan(&workID, &attemptID, &statusStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if workgraph.LeaseStatus(statusStr) != workgraph.LeaseActive {
		return nil, ErrLeaseNotActive
	}

	now := time.Now().UTC()
	status := "succeeded"
	if exitCode != 0 {
		status = "failed"
	}

	// Transition lease -> RELEASED.
	if _, err := tx.ExecContext(ctx, `
        UPDATE work_leases SET status = ?, last_beat_at = ? WHERE id = ? AND status = ?
    `, string(workgraph.LeaseReleased), now.Format(time.RFC3339Nano), leaseID, string(workgraph.LeaseActive)); err != nil {
		return nil, err
	}
	// Finalize the attempt.
	if _, err := tx.ExecContext(ctx, `
        UPDATE work_attempts SET status = ?, exit_code = ?, finished_at = ? WHERE id = ?
    `, status, exitCode, now.Format(time.RFC3339Nano), attemptID); err != nil {
		return nil, err
	}
	_, _ = tx.ExecContext(ctx, `UPDATE works SET updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), workID)

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if artifact != nil {
		if _, err := s.AppendArtifact(ctx, workID, *artifact); err != nil {
			return nil, err
		}
	}
	for _, e := range evidence {
		if _, err := s.AppendEvidence(ctx, workID, e); err != nil {
			return nil, err
		}
	}

	// If a node failed, the work is FAILED. If all nodes succeeded, finalize
	// to VERIFYING -> SUCCEEDED.
	w, err := s.GetWork(ctx, workID)
	if err != nil {
		return nil, err
	}

	// Self-Healing (k-impl-007): classify every failed attempt in the just-
	// completed work. Each classification is persisted as an evidence row
	// of type "policy" so downstream consumers (Self-Healing scheduler,
	// standards-validate, evidence bundle) can read it without a schema
	// change. The attempt's worker-reported Error string is used as the
	// logTail fallback; richer log parsing is a slice-5 concern.
	s.classifyFailedAttempts(ctx, w)

	allOK := true
	anyFailed := false
	for nodeID := range w.Graph.Nodes {
		nodeOK := false
		for _, a := range w.Attempts {
			if a.NodeID != nodeID {
				continue
			}
			if a.Status == "succeeded" {
				nodeOK = true
			}
			if a.Status == "failed" {
				anyFailed = true
			}
		}
		if !nodeOK {
			allOK = false
		}
	}
	switch {
	case anyFailed:
		if _, err := s.UpdateState(ctx, workID, workgraph.StateFailed); err != nil {
			s.logFmt("complete: transition to FAILED: %v", err)
		}
	case allOK:
		if w.State == workgraph.StateRunning {
			// Live timeline (Conversation V1 mirror): terminal transitions
			// are journaled so the AVC worker can mirror work.state.changed.
			if _, err := s.UpdateStateEventful(ctx, workID, workgraph.StateVerifying); err != nil {
				s.logFmt("complete: transition to VERIFYING: %v", err)
			}
			if _, err := s.UpdateStateEventful(ctx, workID, workgraph.StateSucceeded); err != nil {
				s.logFmt("complete: transition to SUCCEEDED: %v", err)
			}
		}
	}
	return s.GetWork(ctx, workID)
}

// logFmt is a tiny helper to surface errors via the package's default logger
// without forcing every caller to plumb a logger.
func (s *SQLiteStore) logFmt(format string, args ...any) {
	// No-op stub; can be replaced with a real logger later.
	_ = format
	_ = args
}

// classifyFailedAttempts runs the Self-Healing Failure Classifier
// (services/classifier) over every failed attempt in `w` and persists the
// resulting Classification as an evidence row. Best-effort: any per-attempt
// error is logged via logFmt but does not abort CompleteLease, because the
// scheduler has already received the work's terminal state.
//
// This function is called from CompleteLease finalization. It runs AFTER
// the attempt row has been written and the lease has been transitioned
// to RELEASED, so there is no transactional coupling between
// classification and the state machine.
func (s *SQLiteStore) classifyFailedAttempts(ctx context.Context, w *workgraph.Work) {
	if w == nil {
		return
	}
	for _, a := range w.Attempts {
		if !classifier.IsFailed(a) {
			continue
		}
		// Skip attempts we've already classified. Evidence rows are
		// append-only; AppendEvidence would create duplicates.
		if s.hasClassificationEvidence(ctx, w.ID, a.ID) {
			continue
		}
		node := w.Graph.Nodes[a.NodeID]
		cls, err := classifier.Classify(ctx, node, a, a.Error)
		if err != nil {
			// logTail empty + no rule fired: record a minimal
			// "unknown" classification rather than aborting.
			cls = &classifier.Classification{
				Class:      classifier.ClassUnknown,
				Retryable:  false,
				MaxRetries: 0,
				Backoff:    "none",
				Rule:       "no_input",
			}
		}
		details := map[string]any{
			"class":                  string(cls.Class),
			"retryable":              cls.Retryable,
			"max_retries":            cls.MaxRetries,
			"backoff":                cls.Backoff,
			"human_required":         cls.HumanRequired,
			"autonomous_remediation": cls.AutonomousRemediation,
			"rule":                   cls.Rule,
		}
		if cls.HumanRequired {
			details["escalation_reason"] = cls.Rule
		}
		ev := workgraph.Evidence{
			ID:          workgraph.NewID("evd"),
			NodeID:      a.NodeID,
			AttemptID:   a.ID,
			Type:        "policy",
			Result:      "fail",
			RecordedAt:  time.Now().UTC(),
			Signer:      "classifier",
			Environment: "self-healing",
			Details:     details,
		}
		// G2: integrity-hash foedes med evidence
		ev.Seal()
		if _, err := s.AppendEvidence(ctx, w.ID, ev); err != nil {
			s.logFmt("classify: append evidence: %v", err)
		}
	}
}

// hasClassificationEvidence returns true if the given attempt already has
// at least one policy-type evidence row produced by the classifier. Used
// to keep classification idempotent across re-reads and reruns.
func (s *SQLiteStore) hasClassificationEvidence(ctx context.Context, workID, attemptID string) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM work_evidence
		WHERE work_id = ? AND attempt_id = ? AND type = 'policy' AND signer = 'classifier'
	`, workID, attemptID).Scan(&n)
	if err != nil {
		return false
	}
	return n > 0
}

// ReleaseLease marks the lease RELEASED and the underlying attempt
// 'cancelled'. Used when the worker voluntarily gives up the lease (e.g.
// the node command had a setup error before executing).
func (s *SQLiteStore) ReleaseLease(ctx context.Context, leaseID, reason string) error {
	return s.transitionLeaseAttempt(ctx, leaseID, workgraph.LeaseReleased, "cancelled", reason)
}

// RevokeLease marks the lease REVOKED and the underlying attempt
// 'cancelled'. Used when the work is cancelled or the lease is
// administratively revoked.
func (s *SQLiteStore) RevokeLease(ctx context.Context, leaseID, reason string) error {
	return s.transitionLeaseAttempt(ctx, leaseID, workgraph.LeaseRevoked, "cancelled", reason)
}

func (s *SQLiteStore) transitionLeaseAttempt(ctx context.Context, leaseID string, to workgraph.LeaseStatus, attemptStatus, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var workID, attemptID, statusStr string
	if err := tx.QueryRowContext(ctx, `SELECT work_id, attempt_id, status FROM work_leases WHERE id = ?`, leaseID).Scan(&workID, &attemptID, &statusStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !workgraph.ValidateLeaseTransition(workgraph.LeaseStatus(statusStr), to) {
		return fmt.Errorf("%w: %s -> %s", workgraph.ErrInvalidTransition, statusStr, to)
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
        UPDATE work_leases SET status = ?, last_beat_at = ? WHERE id = ?
    `, string(to), now.Format(time.RFC3339Nano), leaseID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
        UPDATE work_attempts SET status = ?, finished_at = ?, error = ? WHERE id = ?
    `, attemptStatus, now.Format(time.RFC3339Nano), reason, attemptID); err != nil {
		return err
	}
	_, _ = tx.ExecContext(ctx, `UPDATE works SET updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), workID)
	return tx.Commit()
}

// GetLease returns a lease by ID.
func (s *SQLiteStore) GetLease(ctx context.Context, leaseID string) (*workgraph.Lease, error) {
	var l workgraph.Lease
	var statusStr, grantedStr, expiresStr, beatStr string
	err := s.db.QueryRowContext(ctx, `
        SELECT id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status
        FROM work_leases WHERE id = ?
    `, leaseID).Scan(&l.ID, &l.WorkID, &l.NodeID, &l.WorkerID, &l.AttemptID,
		&grantedStr, &expiresStr, &beatStr, &statusStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	l.GrantedAt, _ = parseTime(grantedStr)
	l.ExpiresAt, _ = parseTime(expiresStr)
	l.LastBeatAt, _ = parseTime(beatStr)
	l.Status = workgraph.LeaseStatus(statusStr)
	return &l, nil
}

// ListExpiredLeases returns up to `limit` leases that are ACTIVE but whose
// ExpiresAt is in the past. Used by the reaper.
func (s *SQLiteStore) ListExpiredLeases(ctx context.Context, limit int) ([]*workgraph.Lease, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status
        FROM work_leases WHERE status = ? AND expires_at < ? LIMIT ?
    `, string(workgraph.LeaseActive), now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*workgraph.Lease
	for rows.Next() {
		var l workgraph.Lease
		var statusStr, grantedStr, expiresStr, beatStr string
		if err := rows.Scan(&l.ID, &l.WorkID, &l.NodeID, &l.WorkerID, &l.AttemptID,
			&grantedStr, &expiresStr, &beatStr, &statusStr); err != nil {
			return nil, err
		}
		l.GrantedAt, _ = parseTime(grantedStr)
		l.ExpiresAt, _ = parseTime(expiresStr)
		l.LastBeatAt, _ = parseTime(beatStr)
		l.Status = workgraph.LeaseStatus(statusStr)
		out = append(out, &l)
	}
	return out, rows.Err()
}

// MarkAttemptCancelled flips a 'running' attempt to 'cancelled' with a
// reason. Idempotent.
func (s *SQLiteStore) MarkAttemptCancelled(ctx context.Context, attemptID, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
        UPDATE work_attempts SET status = 'cancelled', finished_at = ?, error = ?
        WHERE id = ? AND status = 'running'
    `, now, reason, attemptID)
	return err
}

// ActiveLeasesByWorkID returns a set of node IDs that currently have an
// ACTIVE lease for the given work. Used by the scheduler to filter ready
// nodes.
func (s *SQLiteStore) ActiveLeasesByWorkID(ctx context.Context, workID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT node_id FROM work_leases WHERE work_id = ? AND status = ?
    `, workID, string(workgraph.LeaseActive))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// LeasesByWorkID returns every lease (any status) associated with the given
// Work, ordered by granted_at ascending. Used by the evidence bundle
// producer to assemble the components.leases list.
func (s *SQLiteStore) LeasesByWorkID(ctx context.Context, workID string) ([]*workgraph.Lease, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status
        FROM work_leases WHERE work_id = ? ORDER BY granted_at ASC
    `, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*workgraph.Lease
	for rows.Next() {
		var l workgraph.Lease
		var statusStr, grantedStr, expiresStr, beatStr string
		if err := rows.Scan(&l.ID, &l.WorkID, &l.NodeID, &l.WorkerID, &l.AttemptID,
			&grantedStr, &expiresStr, &beatStr, &statusStr); err != nil {
			return nil, err
		}
		l.GrantedAt, _ = parseTime(grantedStr)
		l.ExpiresAt, _ = parseTime(expiresStr)
		l.LastBeatAt, _ = parseTime(beatStr)
		l.Status = workgraph.LeaseStatus(statusStr)
		out = append(out, &l)
	}
	return out, rows.Err()
}