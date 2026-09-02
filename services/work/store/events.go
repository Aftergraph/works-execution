package store

// Task 1 (docs/superpowers/plans/2026-09-01-works-conversation-v1.md):
// durable per-work event journal.
//
// The journal is an append-only projection of canonical Work mutations —
// a resumable event history for SSE consumers, NOT mutation authority.
// Sequence numbers are global (SQLite AUTOINCREMENT rowid), monotonic per
// append, and stable across restarts. Appends are idempotent by event ID:
// a retried AppendWorkEvent returns the originally allocated sequence
// instead of inserting a duplicate row.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Journal event types emitted by canonical mutation wrappers.
const (
	EventWorkCreated        = "work.created"
	EventWorkStateChanged   = "work.state.changed"
	EventActivityAttempt    = "activity.attempt.changed"
	EventEvidenceRecorded   = "evidence.recorded"
	EventArtifactCreated    = "artifact.created"
	EventWorkerLeaseGranted = "worker.lease.granted"
	EventWorkerLeaseRenewed = "worker.lease.renewed"
	EventWorkerLeaseRelease = "worker.lease.released"
	EventWorkerLeaseRevoke  = "worker.lease.revoked"
	EventWorkerLeaseDone    = "worker.lease.completed"
	EventWorkWaitingHuman   = "work.waiting_human"
	EventWorkSuspended      = "work.suspended"
	EventWorkResumed        = "work.resumed"
)

// WorkEvent is one durable journal row for a Work. Sequence is the
// resume cursor: consumers read events with sequence > after.
type WorkEvent struct {
	Sequence   int64           `json:"sequence"`
	ID         string          `json:"id"`
	WorkID     string          `json:"work_id"`
	Type       string          `json:"type"`
	ObservedAt time.Time       `json:"observed_at"`
	Data       json.RawMessage `json:"data"`
}

// journal-owned mutation wrapper errors.
var (
	// ErrEventEmission is returned when a mutation succeeded durably but
	// its journal record could not be written. V1 surfaces this to the
	// caller instead of silently dropping the event: a dropped journal
	// record would silently break SSE resume.
	ErrEventEmission = errors.New("work event journal: emission failed")
)

// journalEvent is the payload shape used by the mutation wrappers. Data
// is kept small: the relevant IDs/states, nothing more.
type journalEvent struct {
	ID     string
	WorkID string
	Type   string
	Data   map[string]any
}

