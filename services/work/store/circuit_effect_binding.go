package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	"github.com/JonasAbde/works-execution/packages/circuitrun"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

var ErrCircuitEffectBindingConflict = errors.New("circuit effect binding conflict")
var ErrCircuitEffectMissionMismatch = errors.New("circuit effect mission mismatch")
var ErrCircuitEffectSubjectMismatch = errors.New("circuit effect subject mismatch")
var ErrCircuitEffectDispatchIncomplete = errors.New("circuit effect dispatch identity incomplete")

const circuitEffectBindingSchema = `
CREATE TABLE IF NOT EXISTS circuit_effect_bindings (
    id TEXT PRIMARY KEY,
    circuit_run_id TEXT NOT NULL REFERENCES circuit_runs(id) ON DELETE CASCADE,
    work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    mission_id TEXT NOT NULL,
    works_execution_id TEXT NOT NULL UNIQUE REFERENCES dispatch_acceptances(works_execution_id) ON DELETE CASCADE,
    runtime_dispatch_id TEXT NOT NULL,
    dispatch_attempt_id TEXT NOT NULL,
    effect_id TEXT NOT NULL,
    verification_subject TEXT NOT NULL,
    causal_id TEXT NOT NULL,
    dispatch_sha256 TEXT NOT NULL,
    bound_at TEXT NOT NULL,
    UNIQUE(circuit_run_id, effect_id)
);
CREATE INDEX IF NOT EXISTS idx_circuit_effect_bindings_run
ON circuit_effect_bindings(circuit_run_id, bound_at);
`

func (s *SQLiteStore) migrateCircuitEffectBinding() error {
	_, err := s.db.Exec(circuitEffectBindingSchema)
	return err
}

type dispatchIdentity struct {
	MissionID           string `json:"mission_id"`
	AuthorityRef        string `json:"authority_ref"`
	AuthorityEpoch      int64  `json:"authority_epoch"`
	RuntimeDispatchID   string `json:"runtime_dispatch_id"`
	AttemptID           string `json:"attempt_id"`
	EffectID            string `json:"effect_id"`
	IdempotencyKey      string `json:"idempotency_key"`
	BudgetRef           string `json:"budget_ref"`
	BudgetCeiling       int64  `json:"budget_ceiling"`
	CheckpointID        string `json:"checkpoint_id"`
	EvidenceRoot        string `json:"evidence_root"`
	VerificationSubject string `json:"verification_subject"`
	CausalID            string `json:"causal_id"`
}

func frozenDispatchIdentity(d dispatch.Dispatch) dispatchIdentity {
	return dispatchIdentity{
		MissionID: d.MissionID, AuthorityRef: d.AuthorityRef, AuthorityEpoch: d.AuthorityEpoch,
		RuntimeDispatchID: d.RuntimeDispatchID, AttemptID: d.AttemptID, EffectID: d.EffectID,
		IdempotencyKey: d.IdempotencyKey, BudgetRef: d.BudgetRef, BudgetCeiling: d.BudgetCeiling,
		CheckpointID: d.CheckpointID, EvidenceRoot: d.EvidenceRoot,
		VerificationSubject: d.VerificationSubj, CausalID: d.CausalID,
	}
}

func dispatchIdentitySHA256(d dispatch.Dispatch) (string, error) {
	payload, err := json.Marshal(frozenDispatchIdentity(d))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
func validateBindableDispatch(d dispatch.Dispatch) error {
	for name, value := range map[string]string{
		"runtime_dispatch_id":  d.RuntimeDispatchID,
		"attempt_id":           d.AttemptID,
		"effect_id":            d.EffectID,
		"verification_subject": d.VerificationSubj,
		"causal_id":            d.CausalID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s", ErrCircuitEffectDispatchIncomplete, name)
		}
	}
	return nil
}

