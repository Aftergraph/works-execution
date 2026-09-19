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

CREATE TABLE IF NOT EXISTS dispatch_authority_bindings (
    idempotency_key TEXT PRIMARY KEY,
    action_id       TEXT NOT NULL,
    binding_digest  TEXT NOT NULL,
    evidence_ref    TEXT NOT NULL,
    revalidated_at  TEXT NOT NULL
);
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

// LoadAuthorityBinding returns the independently-owned authority identity that
// was atomically bound to the winning acceptance. It is intentionally stored
// outside acceptance_json so dispatch.acceptance/1.0 remains wire-frozen.
func (s *dispatchAcceptanceStore) LoadAuthorityBinding(idempotencyKey string) (*dispatch.AuthorityBinding, error) {
	return loadAuthorityBinding(
		s.db,
		`SELECT action_id, binding_digest, evidence_ref
		 FROM dispatch_authority_bindings WHERE idempotency_key = ?`,
		idempotencyKey,
	)
}

func loadAuthorityBinding(q queryRower, query, idempotencyKey string) (*dispatch.AuthorityBinding, error) {
	var actionID, bindingDigest, evidenceRef string
	err := q.QueryRow(query, idempotencyKey).Scan(&actionID, &bindingDigest, &evidenceRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dispatch.AuthorityBinding{
		ActionID:      actionID,
		BindingDigest: bindingDigest,
		EvidenceRef:   evidenceRef,
	}, nil
}

// AcceptRevalidatedIfAbsent commits the authority proof and acceptance in one
// SQLite transaction. A crash can therefore leave neither or both, never an
// accepted execution without the exact action/binding evidence that authorized
// it. A duplicate governed request returns the original winner.
func (s *dispatchAcceptanceStore) AcceptRevalidatedIfAbsent(
	accepted *dispatch.Acceptance,
	binding dispatch.AuthorityBinding,
) (*dispatch.Acceptance, *dispatch.AuthorityBinding, error) {
	if accepted == nil {
		return nil, nil, errors.New("dispatch acceptance: nil governed record")
	}
	if accepted.WorksExecutionID == "" || accepted.Dispatch.IdempotencyKey == "" || accepted.Dispatch.CausalID == "" {
		return nil, nil, errors.New("dispatch acceptance: missing governed identity binding")
	}
	if binding.ActionID == "" || binding.BindingDigest == "" || binding.EvidenceRef == "" {
		return nil, nil, errors.New("dispatch acceptance: incomplete authority binding")
	}

	payload, err := json.Marshal(accepted)
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch acceptance encode: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch governed acceptance begin: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	bindingResult, err := tx.Exec(`
		INSERT INTO dispatch_authority_bindings
			(idempotency_key, action_id, binding_digest, evidence_ref, revalidated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		accepted.Dispatch.IdempotencyKey,
		binding.ActionID,
		binding.BindingDigest,
		binding.EvidenceRef,
		now,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch authority binding insert: %w", err)
	}
	bindingRows, err := bindingResult.RowsAffected()
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch authority binding rows affected: %w", err)
	}

	if bindingRows == 0 {
		// Another governed caller may have won. It is only a valid replay when
		// the matching acceptance exists in the same durable state. A lone
		// binding is corruption/stale partial state and must not authorize work.
		existing, err := loadAcceptance(tx,
			`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json
			 FROM dispatch_acceptances WHERE idempotency_key = ?`,
			accepted.Dispatch.IdempotencyKey,
		)
		if err != nil {
			return nil, nil, err
		}
		existingBinding, err := loadAuthorityBinding(tx,
			`SELECT action_id, binding_digest, evidence_ref
			 FROM dispatch_authority_bindings WHERE idempotency_key = ?`,
			accepted.Dispatch.IdempotencyKey,
		)
		if err != nil {
			return nil, nil, err
		}
		if existing == nil || existingBinding == nil {
			return nil, nil, errors.New("dispatch acceptance: orphan authority binding")
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, fmt.Errorf("dispatch governed acceptance commit duplicate: %w", err)
		}
		return existing, existingBinding, nil
	}

	acceptanceResult, err := tx.Exec(`
		INSERT INTO dispatch_acceptances
			(works_execution_id, idempotency_key, causal_id, acceptance_json, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		accepted.WorksExecutionID,
		accepted.Dispatch.IdempotencyKey,
		accepted.Dispatch.CausalID,
		string(payload),
		now,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch governed acceptance insert: %w", err)
	}
	acceptanceRows, err := acceptanceResult.RowsAffected()
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch governed acceptance rows affected: %w", err)
	}
	if acceptanceRows != 1 {
		return nil, nil, errors.New("dispatch acceptance: legacy/governed idempotency collision")
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("dispatch governed acceptance commit: %w", err)
	}
	return cloneDispatchAcceptance(accepted), &binding, nil
}
