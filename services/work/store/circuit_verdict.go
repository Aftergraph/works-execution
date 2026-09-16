package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/circuitrun"
)

var ErrCircuitVerdictConflict = errors.New("circuit verdict conflict")
var ErrCircuitVerdictWorkNotTerminal = errors.New("circuit verdict requires terminal work")
var ErrCircuitVerdictSubjectMismatch = errors.New("circuit verdict exact subject mismatch")

func (s *SQLiteStore) CreateCircuitVerdict(ctx context.Context, in circuitrun.VerdictInput) (*circuitrun.Verdict, error) {
	if err := in.Validate(); err != nil {
		return nil, fmt.Errorf("circuit verdict input: %w", err)
	}
	run, err := s.GetCircuitRun(ctx, in.CircuitRunID)
	if err != nil {
		return nil, err
	}
	work, err := s.GetWork(ctx, run.WorkID)
	if err != nil {
		return nil, err
	}
	if !work.State.IsTerminal() {
		return nil, ErrCircuitVerdictWorkNotTerminal
	}
	subject, err := circuitrun.Subject(*run)
	if err != nil {
		return nil, fmt.Errorf("derive circuit verdict subject: %w", err)
	}
	want := &circuitrun.Verdict{
		CircuitRunID: run.ID, CircuitSpecSHA256: run.CircuitSpecSHA256,
		Subject: subject, Result: in.Result, VerifierID: in.VerifierID,
		EvidenceRef: in.EvidenceRef, VerifiedAt: in.VerifiedAt.UTC(),
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	existing, err := getCircuitVerdictTx(ctx, tx, run.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if verdictEqual(existing, want) {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return existing, nil
		}
		return nil, ErrCircuitVerdictConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO circuit_verdicts (
        circuit_run_id, circuit_spec_sha256, subject, result, verifier_id, evidence_ref, verified_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		want.CircuitRunID, want.CircuitSpecSHA256, want.Subject, want.Result,
		want.VerifierID, want.EvidenceRef, want.VerifiedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isSQLiteConstraint(err) {
			return nil, ErrCircuitVerdictConflict
		}
		return nil, fmt.Errorf("insert circuit verdict: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return want, nil
}

func (s *SQLiteStore) GetCircuitVerdict(ctx context.Context, circuitRunID string) (*circuitrun.Verdict, error) {
	run, err := s.GetCircuitRun(ctx, circuitRunID)
	if err != nil {
		return nil, err
	}
	got, err := scanCircuitVerdict(s.db.QueryRowContext(ctx, `SELECT circuit_run_id, circuit_spec_sha256,
        subject, result, verifier_id, evidence_ref, verified_at FROM circuit_verdicts WHERE circuit_run_id = ?`, circuitRunID))
	if err != nil {
		return nil, err
	}
	return validateStoredCircuitVerdict(run, got)
}
func getCircuitVerdictTx(ctx context.Context, tx *sql.Tx, circuitRunID string) (*circuitrun.Verdict, error) {
	got, err := scanCircuitVerdict(tx.QueryRowContext(ctx, `SELECT circuit_run_id, circuit_spec_sha256,
        subject, result, verifier_id, evidence_ref, verified_at FROM circuit_verdicts WHERE circuit_run_id = ?`, circuitRunID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return got, err
}

func scanCircuitVerdict(row rowScanner) (*circuitrun.Verdict, error) {
	var got circuitrun.Verdict
	var verifiedAt string
	if err := row.Scan(&got.CircuitRunID, &got.CircuitSpecSHA256, &got.Subject,
		&got.Result, &got.VerifierID, &got.EvidenceRef, &verifiedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, verifiedAt)
	if err != nil {
		return nil, fmt.Errorf("parse circuit verdict verified_at: %w", err)
	}
	got.VerifiedAt = parsed
	return &got, nil
}
func validateStoredCircuitVerdict(run *circuitrun.Run, got *circuitrun.Verdict) (*circuitrun.Verdict, error) {
	if run == nil || got == nil {
		return nil, ErrCircuitVerdictSubjectMismatch
	}
	wantSubject, err := circuitrun.Subject(*run)
	if err != nil {
		return nil, ErrCircuitVerdictSubjectMismatch
	}
	if got.CircuitRunID != run.ID ||
		got.CircuitSpecSHA256 != run.CircuitSpecSHA256 ||
		got.Subject != wantSubject {
		return nil, ErrCircuitVerdictSubjectMismatch
	}
	return got, nil
}

func verdictEqual(a, b *circuitrun.Verdict) bool {
	if a == nil || b == nil {
		return false
	}
	return a.CircuitRunID == b.CircuitRunID &&
		a.CircuitSpecSHA256 == b.CircuitSpecSHA256 &&
		a.Subject == b.Subject && a.Result == b.Result &&
		a.VerifierID == b.VerifierID && a.EvidenceRef == b.EvidenceRef &&
		a.VerifiedAt.Equal(b.VerifiedAt)
}
