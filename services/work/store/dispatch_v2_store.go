package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// DispatchAcceptanceV2Store binds one route-scoped Work to the V2 acceptance
// transaction. WORKS remains the durable owner; this adapter does not authorize
// effects and does not evaluate AIE authority.
func (s *SQLiteStore) DispatchAcceptanceV2Store(workID string) dispatch.V2Store {
	return &dispatchV2Store{
		dispatchAcceptanceStore: &dispatchAcceptanceStore{db: s.db},
		db:                      s.db,
		workID:                  workID,
	}
}

type dispatchV2Store struct {
	*dispatchAcceptanceStore
	db     *sql.DB
	workID string
}

func (s *dispatchV2Store) AcceptContextualIfAbsent(
	ctx context.Context,
	accepted *dispatch.Acceptance,
	binding dispatch.V2Binding,
) (*dispatch.Acceptance, *executioncontext.Context, error) {
	if accepted == nil || accepted.ContractVersion != "dispatch.acceptance/2.0" {
		return nil, nil, dispatch.ErrContextBinding
	}
	if binding.WorkID == "" || binding.WorkID != s.workID || accepted.WorkID != s.workID {
		return nil, nil, dispatch.ErrContextBinding
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch v2 begin: %w", err)
	}
	defer tx.Rollback()

	// Replay/readback precedes current WorkerLease freshness checks. An already
	// committed acceptance remains recoverable after the worker lease later
	// expires or is released; a NEW acceptance must pass the checks below.
	existing, err := loadAcceptance(tx,
		`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json
		 FROM dispatch_acceptances WHERE idempotency_key = ?`,
		accepted.Dispatch.IdempotencyKey,
	)
	if err != nil {
		return nil, nil, err
	}
	if existing != nil {
		if existing.ContractVersion != "dispatch.acceptance/2.0" ||
			existing.WorkID != s.workID ||
			existing.Dispatch.CausalID != accepted.Dispatch.CausalID ||
			existing.Dispatch.MissionID != accepted.Dispatch.MissionID ||
			existing.Dispatch.AuthorityRef != accepted.Dispatch.AuthorityRef {
			return nil, nil, fmt.Errorf("%w: key %q", dispatch.ErrCausalMismatch, accepted.Dispatch.IdempotencyKey)
		}
		ec, err := loadExecutionContextTx(ctx, tx, existing.ExecutionContextID)
		if err != nil {
			return nil, nil, err
		}
		if ec == nil {
			return nil, nil, dispatch.ErrContextBinding
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, fmt.Errorf("dispatch v2 replay commit: %w", err)
		}
		return existing, ec, nil
	}

	var leaseWorkID, workerID, leaseStatus, expiresAt string
	err = tx.QueryRowContext(ctx,
		`SELECT work_id, worker_id, status, expires_at FROM work_leases WHERE id = ?`,
		binding.WorkerLeaseID,
	).Scan(&leaseWorkID, &workerID, &leaseStatus, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, dispatch.ErrWorkerLeaseUnavailable
	}
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch v2 read worker lease: %w", err)
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || leaseWorkID != s.workID ||
		leaseStatus != string(workgraph.LeaseActive) || !expiry.After(time.Now().UTC()) {
		return nil, nil, dispatch.ErrWorkerLeaseUnavailable
	}

	ec := &executioncontext.Context{
		Schema:              "execution-context/1.0",
		ID:                  accepted.ExecutionContextID,
		OrganizationID:      binding.OrganizationID,
		TenantID:            binding.TenantID,
		PrincipalID:         binding.PrincipalID,
		MissionID:           accepted.Dispatch.MissionID,
		AuthorityLeaseID:    binding.AuthorityLeaseID,
		WorkID:              binding.WorkID,
		WorkerID:            workerID,
		WorkerLeaseID:       binding.WorkerLeaseID,
		AdmissionDecisionID: binding.AdmissionDecisionID,
		TraceID:             accepted.TraceID,
	}
	if err := ec.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", dispatch.ErrContextBinding, err)
	}

	payload, err := json.Marshal(accepted)
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch v2 encode acceptance: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
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
		return nil, nil, fmt.Errorf("dispatch v2 insert acceptance: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch v2 rows affected: %w", err)
	}
	if rows == 0 {
		winner, err := loadAcceptance(tx,
			`SELECT works_execution_id, idempotency_key, causal_id, acceptance_json
			 FROM dispatch_acceptances WHERE idempotency_key = ?`,
			accepted.Dispatch.IdempotencyKey,
		)
		if err != nil {
			return nil, nil, err
		}
		if winner == nil {
			return nil, nil, errors.New("dispatch v2 conflict without committed winner")
		}
		winnerCtx, err := loadExecutionContextTx(ctx, tx, winner.ExecutionContextID)
		if err != nil {
			return nil, nil, err
		}
		if winnerCtx == nil {
			return nil, nil, dispatch.ErrContextBinding
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, fmt.Errorf("dispatch v2 duplicate commit: %w", err)
		}
		return winner, winnerCtx, nil
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO work_execution_contexts (
		id, prior_context_id, work_id, organization_id, tenant_id, principal_id, mission_id,
		authority_lease_id, worker_id, worker_lease_id, admission_decision_id, trace_id, created_at
	) VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ec.ID, ec.WorkID, ec.OrganizationID, ec.TenantID, ec.PrincipalID, ec.MissionID,
		ec.AuthorityLeaseID, ec.WorkerID, ec.WorkerLeaseID, ec.AdmissionDecisionID, ec.TraceID, now,
	)
	if err != nil {
		if isSQLiteConstraint(err) {
			return nil, nil, dispatch.ErrContextBinding
		}
		return nil, nil, fmt.Errorf("dispatch v2 insert execution context: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("dispatch v2 commit: %w", err)
	}
	return cloneDispatchAcceptance(accepted), ec, nil
}

func loadExecutionContextTx(
	ctx context.Context,
	tx *sql.Tx,
	id string,
) (*executioncontext.Context, error) {
	var c executioncontext.Context
	c.Schema = "execution-context/1.0"
	err := tx.QueryRowContext(ctx, `SELECT id, COALESCE(prior_context_id,''), work_id, organization_id, tenant_id,
		principal_id, mission_id, authority_lease_id, worker_id, worker_lease_id, admission_decision_id, trace_id
		FROM work_execution_contexts WHERE id = ?`, id).Scan(
		&c.ID, &c.PriorContextID, &c.WorkID, &c.OrganizationID, &c.TenantID, &c.PrincipalID,
		&c.MissionID, &c.AuthorityLeaseID, &c.WorkerID, &c.WorkerLeaseID, &c.AdmissionDecisionID, &c.TraceID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}