// AppendWorkEvent durably appends one event to the journal. Idempotent by
// event ID: a duplicate ID inserts nothing and returns the original row
// (original sequence) so retries are safe. The sequence is allocated by
// SQLite (AUTOINCREMENT) and is globally monotonic.
func (s *SQLiteStore) AppendWorkEvent(ctx context.Context, event WorkEvent) (WorkEvent, error) {
	if event.ID == "" {
		return WorkEvent{}, errors.New("event.ID required")
	}
	if event.WorkID == "" {
		return WorkEvent{}, errors.New("event.WorkID required")
	}
	if event.Type == "" {
		return WorkEvent{}, errors.New("event.Type required")
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = time.Now().UTC()
	}
	data := string(event.Data)
	if data == "" {
		data = "{}"
	}
	if !json.Valid([]byte(data)) {
		return WorkEvent{}, errors.New("event.Data must be valid JSON")
	}

	// The referenced Work must exist: work_events.work_id carries a real
	// foreign key (ON DELETE CASCADE), so a bogus work_id is a caller bug
	// and must fail here, not linger as an orphan row.
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works WHERE id = ?`, event.WorkID).Scan(&exists); err != nil {
		return WorkEvent{}, fmt.Errorf("journal: check work %s: %w", event.WorkID, err)
	}
	if exists == 0 {
		return WorkEvent{}, ErrNotFound
	}

	if _, err := s.db.ExecContext(ctx, `
        INSERT OR IGNORE INTO work_events (id, work_id, type, observed_at, data_json)
        VALUES (?, ?, ?, ?, ?)
    `, event.ID, event.WorkID, event.Type, event.ObservedAt.UTC().Format(time.RFC3339Nano), data); err != nil {
		return WorkEvent{}, fmt.Errorf("journal: insert %s: %w", event.ID, err)
	}

	// Read the surviving row back by ID: identical payload for retries and
	// the authoritative answer for first inserts.
	return s.getWorkEventByID(ctx, event.ID)
}

// getWorkEventByID loads the journal row with the given event ID.
func (s *SQLiteStore) getWorkEventByID(ctx context.Context, id string) (WorkEvent, error) {
	var ev WorkEvent
	var observed, data string
	err := s.db.QueryRowContext(ctx, `
        SELECT sequence, id, work_id, type, observed_at, data_json
        FROM work_events WHERE id = ?
    `, id).Scan(&ev.Sequence, &ev.ID, &ev.WorkID, &ev.Type, &observed, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkEvent{}, fmt.Errorf("journal: event %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return WorkEvent{}, fmt.Errorf("journal: read %s: %w", id, err)
	}
	ev.ObservedAt, _ = parseTime(observed)
	ev.Data = json.RawMessage(data)
	return ev, nil
}

// ListWorkEventsAfter returns events for workID with sequence > after, in
// ascending sequence order. limit is clamped to 1..1000 (0 and negatives
// clamp to 1, values above 1000 clamp to 1000).
func (s *SQLiteStore) ListWorkEventsAfter(ctx context.Context, workID string, after int64, limit int) ([]WorkEvent, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT sequence, id, work_id, type, observed_at, data_json
        FROM work_events
        WHERE work_id = ? AND sequence > ?
        ORDER BY sequence ASC
        LIMIT ?
    `, workID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("journal: list %s after %d: %w", workID, after, err)
	}
	defer rows.Close()
	out := make([]WorkEvent, 0, limit)
	for rows.Next() {
		var ev WorkEvent
		var observed, data string
		if err := rows.Scan(&ev.Sequence, &ev.ID, &ev.WorkID, &ev.Type, &observed, &data); err != nil {
			return nil, err
		}
		ev.ObservedAt, _ = parseTime(observed)
		ev.Data = json.RawMessage(data)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// OldestWorkEventSequence returns the lowest journal sequence for a Work.
// Returns ErrNotFound if the Work itself does not exist, and (0, nil) when
// the Work exists but has no journal rows yet (a fresh consumer starts at 0).
func (s *SQLiteStore) OldestWorkEventSequence(ctx context.Context, workID string) (int64, error) {
	return s.workEventBoundary(ctx, workID, `MIN(sequence)`)
}

// LatestWorkEventSequence returns the highest journal sequence for a Work.
// Returns ErrNotFound if the Work itself does not exist, and (0, nil) when
// the Work exists but has no journal rows yet.
func (s *SQLiteStore) LatestWorkEventSequence(ctx context.Context, workID string) (int64, error) {
	return s.workEventBoundary(ctx, workID, `MAX(sequence)`)
}

func (s *SQLiteStore) workEventBoundary(ctx context.Context, workID, agg string) (int64, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works WHERE id = ?`, workID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("journal: boundary check %s: %w", workID, err)
	}
	if exists == 0 {
		return 0, ErrNotFound
	}
	var seq sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT `+agg+` FROM work_events WHERE work_id = ?`, workID,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("journal: boundary %s %s: %w", workID, agg, err)
	}
	if !seq.Valid {
		return 0, nil // Work exists, journal empty: cursor starts at 0.
	}
	return seq.Int64, nil
}

// emitJournalEvent appends one journal record AFTER the canonical
// transaction has already committed. Failure is never silently dropped:
// the wrapped mutation returns it joined with ErrEventEmission so the
// caller knows the event cursor is not trustworthy for this mutation.
func (s *SQLiteStore) emitJournalEvent(ctx context.Context, je journalEvent) error {
	data := mustJSON(je.Data)
	if _, err := s.AppendWorkEvent(ctx, WorkEvent{
		ID:         je.ID,
		WorkID:     je.WorkID,
		Type:       je.Type,
		ObservedAt: time.Now().UTC(),
		Data:       json.RawMessage(data),
	}); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrEventEmission, je.Type, err)
	}
	return nil
}

// journalWorkEvent emits a journal record for a canonical mutation and
// collapses any failure into the mutation's returned error.
func (s *SQLiteStore) journalWorkEvent(ctx context.Context, je journalEvent) error {
	return s.emitJournalEvent(ctx, je)
}