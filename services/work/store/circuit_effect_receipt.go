package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/JonasAbde/works-execution/packages/circuitrun"
)

var ErrCircuitEffectReceiptTransition = errors.New("invalid circuit effect receipt transition")
var ErrCircuitEffectReceiptSubjectMismatch = errors.New("circuit effect receipt subject mismatch")
var ErrCircuitEffectVerifierNotIndependent = errors.New("circuit effect verifier not independent")
var ErrCircuitEffectVerdictConflict = errors.New("circuit effect verdict conflict")

const circuitEffectOutcomeSchema = `
CREATE TABLE IF NOT EXISTS circuit_effect_receipts (
  id TEXT PRIMARY KEY,
  effect_binding_id TEXT NOT NULL REFERENCES circuit_effect_bindings(id) ON DELETE CASCADE,
  circuit_run_id TEXT NOT NULL REFERENCES circuit_runs(id) ON DELETE CASCADE,
  effect_id TEXT NOT NULL,
  verification_subject TEXT NOT NULL,
  dispatch_sha256 TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  state TEXT NOT NULL,
  executor_id TEXT NOT NULL,
  evidence_ref TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  receipt_sha256 TEXT NOT NULL,
  UNIQUE(effect_binding_id, sequence)
);
CREATE INDEX IF NOT EXISTS idx_circuit_effect_receipts_binding
ON circuit_effect_receipts(effect_binding_id, sequence);

CREATE TABLE IF NOT EXISTS circuit_effect_verdicts (
  effect_receipt_id TEXT PRIMARY KEY REFERENCES circuit_effect_receipts(id) ON DELETE CASCADE,
  receipt_sha256 TEXT NOT NULL,
  verification_subject TEXT NOT NULL,
  subject TEXT NOT NULL,
  result TEXT NOT NULL,
  verifier_id TEXT NOT NULL,
  evidence_ref TEXT NOT NULL,
  verified_at TEXT NOT NULL
);`

func (s *SQLiteStore) migrateCircuitEffectOutcome() error {
	_, err := s.db.Exec(circuitEffectOutcomeSchema)
	return err
}

func seedDispatchedReceiptTx(ctx context.Context, tx *sql.Tx, binding circuitrun.EffectBinding, at time.Time) (*circuitrun.EffectReceipt, error) {
	in := circuitrun.EffectReceiptInput{EffectBindingID: binding.ID, State: circuitrun.EffectDispatched, RecordedAt: at.UTC()}
	receipt, err := circuitrun.NewEffectReceipt(binding, 1, in)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO circuit_effect_receipts
	(id,effect_binding_id,circuit_run_id,effect_id,verification_subject,dispatch_sha256,sequence,state,executor_id,evidence_ref,recorded_at,receipt_sha256)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.EffectBindingID, receipt.CircuitRunID, receipt.EffectID,
		receipt.VerificationSubject, receipt.DispatchSHA256, receipt.Sequence, string(receipt.State), receipt.ExecutorID,
		receipt.EvidenceRef, receipt.RecordedAt.Format(time.RFC3339Nano), receipt.ReceiptSHA256)
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}
func (s *SQLiteStore) GetLatestCircuitEffectReceipt(ctx context.Context, bindingID string) (*circuitrun.EffectReceipt, error) {
	return scanEffectReceipt(s.db.QueryRowContext(ctx, `SELECT id,effect_binding_id,circuit_run_id,effect_id,verification_subject,
	dispatch_sha256,sequence,state,executor_id,evidence_ref,recorded_at,receipt_sha256
	FROM circuit_effect_receipts WHERE effect_binding_id=? ORDER BY sequence DESC LIMIT 1`, bindingID))
}

func (s *SQLiteStore) RecordCircuitEffectOutcome(ctx context.Context, in circuitrun.EffectReceiptInput) (*circuitrun.EffectReceipt, error) {
	binding, err := s.GetCircuitEffectBinding(ctx, in.EffectBindingID)
	if err != nil {
		return nil, err
	}
	latest, err := s.GetLatestCircuitEffectReceipt(ctx, binding.ID)
	if err != nil {
		return nil, err
	}
	if !circuitrun.CanTransitionEffect(latest.State, in.State) {
		return nil, ErrCircuitEffectReceiptTransition
	}
	receipt, err := circuitrun.NewEffectReceipt(*binding, latest.Sequence+1, in)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO circuit_effect_receipts
	(id,effect_binding_id,circuit_run_id,effect_id,verification_subject,dispatch_sha256,sequence,state,executor_id,evidence_ref,recorded_at,receipt_sha256)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.EffectBindingID, receipt.CircuitRunID, receipt.EffectID,
		receipt.VerificationSubject, receipt.DispatchSHA256, receipt.Sequence, string(receipt.State), receipt.ExecutorID,
		receipt.EvidenceRef, receipt.RecordedAt.Format(time.RFC3339Nano), receipt.ReceiptSHA256)
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}

