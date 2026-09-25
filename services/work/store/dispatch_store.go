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
    updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dispatch_acceptances_causal
ON dispatch_acceptances(causal_id);
`

func (s *SQLiteStore) migrateDispatchAcceptance() error {
	_, err := s.db.Exec(dispatchAcceptanceSchema)
	return err
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
			(works_execution_id, idempotency_key, causal_id, acceptance_json, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		accepted.WorksExecutionID,
		accepted.Dispatch.IdempotencyKey,
		accepted.Dispatch.CausalID,
		string(payload),
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
			`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json
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
		`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json
		 FROM dispatch_acceptances WHERE idempotency_key = ?`,
		key,
	)
}

func (s *dispatchAcceptanceStore) LoadByExecution(id string) (*dispatch.Acceptance, error) {
	return s.load(
		`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json
		 FROM dispatch_acceptances WHERE works_execution_id = ?`,
		id,
	)
}
func (s *dispatchAcceptanceStore) load(query string, arg string) (*dispatch.Acceptance, error) {
	return loadAcceptance(s.db, query, arg)
}

func loadAcceptance(q queryRower, query string, arg string) (*dispatch.Acceptance, error) {
	var worksExecutionID, idempotencyKey, causalID, payload string
	err := q.QueryRow(query, arg).Scan(
		&worksExecutionID, &idempotencyKey, &causalID, &payload,
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
	if accepted.WorksExecutionID != worksExecutionID ||
		accepted.Dispatch.IdempotencyKey != idempotencyKey ||
		accepted.Dispatch.CausalID != causalID {
		return nil, errors.New("dispatch acceptance identity mismatch")
	}
	return &accepted, nil
}

// SpendIfWithinCeiling atomically adds amount to BudgetSpent only when the
// record exists, is not revoked, and spent+amount stays within the ceiling.
// The check and the increment happen inside one transaction on the JSON
// payload, so concurrent spends cannot interleave load-modify-save past the
// ceiling. Returns (nil, nil) when the conditional update did not apply;
// the caller re-loads to classify the rejection.
func (s *dispatchAcceptanceStore) SpendIfWithinCeiling(worksExecutionID string, amount int64) (*dispatch.Acceptance, error) {
	if amount <= 0 {
		return nil, errors.New("dispatch acceptance: spend amount must be positive")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance begin spend: %w", err)
	}
	defer tx.Rollback()
	acc, err := loadAcceptance(tx,
		`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json
			 FROM dispatch_acceptances WHERE works_execution_id = ?`,
		worksExecutionID,
	)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, nil
	}
	if acc.Revoked {
		return nil, nil
	}
	if acc.Dispatch.BudgetCeiling < 0 || acc.BudgetSpent < 0 || acc.BudgetSpent > acc.Dispatch.BudgetCeiling {
		return nil, nil
	}
	// Subtraction form so an overflowing amount cannot wrap below the ceiling.
	if amount > acc.Dispatch.BudgetCeiling-acc.BudgetSpent {
		return nil, nil
	}
	acc.BudgetSpent += amount
	payload, err := json.Marshal(acc)
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance encode spend: %w", err)
	}
	result, err := tx.Exec(`
		UPDATE dispatch_acceptances
			SET acceptance_json = ?, updated_at = ?
			WHERE works_execution_id = ?`,
		string(payload),
		time.Now().UTC().Format(time.RFC3339Nano),
		worksExecutionID,
	)
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance spend: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance spend rows affected: %w", err)
	}
	if rows == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dispatch acceptance commit spend: %w", err)
	}
	return cloneDispatchAcceptance(acc), nil
}

func (s *dispatchAcceptanceStore) Save(accepted *dispatch.Acceptance) error {
	if accepted == nil {
		return errors.New("dispatch acceptance: nil record")
	}
	if accepted.WorksExecutionID == "" || accepted.Dispatch.IdempotencyKey == "" || accepted.Dispatch.CausalID == "" {
		return errors.New("dispatch acceptance: missing identity binding")
	}
	payload, err := json.Marshal(accepted)
	if err != nil {
		return fmt.Errorf("dispatch acceptance encode: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO dispatch_acceptances
			(works_execution_id, idempotency_key, causal_id, acceptance_json, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(works_execution_id) DO UPDATE SET
			idempotency_key = excluded.idempotency_key,
			causal_id = excluded.causal_id,
			acceptance_json = excluded.acceptance_json,
			updated_at = excluded.updated_at`,
		accepted.WorksExecutionID,
		accepted.Dispatch.IdempotencyKey,
		accepted.Dispatch.CausalID,
		string(payload),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("dispatch acceptance save: %w", err)
	}
	return nil
}