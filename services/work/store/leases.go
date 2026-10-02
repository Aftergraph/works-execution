package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// ErrLeaseFenced is returned when a caller presents a fencing triple whose
// executor identity or epoch does not match the lease row (ADR-0033).
//
// It is deliberately distinct from ErrLeaseNotActive, because the two mean
// different things and a caller must be able to act differently:
//
//   - ErrLeaseNotActive: the lease is finished (released/revoked/expired).
//     Re-granting the node is the legitimate next step.
//   - ErrLeaseFenced: the caller's token is STALE. Its lease generation no
//     longer owns this node — someone else has been granted it since, or the
//     caller never owned it. Retrying is pointless and, for a worker that
//     thinks it is still executing, a signal that its view of the world is
//     wrong. It must not retry; it must re-acquire.
//
// Wrapped errors from the fenced verbs carry the presented vs. current
// values so operators can tell a zombie worker from an identity bug:
// errors.Is(err, ErrLeaseFenced) holds in both cases.
var ErrLeaseFenced = errors.New("lease fenced: stale executor or epoch")

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
	//
	// ADR-0032 extends that law to UNKNOWN, which is not a pause but is
	// the same hazard in a sharper form: UNKNOWN means the control plane
	// lost track of a run and cannot say whether the node's work already
	// happened. Leasing it would hand the node straight back to the runtime
	// for a blind retry. UNKNOWN is resolved outward only — by a human or
	// the recovery supervisor — never by re-leasing.
	switch state {
	case workgraph.StateWaitingHuman, workgraph.StateSuspended, workgraph.StateBudgetExhausted:
		return nil, nil, fmt.Errorf("%w: paused mission %s (%s) cannot lease; resume via kernel authorization only",
			ErrLeaseConflict, workID, state)
	case workgraph.StateUnknown:
		return nil, nil, fmt.Errorf("%w: indeterminate work %s (%s) cannot lease; a lost run must be resolved by a human or the recovery supervisor, not re-run blindly",
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

	// ADR-0033: mint the fencing epoch for this grant. The epoch is
	// monotonic PER NODE — a lease on node "a" never fences a lease on
	// node "b", so unrelated nodes cannot perturb each other's tokens.
	// MAX(epoch)+1 over this node's existing lease rows is strictly
	// increasing because a re-grant can only happen once the previous row
	// has left ACTIVE (the conflict check above), so the previous row — and
	// its epoch — is already durable in this transaction.
	//
	// This read is safe against concurrent grants: it runs inside the same
	// transaction that performs the INSERT, on the single-connection
	// writer pool, so no other writer can commit a lease for this node
	// between the read and the insert.
	var epoch int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(epoch), 0) + 1 FROM work_leases WHERE work_id = ? AND node_id = ?`,
		workID, nodeID,
	).Scan(&epoch); err != nil {
		return nil, nil, fmt.Errorf("mint lease epoch: %w", err)
	}

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
        INSERT INTO work_leases (id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status, epoch)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, leaseID, workID, nodeID, workerID, attemptID,
		now.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		string(workgraph.LeaseActive), epoch); err != nil {
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
		Epoch:      epoch,
	}, &workgraph.Attempt{
		ID:        attemptID,
		NodeID:    nodeID,
		WorkerID:  workerID,
		StartedAt: now,
		Status:    "running",
	}, nil
}

// loadLeaseForFence reads the fields a fenced mutation needs and
// classifies the caller's fencing triple against them (ADR-0033).
//
// The classification order is deliberate and is a security property, not
// a convenience:
//
//  1. missing row      -> ErrNotFound
//  2. not ACTIVE       -> ErrLeaseNotActive
//  3. worker/epoch     -> ErrLeaseFenced
//
// Status is checked BEFORE the token so that a finished lease reports
// "not active" rather than "fenced". A caller must not be able to learn
// anything about the token from the shape of the error, and a lease that
// is already RELEASED has no meaningful epoch to compare against.
func loadLeaseForFence(ctx context.Context, tx *sql.Tx, ref LeaseRef) (workID, attemptID, statusStr string, err error) {
	var workerID string
	var epoch int64
	err = tx.QueryRowContext(ctx,
		`SELECT work_id, attempt_id, status, worker_id, epoch FROM work_leases WHERE id = ?`,
		ref.LeaseID).Scan(&workID, &attemptID, &statusStr, &workerID, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	if err != nil {
		return "", "", "", err
	}
	if workgraph.LeaseStatus(statusStr) != workgraph.LeaseActive {
		return "", "", "", ErrLeaseNotActive
	}
	if workerID != ref.WorkerID || epoch != ref.Epoch {
		return "", "", "", fmt.Errorf("%w: lease %s is held by worker %q at epoch %d, caller presented worker %q at epoch %d",
			ErrLeaseFenced, ref.LeaseID, workerID, epoch, ref.WorkerID, ref.Epoch)
	}
	return workID, attemptID, statusStr, nil
}

// requireCASRows enforces that a compare-and-swap UPDATE actually claimed
// the row. This is the check that makes fencing safe under concurrency,
// and it is the reason the CAS predicate — not the pre-read — is the law:
//
// Two callers holding the same, current fencing triple and racing to
// complete the same lease BOTH pass loadLeaseForFence, because at the
// instant each reads, the lease really is ACTIVE and the token really does
// match. Neither pre-read can distinguish them. What separates them is the
// UPDATE: its WHERE clause re-asserts (id, status='ACTIVE', epoch, worker_id)
// atomically, so the second writer's predicate no longer matches the row the
// first writer already moved to RELEASED, it affects zero rows, and this
// function turns that into ErrLeaseNotActive. Exactly one completion wins;
// the loser never finalizes the attempt twice and never double-publishes.
//
// Returns nil when the CAS claimed the row.
func requireCASRows(res sql.Result, ctx context.Context, tx *sql.Tx, ref LeaseRef) error {
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	// The CAS lost. Re-read inside the transaction to report WHY, rather
	// than guessing — a concurrent completion (not active) and a
	// concurrent re-grant (fenced) are different operator problems.
	var statusStr, workerID string
	var epoch int64
	err = tx.QueryRowContext(ctx,
		`SELECT status, worker_id, epoch FROM work_leases WHERE id = ?`,
		ref.LeaseID).Scan(&statusStr, &workerID, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if workgraph.LeaseStatus(statusStr) != workgraph.LeaseActive {
		return ErrLeaseNotActive
	}
	return fmt.Errorf("%w: lease %s moved to worker %q at epoch %d while the caller held worker %q at epoch %d",
		ErrLeaseFenced, ref.LeaseID, workerID, epoch, ref.WorkerID, ref.Epoch)
}

// RenewLease extends ExpiresAt by ttl if the lease is ACTIVE AND the
// caller presents the current fencing triple (ADR-0033).
//
// Returns ErrLeaseNotActive if the lease is in a terminal state, and
// ErrLeaseFenced if the epoch or executor identity is stale.
func (s *SQLiteStore) RenewLease(ctx context.Context, ref LeaseRef, ttl time.Duration) (*workgraph.Lease, error) {
	if ttl <= 0 {
		ttl = 25 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, _, _, err := loadLeaseForFence(ctx, tx, ref); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	newExpires := now.Add(ttl)
	res, err := tx.ExecContext(ctx, `
        UPDATE work_leases SET expires_at = ?, last_beat_at = ?
        WHERE id = ? AND status = ? AND epoch = ? AND worker_id = ?
    `, newExpires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		ref.LeaseID, string(workgraph.LeaseActive), ref.Epoch, ref.WorkerID)
	if err != nil {
		return nil, err
	}
	if err := requireCASRows(res, ctx, tx, ref); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetLease(ctx, ref.LeaseID)
}

// CompleteLease marks the lease RELEASED and finalizes the underlying
// attempt with the given exit code. If exit code is 0 the attempt is
// 'succeeded', otherwise 'failed'. Also persists any artifact + evidence
// rows the worker reported.
//
// The caller must present the fencing triple (ADR-0033): the completion is
// a compare-and-swap on (id, status, epoch, worker_id), so two callers
// racing to complete the same lease produce exactly one finalization. A
// stale holder — a worker that hung past its TTL and woke after the node
// was re-granted — is refused with ErrLeaseFenced and cannot overwrite the
// real holder's result.
//
// After committing the attempt, this method also calls
// MaybeFinalizeWork — if all nodes in the work have a successful attempt
// and the work is RUNNING, it transitions to VERIFYING then SUCCEEDED.
func (s *SQLiteStore) CompleteLease(ctx context.Context, ref LeaseRef, exitCode int, artifact *workgraph.Artifact, evidence []workgraph.Evidence) (*workgraph.Work, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	workID, attemptID, _, err := loadLeaseForFence(ctx, tx, ref)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	status := "succeeded"
	if exitCode != 0 {
		status = "failed"
	}

	// Transition lease -> RELEASED. The CAS predicate carries the fencing
	// triple; see requireCASRows for why the pre-read is not the law.
	res, err := tx.ExecContext(ctx, `
        UPDATE work_leases SET status = ?, last_beat_at = ?
        WHERE id = ? AND status = ? AND epoch = ? AND worker_id = ?
    `, string(workgraph.LeaseReleased), now.Format(time.RFC3339Nano),
		ref.LeaseID, string(workgraph.LeaseActive), ref.Epoch, ref.WorkerID)
	if err != nil {
		return nil, err
	}
	if err := requireCASRows(res, ctx, tx, ref); err != nil {
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
func (s *SQLiteStore) ReleaseLease(ctx context.Context, ref LeaseRef, reason string) error {
	return s.transitionLeaseAttempt(ctx, ref, workgraph.LeaseReleased, "cancelled", reason)
}

// RevokeLease marks the lease REVOKED and the underlying attempt
// 'cancelled'. Used when the work is cancelled or the lease is
// administratively revoked.
//
// Revocation is fenced exactly like release (ADR-0033). This matters most
// for the lease reaper, which reads a lease via ListExpiredLeases and then
// revokes it: if the node was re-granted between that read and the revoke,
// an unfenced revoke would cancel the NEW holder's live lease. Because
// ListExpiredLeases returns the epoch, the reaper presents the token it
// actually observed and the revocation is refused if the row moved on.
func (s *SQLiteStore) RevokeLease(ctx context.Context, ref LeaseRef, reason string) error {
	return s.transitionLeaseAttempt(ctx, ref, workgraph.LeaseRevoked, "cancelled", reason)
}

// transitionLeaseAttempt performs the release/revoke path. It is fenced
// identically to CompleteLease, with the same CAS predicate and the same
// reason the CAS — not the pre-read — is what makes it safe.
func (s *SQLiteStore) transitionLeaseAttempt(ctx context.Context, ref LeaseRef, to workgraph.LeaseStatus, attemptStatus, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	workID, attemptID, statusStr, err := loadLeaseForFence(ctx, tx, ref)
	if err != nil {
		return err
	}
	if !workgraph.ValidateLeaseTransition(workgraph.LeaseStatus(statusStr), to) {
		return fmt.Errorf("%w: %s -> %s", workgraph.ErrInvalidTransition, statusStr, to)
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
        UPDATE work_leases SET status = ?, last_beat_at = ?
        WHERE id = ? AND status = ? AND epoch = ? AND worker_id = ?
    `, string(to), now.Format(time.RFC3339Nano),
		ref.LeaseID, string(workgraph.LeaseActive), ref.Epoch, ref.WorkerID)
	if err != nil {
		return err
	}
	if err := requireCASRows(res, ctx, tx, ref); err != nil {
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
	err := s.readQueryRow(ctx, `
        SELECT id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status, epoch
        FROM work_leases WHERE id = ?
    `, leaseID).Scan(&l.ID, &l.WorkID, &l.NodeID, &l.WorkerID, &l.AttemptID,
		&grantedStr, &expiresStr, &beatStr, &statusStr, &l.Epoch)
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
//
// The epoch is returned so the reaper can present the exact token it
// observed when it revokes (ADR-0033) — see RevokeLease.
func (s *SQLiteStore) ListExpiredLeases(ctx context.Context, limit int) ([]*workgraph.Lease, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.readQuery(ctx, `
        SELECT id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status, epoch
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
			&grantedStr, &expiresStr, &beatStr, &statusStr, &l.Epoch); err != nil {
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
	rows, err := s.readQuery(ctx, `
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

// ActiveLeasesByWorkIDs returns, for each Work ID given, the set of node IDs
// that currently have an ACTIVE lease on it. IDs without a Work row are
// omitted. One query for the whole batch: the scheduler poll path calls
// this instead of ActiveLeasesByWorkID per Work.
func (s *SQLiteStore) ActiveLeasesByWorkIDs(ctx context.Context, workIDs []string) (map[string]map[string]bool, error) {
	out := make(map[string]map[string]bool, len(workIDs))
	if len(workIDs) == 0 {
		return out, nil
	}
	const chunk = 400
	for base := 0; base < len(workIDs); base += chunk {
		end := base + chunk
		if end > len(workIDs) {
			end = len(workIDs)
		}
		part := workIDs[base:end]
		ph := strings.Repeat("?,", len(part))
		ph = ph[:len(ph)-1]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.readQuery(ctx,
			`SELECT work_id, node_id FROM work_leases WHERE work_id IN (`+ph+`) AND status = ?`,
			append(args, string(workgraph.LeaseActive))...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var wid, nid string
			if err := rows.Scan(&wid, &nid); err != nil {
				rows.Close()
				return nil, err
			}
			if out[wid] == nil {
				out[wid] = map[string]bool{}
			}
			out[wid][nid] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// LeasesByWorkID returns every lease (any status) associated with the given
// Work, ordered by granted_at ascending. Used by the evidence bundle
// producer to assemble the components.leases list.
func (s *SQLiteStore) LeasesByWorkID(ctx context.Context, workID string) ([]*workgraph.Lease, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, work_id, node_id, worker_id, attempt_id, granted_at, expires_at, last_beat_at, status, epoch
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
			&grantedStr, &expiresStr, &beatStr, &statusStr, &l.Epoch); err != nil {
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