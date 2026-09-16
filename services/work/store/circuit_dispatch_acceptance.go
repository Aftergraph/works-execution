package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	"github.com/JonasAbde/works-execution/packages/circuitrun"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// AcceptCircuitDispatch implements CDA-0.1: dispatch acceptance and Circuit
// effect binding are one atomic WORKS transaction. It does not authorize,
// execute, or verify the effect.
func (s *SQLiteStore) AcceptCircuitDispatch(ctx context.Context, circuitRunID string, d dispatch.Dispatch, currentEpoch int64) (*dispatch.Acceptance, *circuitrun.EffectBinding, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	acceptor := dispatch.NewAcceptor(&dispatchAcceptanceTxStore{tx: tx}, nil)
	accepted, err := acceptor.Accept(d, currentEpoch)
	if err != nil {
		return nil, nil, err
	}
	binding, err := createCircuitEffectBindingTx(ctx, tx, circuitRunID, accepted)
	if err != nil {
		return nil, nil, err
	}
	if _, err := seedDispatchedReceiptTx(ctx, tx, *binding, time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return accepted, binding, nil
}

type dispatchAcceptanceTxStore struct{ tx *sql.Tx }

func (s *dispatchAcceptanceTxStore) AcceptIfAbsent(a *dispatch.Acceptance) (*dispatch.Acceptance, error) {
	if a == nil || a.WorksExecutionID == "" || a.Dispatch.IdempotencyKey == "" || a.Dispatch.CausalID == "" {
		return nil, errors.New("dispatch acceptance: missing identity binding")
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance encode: %w", err)
	}
	res, err := s.tx.Exec(`INSERT INTO dispatch_acceptances
		(works_execution_id,idempotency_key,causal_id,acceptance_json,updated_at)
		VALUES (?,?,?,?,?) ON CONFLICT(idempotency_key) DO NOTHING`,
		a.WorksExecutionID, a.Dispatch.IdempotencyKey, a.Dispatch.CausalID,
		string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("dispatch acceptance insert: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 1 {
		return cloneDispatchAcceptance(a), nil
	}
	got, err := loadAcceptance(s.tx, `SELECT works_execution_id,idempotency_key,causal_id,acceptance_json FROM dispatch_acceptances WHERE idempotency_key = ?`, a.Dispatch.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if got == nil {
		return nil, errors.New("dispatch acceptance conflict without existing row")
	}
	return got, nil
}

func (s *dispatchAcceptanceTxStore) LoadByIdempotency(key string) (*dispatch.Acceptance, error) {
	return loadAcceptance(s.tx, `SELECT works_execution_id,idempotency_key,causal_id,acceptance_json FROM dispatch_acceptances WHERE idempotency_key = ?`, key)
}

func (s *dispatchAcceptanceTxStore) LoadByExecution(id string) (*dispatch.Acceptance, error) {
	return loadAcceptance(s.tx, `SELECT works_execution_id,idempotency_key,causal_id,acceptance_json FROM dispatch_acceptances WHERE works_execution_id = ?`, id)
}
func (s *dispatchAcceptanceTxStore) Save(a *dispatch.Acceptance) error {
	if a == nil || a.WorksExecutionID == "" || a.Dispatch.IdempotencyKey == "" || a.Dispatch.CausalID == "" {
		return errors.New("dispatch acceptance: missing identity binding")
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("dispatch acceptance encode: %w", err)
	}
	_, err = s.tx.Exec(`INSERT INTO dispatch_acceptances
		(works_execution_id,idempotency_key,causal_id,acceptance_json,updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(works_execution_id) DO UPDATE SET
		idempotency_key=excluded.idempotency_key, causal_id=excluded.causal_id,
		acceptance_json=excluded.acceptance_json, updated_at=excluded.updated_at`,
		a.WorksExecutionID, a.Dispatch.IdempotencyKey, a.Dispatch.CausalID,
		string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func createCircuitEffectBindingTx(ctx context.Context, tx *sql.Tx, circuitRunID string, accepted *dispatch.Acceptance) (*circuitrun.EffectBinding, error) {
	run, err := scanCircuitRun(tx.QueryRowContext(ctx, `SELECT id,circuit_id,circuit_spec_sha256,circuit_spec_json,work_id,mission_id,created_at FROM circuit_runs WHERE id = ?`, circuitRunID))
	if err != nil {
		return nil, err
	}
	if accepted == nil || accepted.Dispatch.MissionID != run.MissionID {
		return nil, ErrCircuitEffectMissionMismatch
	}
	if err := validateBindableDispatch(accepted.Dispatch); err != nil {
		return nil, err
	}
	digest, err := dispatchIdentitySHA256(accepted.Dispatch)
	if err != nil {
		return nil, err
	}
	existing, err := getCircuitEffectBindingByExecutionTx(ctx, tx, accepted.WorksExecutionID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := validateCircuitEffectBindingAgainst(run, accepted, digest, existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	binding := &circuitrun.EffectBinding{
		ID: workgraph.NewID("ceff"), CircuitRunID: run.ID, WorkID: run.WorkID, MissionID: run.MissionID,
		WorksExecutionID: accepted.WorksExecutionID, RuntimeDispatchID: accepted.Dispatch.RuntimeDispatchID,
		DispatchAttemptID: accepted.Dispatch.AttemptID, EffectID: accepted.Dispatch.EffectID,
		VerificationSubject: accepted.Dispatch.VerificationSubj, CausalID: accepted.Dispatch.CausalID,
		DispatchSHA256: digest, BoundAt: time.Now().UTC(),
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO circuit_effect_bindings (
		id,circuit_run_id,work_id,mission_id,works_execution_id,runtime_dispatch_id,
		dispatch_attempt_id,effect_id,verification_subject,causal_id,dispatch_sha256,bound_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		binding.ID, binding.CircuitRunID, binding.WorkID, binding.MissionID,
		binding.WorksExecutionID, binding.RuntimeDispatchID, binding.DispatchAttemptID,
		binding.EffectID, binding.VerificationSubject, binding.CausalID,
		binding.DispatchSHA256, binding.BoundAt.Format(time.RFC3339Nano))
	if err != nil {
		if isSQLiteConstraint(err) {
			return nil, ErrCircuitEffectBindingConflict
		}
		return nil, fmt.Errorf("insert circuit effect binding: %w", err)
	}
	return binding, nil
}

func validateCircuitEffectBindingAgainst(run *circuitrun.Run, accepted *dispatch.Acceptance, digest string, binding *circuitrun.EffectBinding) error {
	if binding.CircuitRunID != run.ID || binding.WorkID != run.WorkID || binding.MissionID != run.MissionID ||
		binding.WorksExecutionID != accepted.WorksExecutionID || binding.RuntimeDispatchID != accepted.Dispatch.RuntimeDispatchID ||
		binding.DispatchAttemptID != accepted.Dispatch.AttemptID || binding.EffectID != accepted.Dispatch.EffectID ||
		binding.VerificationSubject != accepted.Dispatch.VerificationSubj || binding.CausalID != accepted.Dispatch.CausalID ||
		binding.DispatchSHA256 != digest {
		return ErrCircuitEffectBindingConflict
	}
	return nil
}