type effectReceiptRow interface{ Scan(...any) error }

func scanEffectReceipt(row effectReceiptRow) (*circuitrun.EffectReceipt, error) {
	var r circuitrun.EffectReceipt
	var state, at string
	if err := row.Scan(&r.ID, &r.EffectBindingID, &r.CircuitRunID, &r.EffectID, &r.VerificationSubject, &r.DispatchSHA256,
		&r.Sequence, &state, &r.ExecutorID, &r.EvidenceRef, &at, &r.ReceiptSHA256); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.State = circuitrun.EffectState(state)
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, err
	}
	r.RecordedAt = parsed
	return &r, nil
}
func (s *SQLiteStore) CreateCircuitEffectVerdict(ctx context.Context, in circuitrun.EffectVerdictInput) (*circuitrun.EffectVerdict, error) {
	receipt, err := scanEffectReceipt(s.db.QueryRowContext(ctx, `SELECT id,effect_binding_id,circuit_run_id,effect_id,verification_subject,
	dispatch_sha256,sequence,state,executor_id,evidence_ref,recorded_at,receipt_sha256 FROM circuit_effect_receipts WHERE id=?`, in.EffectReceiptID))
	if err != nil {
		return nil, err
	}
	if err := in.ValidateAgainstExecutor(receipt.ExecutorID); err != nil {
		if in.VerifierID == receipt.ExecutorID {
			return nil, ErrCircuitEffectVerifierNotIndependent
		}
		return nil, err
	}
	subject, err := circuitrun.EffectReceiptSubject(*receipt)
	if err != nil {
		return nil, err
	}
	want := &circuitrun.EffectVerdict{EffectReceiptID: receipt.ID, ReceiptSHA256: receipt.ReceiptSHA256,
		VerificationSubject: receipt.VerificationSubject, Subject: subject, Result: in.Result,
		VerifierID: in.VerifierID, EvidenceRef: in.EvidenceRef, VerifiedAt: in.VerifiedAt.UTC()}
	_, err = s.db.ExecContext(ctx, `INSERT INTO circuit_effect_verdicts
	(effect_receipt_id,receipt_sha256,verification_subject,subject,result,verifier_id,evidence_ref,verified_at)
	VALUES (?,?,?,?,?,?,?,?)`, want.EffectReceiptID, want.ReceiptSHA256, want.VerificationSubject, want.Subject,
		want.Result, want.VerifierID, want.EvidenceRef, want.VerifiedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isSQLiteConstraint(err) {
			existing, getErr := s.GetCircuitEffectVerdict(ctx, receipt.ID)
			if getErr != nil {
				return nil, getErr
			}
			if existing.Result == want.Result && existing.VerifierID == want.VerifierID && existing.EvidenceRef == want.EvidenceRef && existing.VerifiedAt.Equal(want.VerifiedAt) {
				return existing, nil
			}
			return nil, ErrCircuitEffectVerdictConflict
		}
		return nil, err
	}
	return want, nil
}

func (s *SQLiteStore) GetCircuitEffectVerdict(ctx context.Context, receiptID string) (*circuitrun.EffectVerdict, error) {
	var v circuitrun.EffectVerdict
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT effect_receipt_id,receipt_sha256,verification_subject,subject,result,verifier_id,evidence_ref,verified_at
	FROM circuit_effect_verdicts WHERE effect_receipt_id=?`, receiptID).Scan(&v.EffectReceiptID, &v.ReceiptSHA256, &v.VerificationSubject, &v.Subject, &v.Result, &v.VerifierID, &v.EvidenceRef, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, err
	}
	v.VerifiedAt = parsed
	receipt, err := scanEffectReceipt(s.db.QueryRowContext(ctx, `SELECT id,effect_binding_id,circuit_run_id,effect_id,verification_subject,
	dispatch_sha256,sequence,state,executor_id,evidence_ref,recorded_at,receipt_sha256 FROM circuit_effect_receipts WHERE id=?`, receiptID))
	if err != nil {
		return nil, err
	}
	subject, err := circuitrun.EffectReceiptSubject(*receipt)
	if err != nil {
		return nil, err
	}
	if v.ReceiptSHA256 != receipt.ReceiptSHA256 || v.VerificationSubject != receipt.VerificationSubject || v.Subject != subject {
		return nil, ErrCircuitEffectReceiptSubjectMismatch
	}
	return &v, nil
}
