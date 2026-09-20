package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrExecutionPolicyCorrelationConflict = errors.New("execution policy correlation conflict")
var ErrExecutionPolicyContextMismatch = errors.New("execution policy context work mismatch")

type ExecutionPolicyCorrelation struct {
	WorkID             string
	ExecutionContextID string
	ExecutionPDRID     string
	BindingDigest        string
	RecordedAt         time.Time
}

func (s *SQLiteStore) EnsureExecutionPolicyCorrelationsTable() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS work_execution_policy_correlations (
    execution_context_id TEXT PRIMARY KEY,
    work_id              TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    execution_pdr_id     TEXT NOT NULL,
    binding_digest         TEXT NOT NULL,
    recorded_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_execution_policy_correlations_work
ON work_execution_policy_correlations(work_id);
`)
	if err != nil {
		return fmt.Errorf("execution policy correlation: ensure table: %w", err)
	}
	return nil
}

func (s *SQLiteStore) RecordExecutionPolicyCorrelation(
	ctx context.Context,
	in ExecutionPolicyCorrelation,
) (*ExecutionPolicyCorrelation, bool, error) {
	if err := s.EnsureExecutionPolicyCorrelationsTable(); err != nil {
		return nil, false, err
	}
	if in.WorkID == "" || in.ExecutionContextID == "" || in.ExecutionPDRID == "" || in.BindingDigest == "" {
		return nil, false, errors.New("execution policy correlation: incomplete record")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("execution policy correlation: begin: %w", err)
	}
	defer tx.Rollback()

	var contextWorkID string
	err = tx.QueryRowContext(ctx,
		`SELECT work_id FROM work_execution_contexts WHERE id = ?`,
		in.ExecutionContextID,
	).Scan(&contextWorkID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, fmt.Errorf("execution policy correlation: context lookup: %w", err)
	}
	if contextWorkID != in.WorkID {
		return nil, false, ErrExecutionPolicyContextMismatch
	}

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
        INSERT INTO work_execution_policy_correlations
            (execution_context_id, work_id, execution_pdr_id, binding_digest, recorded_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(execution_context_id) DO NOTHING
    `,
		in.ExecutionContextID,
		in.WorkID,
		in.ExecutionPDRID,
		in.BindingDigest,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, false, fmt.Errorf("execution policy correlation: insert: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("execution policy correlation: rows affected: %w", err)
	}

	out, err := loadExecutionPolicyCorrelation(ctx, tx, in.ExecutionContextID)
	if err != nil {
		return nil, false, err
	}
	if out == nil {
		return nil, false, errors.New("execution policy correlation: durable row missing after insert")
	}
	if out.WorkID != in.WorkID ||
		out.ExecutionPDRID != in.ExecutionPDRID ||
		out.BindingDigest != in.BindingDigest {
		return nil, false, ErrExecutionPolicyCorrelationConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("execution policy correlation: commit: %w", err)
	}
	return out, rows == 0, nil
}

func (s *SQLiteStore) GetExecutionPolicyCorrelation(
	ctx context.Context,
	executionContextID string,
) (*ExecutionPolicyCorrelation, error) {
	if err := s.EnsureExecutionPolicyCorrelationsTable(); err != nil {
		return nil, err
	}
	return loadExecutionPolicyCorrelation(ctx, s.db, executionContextID)
}

type executionPolicyCorrelationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadExecutionPolicyCorrelation(
	ctx context.Context,
	q executionPolicyCorrelationQueryer,
	executionContextID string,
) (*ExecutionPolicyCorrelation, error) {
	var rec ExecutionPolicyCorrelation
	var recorded string
	err := q.QueryRowContext(ctx, `
        SELECT work_id, execution_context_id, execution_pdr_id, binding_digest, recorded_at
        FROM work_execution_policy_correlations
        WHERE execution_context_id = ?
    `, executionContextID).Scan(
		&rec.WorkID,
		&rec.ExecutionContextID,
		&rec.ExecutionPDRID,
		&rec.BindingDigest,
		&recorded,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("execution policy correlation: read: %w", err)
	}
	rec.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	if err != nil {
		return nil, fmt.Errorf("execution policy correlation: invalid recorded_at: %w", err)
	}
	return &rec, nil
}
