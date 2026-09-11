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
	var worksExecutionID, idempotencyKey, causalID, payload string
	err := s.db.QueryRow(query, arg).Scan(
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
