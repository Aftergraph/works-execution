package store

// Durable storage for the Runtime -> WORKS dispatch acceptance seal.
// The dispatch package owns seam semantics; the WORKS SQLite store owns
// persistence so acceptance, effect identity, budget state and verification
// state survive process restart.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
)

const dispatchAcceptanceSchema = `
CREATE TABLE IF NOT EXISTS dispatch_acceptances (
    works_execution_id TEXT PRIMARY KEY,
    idempotency_key    TEXT NOT NULL UNIQUE,
    causal_id          TEXT NOT NULL,
    acceptance_json    TEXT NOT NULL,
    version            INTEGER NOT NULL DEFAULT 1,
    updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dispatch_acceptances_causal
ON dispatch_acceptances(causal_id);
`

func (s *SQLiteStore) migrateDispatchAcceptance() error {
	if _, err := s.db.Exec(dispatchAcceptanceSchema); err != nil {
		return err
	}
	hasVersion, err := s.dispatchAcceptanceHasVersionColumn()
	if err != nil {
		return err
	}
	if !hasVersion {
		if _, err := s.db.Exec(`ALTER TABLE dispatch_acceptances ADD COLUMN version INTEGER NOT NULL DEFAULT 1`); err != nil {
			return fmt.Errorf("migrate dispatch_acceptances.version: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) dispatchAcceptanceHasVersionColumn() (bool, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('dispatch_acceptances')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == "version" {
			return true, nil
		}
	}
	return false, rows.Err()
}

// DispatchAcceptanceStore returns the durable adapter consumed by
// internal/dispatch.Acceptor. It shares the WORKS database handle and never
// creates an independent source of execution truth.
func (s *SQLiteStore) DispatchAcceptanceStore() dispatch.Store {
	return &dispatchAcceptanceStore{db: s.db}
}

type dispatchAcceptanceStore struct {
	db *sql.DB
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

// AcceptIfAbsent performs the insert and duplicate lookup inside one database
// transaction. The UNIQUE idempotency_key constraint is the concurrency gate;
// no caller can win by observing an empty row and saving later.
func (s *dispatchAcceptanceStore) AcceptIfAbsent(accepted *dispatch.Acceptance) (*dispatch.Acceptance, error) {
	if accepted == nil {
		return nil, errors.New("dispatch acceptance: nil record")
	}
	if accepted.WorksExecutionID == "" || accepted.Dispatch.IdempotencyKey == "" || accepted.Dispatch.CausalID == "" {
		return nil, errors.New("dispatch acceptance: missing identity binding")
	}
	accepted.RecordVersion = 1
	payload, err := json.Marshal(accepted)
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance encode: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance begin: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		INSERT INTO dispatch_acceptances
			(works_execution_id, idempotency_key, causal_id, acceptance_json, version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		accepted.WorksExecutionID,
		accepted.Dispatch.IdempotencyKey,
		accepted.Dispatch.CausalID,
		string(payload),
		accepted.RecordVersion,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance insert: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance rows affected: %w", err)
	}
	if rows == 0 {
		existing, err := loadAcceptance(tx,
			`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json, version
			 FROM dispatch_acceptances WHERE idempotency_key = ?`,
			accepted.Dispatch.IdempotencyKey,
		)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, errors.New("dispatch acceptance conflict without existing row")
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("dispatch acceptance commit duplicate: %w", err)
		}
		return existing, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dispatch acceptance commit insert: %w", err)
	}
	return cloneDispatchAcceptance(accepted), nil
}

func cloneDispatchAcceptance(a *dispatch.Acceptance) *dispatch.Acceptance {
	if a == nil {
		return nil
	}
	cp := *a
	if a.Verdict != nil {
		verdict := *a.Verdict
		cp.Verdict = &verdict
	}
	return &cp
}

func (s *dispatchAcceptanceStore) LoadByIdempotency(key string) (*dispatch.Acceptance, error) {
	return s.load(
		`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json, version
		 FROM dispatch_acceptances WHERE idempotency_key = ?`,
		key,
	)
}

func (s *dispatchAcceptanceStore) LoadByExecution(id string) (*dispatch.Acceptance, error) {
	return s.load(
		`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json, version
		 FROM dispatch_acceptances WHERE works_execution_id = ?`,
		id,
	)
}
func (s *dispatchAcceptanceStore) load(query string, arg string) (*dispatch.Acceptance, error) {
	return loadAcceptance(s.db, query, arg)
}

func loadAcceptance(q queryRower, query string, arg string) (*dispatch.Acceptance, error) {
	var worksExecutionID, idempotencyKey, causalID, payload string
	var recordVersion int64
	err := q.QueryRow(query, arg).Scan(
		&worksExecutionID, &idempotencyKey, &causalID, &payload, &recordVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var accepted dispatch.Acceptance
	if err := json.Unmarshal([]byte(payload), &accepted); err != nil {
		return nil, fmt.Errorf("dispatch acceptance decode: %w", err)
	}
	if recordVersion < 1 {
		return nil, errors.New("dispatch acceptance invalid record version")
	}
	accepted.RecordVersion = recordVersion
	if accepted.WorksExecutionID != worksExecutionID ||
		accepted.Dispatch.IdempotencyKey != idempotencyKey ||
		accepted.Dispatch.CausalID != causalID {
		return nil, errors.New("dispatch acceptance identity mismatch")
	}
	return &accepted, nil
}

func (s *dispatchAcceptanceStore) MutateByExecution(id string, mutate func(*dispatch.Acceptance) error) (*dispatch.Acceptance, error) {
	if id == "" {
		return nil, errors.New("dispatch acceptance: execution id required")
	}
	if mutate == nil {
		return nil, errors.New("dispatch acceptance: nil mutation")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance mutation begin: %w", err)
	}
	defer tx.Rollback()

	current, err := loadAcceptance(tx,
		`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json, version
		 FROM dispatch_acceptances WHERE works_execution_id = ?`,
		id,
	)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("%w: %q", dispatch.ErrUnknownAcceptance, id)
	}
	expectedVersion := current.RecordVersion
	if err := mutate(current); err != nil {
		return nil, err
	}
	nextVersion := expectedVersion + 1
	if nextVersion <= expectedVersion {
		return nil, dispatch.ErrMutationConflict
	}
	current.RecordVersion = nextVersion
	payload, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance mutation encode: %w", err)
	}
	result, err := tx.Exec(`UPDATE dispatch_acceptances
		   SET acceptance_json = ?, version = ?, updated_at = ?
		 WHERE works_execution_id = ? AND version = ?`,
		string(payload),
		nextVersion,
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
		expectedVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance mutation update: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance mutation rows affected: %w", err)
	}
	if rows != 1 {
		return nil, dispatch.ErrMutationConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dispatch acceptance mutation commit: %w", err)
	}
	return cloneDispatchAcceptance(current), nil
}
