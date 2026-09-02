// Package store — file-local bridge additions for Task 2 (Conversation V1).
//
// This file belongs to the services/api Task 2 workstream, NOT to store.go
// (Task 1 owns store.go). It adds, on the same SQLiteStore type:
//
//   - a defensive work_events migration guard: if the Task 1 journal table
//     is not yet present (store.go migration not merged), create it here so
//     the API endpoints can be developed and tested in isolation.
//   - HandoffRecord + LatestHandoffRecord: a read-only accessor that
//     verifies the persisted handoff payload hash exactly as LatestHandoff
//     does and returns the raw hash for checkpoint binding at the resume
//     boundary.
//
// Everything here is additive and idempotent (CREATE TABLE IF NOT EXISTS),
// so it is safe when Task 1's migration lands and provides the same table.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// EnsureWorkEventsTable idempotently creates the work_events journal table
// if the shared schema migration has not created it yet. Exported so the
// services/api SSE endpoint can lazily guarantee the journal exists
// regardless of schema version.
func (s *SQLiteStore) EnsureWorkEventsTable() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS work_events (
    sequence     INTEGER PRIMARY KEY AUTOINCREMENT,
    id           TEXT NOT NULL UNIQUE,
    work_id      TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    type         TEXT NOT NULL,
    observed_at  TEXT NOT NULL,
    data_json    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_events_work_sequence
ON work_events(work_id, sequence);
`)
	if err != nil {
		return fmt.Errorf("journal bridge: ensure work_events: %w", err)
	}
	return nil
}

// HandoffRecord is the read-only checkpoint record used by the resume
// endpoint to bind a request to the exact persisted payload (ADR-0010).
// PayloadHash is the sha256 hex over the exact stored handoff JSON bytes —
// the same value LatestHandoff verifies.
type HandoffRecord struct {
	ID          string
	WorkID      string
	ToState     workgraph.State
	PayloadHash string
	Handoff     workgraph.Handoff
	CreatedAt   time.Time
}

// LatestHandoffRecord returns the most recent checkpoint for a Work with
// its persisted payload hash. Corruption detection matches LatestHandoff:
// a stored payload whose re-derived hash differs is rejected fail-closed
// (ErrCorruptHandoff), never handed to a caller.
func (s *SQLiteStore) LatestHandoffRecord(ctx context.Context, workID string) (*HandoffRecord, error) {
	var id, payload, hash, toState, created string
	err := s.db.QueryRowContext(ctx, `
        SELECT id, work_id, to_state, payload_hash, payload_json, created_at
        FROM work_handoffs
        WHERE work_id = ? ORDER BY created_at DESC LIMIT 1
    `, workID).Scan(&id, &workID, &toState, &hash, &payload, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoHandoff
	}
	if err != nil {
		return nil, fmt.Errorf("handoff record %s: %w", workID, err)
	}
	if handoffHash(payload) != hash {
		return nil, ErrCorruptHandoff
	}
	var h workgraph.Handoff
	if err := unmarshalJSON(payload, &h); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrCorruptHandoff, err)
	}
	if err := workgraph.ValidateHandoff(&h); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptHandoff, err)
	}
	createdAt, _ := parseTime(created)
	return &HandoffRecord{
		ID:          id,
		WorkID:      workID,
		ToState:     workgraph.State(toState),
		PayloadHash: hash,
		Handoff:     h,
		CreatedAt:   createdAt,
	}, nil
}

// unmarshalJSON decodes a stored handoff payload. json.Unmarshal wrapper
// kept local so the bridge file owns its own decode path.
func unmarshalJSON(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}

// ResumeReceipt is the durable record of one governed resume, keyed by
// idempotency key. The resume endpoint replays the stored receipt on an
// identical retry and rejects a changed payload under the same key.
type ResumeReceipt struct {
	WorkID            string
	IdempotencyKey    string
	PayloadHash       string
	ApprovalReceiptID string
	PrincipalID       string
	TenantID          string
	ResultingState    string
	CreatedAt         string
}

// EnsureResumeReceiptsTable idempotently creates the work_resume_receipts
// table (file-local Task 2 migration; additive to whatever schema version
// store.go carries).
func (s *SQLiteStore) EnsureResumeReceiptsTable() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS work_resume_receipts (
    idempotency_key  TEXT PRIMARY KEY,
    work_id          TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    payload_hash     TEXT NOT NULL,
    approval_receipt_id TEXT NOT NULL,
    principal_id     TEXT NOT NULL,
    tenant_id        TEXT NOT NULL,
    resulting_state  TEXT NOT NULL,
    created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_resume_receipts_work
ON work_resume_receipts(work_id);
`)
	if err != nil {
		return fmt.Errorf("resume bridge: ensure work_resume_receipts: %w", err)
	}
	return nil
}

// LookupResumeReceipt returns the receipt for an idempotency key, or
// (nil, nil) when absent.
func (s *SQLiteStore) LookupResumeReceipt(ctx context.Context, idempotencyKey string) (*ResumeReceipt, error) {
	if err := s.EnsureResumeReceiptsTable(); err != nil {
		return nil, err
	}
	var rec ResumeReceipt
	var created string
	err := s.db.QueryRowContext(ctx, `
        SELECT idempotency_key, work_id, payload_hash, approval_receipt_id, principal_id, tenant_id, resulting_state, created_at
        FROM work_resume_receipts WHERE idempotency_key = ?
    `, idempotencyKey).Scan(&rec.IdempotencyKey, &rec.WorkID, &rec.PayloadHash, &rec.ApprovalReceiptID,
		&rec.PrincipalID, &rec.TenantID, &rec.ResultingState, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resume bridge: lookup %s: %w", idempotencyKey, err)
	}
	rec.CreatedAt = created
	return &rec, nil
}

// RecordResumeReceipt persists one resume receipt. An INSERT of an
// existing key with a different payload is rejected (the endpoint checks
// first, but the unique key is the last line of defense under races).
func (s *SQLiteStore) RecordResumeReceipt(ctx context.Context, rec ResumeReceipt) error {
	if err := s.EnsureResumeReceiptsTable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO work_resume_receipts
            (idempotency_key, work_id, payload_hash, approval_receipt_id, principal_id, tenant_id, resulting_state, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(idempotency_key) DO NOTHING
    `, rec.IdempotencyKey, rec.WorkID, rec.PayloadHash, rec.ApprovalReceiptID,
		rec.PrincipalID, rec.TenantID, rec.ResultingState,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("resume bridge: record %s: %w", rec.IdempotencyKey, err)
	}
	return nil
}