func (s *SQLiteStore) loadDispatchAcceptance(worksExecutionID string) (*dispatch.Acceptance, error) {
	accepted, err := s.DispatchAcceptanceStore().LoadByExecution(worksExecutionID)
	if err != nil {
		return nil, err
	}
	if accepted == nil {
		return nil, ErrNotFound
	}
	if accepted.WorksExecutionID != worksExecutionID {
		return nil, ErrCircuitEffectSubjectMismatch
	}
	if err := validateBindableDispatch(accepted.Dispatch); err != nil {
		return nil, err
	}
	return accepted, nil
}
func (s *SQLiteStore) CreateCircuitEffectBinding(ctx context.Context, in circuitrun.EffectBindingInput) (*circuitrun.EffectBinding, error) {
	if err := in.Validate(); err != nil {
		return nil, fmt.Errorf("circuit effect input: %w", err)
	}
	run, err := s.GetCircuitRun(ctx, in.CircuitRunID)
	if err != nil {
		return nil, err
	}
	accepted, err := s.loadDispatchAcceptance(in.WorksExecutionID)
	if err != nil {
		return nil, err
	}
	if accepted.Dispatch.MissionID != run.MissionID {
		return nil, ErrCircuitEffectMissionMismatch
	}
	digest, err := dispatchIdentitySHA256(accepted.Dispatch)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	existing, err := getCircuitEffectBindingByExecutionTx(ctx, tx, in.WorksExecutionID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.CircuitRunID == run.ID && existing.DispatchSHA256 == digest {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return s.verifyCircuitEffectBinding(ctx, existing)
		}
		return nil, ErrCircuitEffectBindingConflict
	}
	binding := &circuitrun.EffectBinding{
		ID: workgraph.NewID("ceff"), CircuitRunID: run.ID, WorkID: run.WorkID,
		MissionID: run.MissionID, WorksExecutionID: accepted.WorksExecutionID,
		RuntimeDispatchID: accepted.Dispatch.RuntimeDispatchID,
		DispatchAttemptID: accepted.Dispatch.AttemptID, EffectID: accepted.Dispatch.EffectID,
		VerificationSubject: accepted.Dispatch.VerificationSubj, CausalID: accepted.Dispatch.CausalID,
		DispatchSHA256: digest, BoundAt: time.Now().UTC(),
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO circuit_effect_bindings (
        id, circuit_run_id, work_id, mission_id, works_execution_id, runtime_dispatch_id,
        dispatch_attempt_id, effect_id, verification_subject, causal_id, dispatch_sha256, bound_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return binding, nil
}
func (s *SQLiteStore) GetCircuitEffectBinding(ctx context.Context, id string) (*circuitrun.EffectBinding, error) {
	binding, err := scanCircuitEffectBinding(s.db.QueryRowContext(ctx, `SELECT
        id, circuit_run_id, work_id, mission_id, works_execution_id, runtime_dispatch_id,
        dispatch_attempt_id, effect_id, verification_subject, causal_id, dispatch_sha256, bound_at
        FROM circuit_effect_bindings WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return s.verifyCircuitEffectBinding(ctx, binding)
}

func (s *SQLiteStore) GetCircuitEffectBindingByExecutionID(ctx context.Context, worksExecutionID string) (*circuitrun.EffectBinding, error) {
	binding, err := scanCircuitEffectBinding(s.db.QueryRowContext(ctx, `SELECT
        id, circuit_run_id, work_id, mission_id, works_execution_id, runtime_dispatch_id,
        dispatch_attempt_id, effect_id, verification_subject, causal_id, dispatch_sha256, bound_at
        FROM circuit_effect_bindings WHERE works_execution_id = ?`, worksExecutionID))
	if err != nil {
		return nil, err
	}
	return s.verifyCircuitEffectBinding(ctx, binding)
}

func (s *SQLiteStore) verifyCircuitEffectBinding(ctx context.Context, binding *circuitrun.EffectBinding) (*circuitrun.EffectBinding, error) {
	run, err := s.GetCircuitRun(ctx, binding.CircuitRunID)
	if err != nil {
		return nil, err
	}
	accepted, err := s.loadDispatchAcceptance(binding.WorksExecutionID)
	if err != nil {
		return nil, err
	}
	digest, err := dispatchIdentitySHA256(accepted.Dispatch)
	if err != nil {
		return nil, err
	}
	if accepted.Dispatch.MissionID != run.MissionID ||
		binding.WorkID != run.WorkID || binding.MissionID != run.MissionID ||
		binding.RuntimeDispatchID != accepted.Dispatch.RuntimeDispatchID ||
		binding.DispatchAttemptID != accepted.Dispatch.AttemptID ||
		binding.EffectID != accepted.Dispatch.EffectID ||
		binding.VerificationSubject != accepted.Dispatch.VerificationSubj ||
		binding.CausalID != accepted.Dispatch.CausalID || binding.DispatchSHA256 != digest {
		return nil, ErrCircuitEffectSubjectMismatch
	}
	return binding, nil
}

type circuitEffectRow interface{ Scan(dest ...any) error }

func scanCircuitEffectBinding(row circuitEffectRow) (*circuitrun.EffectBinding, error) {
	var binding circuitrun.EffectBinding
	var boundAt string
	if err := row.Scan(&binding.ID, &binding.CircuitRunID, &binding.WorkID, &binding.MissionID,
		&binding.WorksExecutionID, &binding.RuntimeDispatchID, &binding.DispatchAttemptID,
		&binding.EffectID, &binding.VerificationSubject, &binding.CausalID,
		&binding.DispatchSHA256, &boundAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, boundAt)
	if err != nil {
		return nil, fmt.Errorf("parse circuit effect bound_at: %w", err)
	}
	binding.BoundAt = parsed
	return &binding, nil
}

func getCircuitEffectBindingByExecutionTx(ctx context.Context, tx *sql.Tx, worksExecutionID string) (*circuitrun.EffectBinding, error) {
	binding, err := scanCircuitEffectBinding(tx.QueryRowContext(ctx, `SELECT
        id, circuit_run_id, work_id, mission_id, works_execution_id, runtime_dispatch_id,
        dispatch_attempt_id, effect_id, verification_subject, causal_id, dispatch_sha256, bound_at
        FROM circuit_effect_bindings WHERE works_execution_id = ?`, worksExecutionID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return binding, err
}
func (s *SQLiteStore) ListCircuitEffectBindingsByRunID(ctx context.Context, circuitRunID string) ([]circuitrun.EffectBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
        id, circuit_run_id, work_id, mission_id, works_execution_id, runtime_dispatch_id,
        dispatch_attempt_id, effect_id, verification_subject, causal_id, dispatch_sha256, bound_at
        FROM circuit_effect_bindings WHERE circuit_run_id = ? ORDER BY bound_at, id`, circuitRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []circuitrun.EffectBinding
	for rows.Next() {
		binding, err := scanCircuitEffectBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, *binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range bindings {
		verified, err := s.verifyCircuitEffectBinding(ctx, &bindings[i])
		if err != nil {
			return nil, err
		}
		bindings[i] = *verified
	}
	return bindings, nil
}
