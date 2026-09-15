package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

var ErrExecutionContextConflict = errors.New("execution context conflict")
var ErrExecutionContextLeaseMismatch = errors.New("execution context worker lease mismatch")

func (s *SQLiteStore) CreateExecutionContext(ctx context.Context, in executioncontext.Context) (*executioncontext.Context, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var leaseWorkID, workerID string
	err = tx.QueryRowContext(ctx, `SELECT work_id, worker_id FROM work_leases WHERE id = ?`, in.WorkerLeaseID).Scan(&leaseWorkID, &workerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrExecutionContextLeaseMismatch
		}
		return nil, err
	}
	if leaseWorkID != in.WorkID {
		return nil, ErrExecutionContextLeaseMismatch
	}

	in.Schema = "execution-context/1.0"
	if in.ID == "" {
		in.ID = workgraph.NewID("ctx")
	}
	in.WorkerID = workerID
	if err := in.Validate(); err != nil {
		return nil, err
	}

	if in.PriorContextID != "" {
		var priorWorkID string
		if err := tx.QueryRowContext(ctx, `SELECT work_id FROM work_execution_contexts WHERE id = ?`, in.PriorContextID).Scan(&priorWorkID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrExecutionContextConflict
			}
			return nil, err
		}
		if priorWorkID != in.WorkID {
			return nil, ErrExecutionContextConflict
		}
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO work_execution_contexts (
        id, prior_context_id, work_id, organization_id, tenant_id, principal_id, mission_id,
        authority_lease_id, worker_id, worker_lease_id, admission_decision_id, trace_id, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, nullString(in.PriorContextID), in.WorkID, in.OrganizationID, in.TenantID, in.PrincipalID, in.MissionID,
		in.AuthorityLeaseID, in.WorkerID, in.WorkerLeaseID, in.AdmissionDecisionID, in.TraceID,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isSQLiteConstraint(err) {
			return nil, ErrExecutionContextConflict
		}
		return nil, fmt.Errorf("insert execution context: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := in
	return &out, nil
}

func (s *SQLiteStore) GetExecutionContext(ctx context.Context, id string) (*executioncontext.Context, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, COALESCE(prior_context_id,''), work_id, organization_id, tenant_id,
        principal_id, mission_id, authority_lease_id, worker_id, worker_lease_id, admission_decision_id, trace_id
        FROM work_execution_contexts WHERE id = ?`, id)
	var c executioncontext.Context
	c.Schema = "execution-context/1.0"
	if err := row.Scan(&c.ID, &c.PriorContextID, &c.WorkID, &c.OrganizationID, &c.TenantID, &c.PrincipalID,
		&c.MissionID, &c.AuthorityLeaseID, &c.WorkerID, &c.WorkerLeaseID, &c.AdmissionDecisionID, &c.TraceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (s *SQLiteStore) ListExecutionContextsByWorkID(ctx context.Context, workID string) ([]executioncontext.Context, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(prior_context_id,''), work_id, organization_id, tenant_id,
        principal_id, mission_id, authority_lease_id, worker_id, worker_lease_id, admission_decision_id, trace_id
        FROM work_execution_contexts WHERE work_id = ? ORDER BY created_at, id`, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []executioncontext.Context
	for rows.Next() {
		var c executioncontext.Context
		c.Schema = "execution-context/1.0"
		if err := rows.Scan(&c.ID, &c.PriorContextID, &c.WorkID, &c.OrganizationID, &c.TenantID, &c.PrincipalID,
			&c.MissionID, &c.AuthorityLeaseID, &c.WorkerID, &c.WorkerLeaseID, &c.AdmissionDecisionID, &c.TraceID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func isSQLiteConstraint(err error) bool {
	return err != nil && (contains(err.Error(), "constraint") || contains(err.Error(), "UNIQUE"))
}
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
