package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// VerificationVerdict is the durable semantic-verification record for a Work.
// It is intentionally separate from Work.State and work evidence integrity:
// execution may finish without an independent verifier having ruled on the
// requested outcome.
type VerificationVerdict struct {
	WorkID      string    `json:"work_id"`
	Result      string    `json:"result"`
	VerifierID  string    `json:"verifier_id"`
	EvidenceRef string    `json:"evidence_ref"`
	VerifiedAt  time.Time `json:"verified_at"`
}

const verificationVerdictSchema = `
CREATE TABLE IF NOT EXISTS work_verification_verdicts (
    work_id      TEXT PRIMARY KEY REFERENCES works(id) ON DELETE CASCADE,
    result       TEXT NOT NULL,
    verifier_id  TEXT NOT NULL,
    evidence_ref TEXT NOT NULL,
    verified_at  TEXT NOT NULL
);`

func (s *SQLiteStore) ensureVerificationVerdictTable(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, verificationVerdictSchema); err != nil {
		return fmt.Errorf("verification verdict schema: %w", err)
	}
	return nil
}

// SaveVerificationVerdict persists the independent verifier's semantic
// verdict. This first persistence slice deliberately does not infer or derive
// a verdict from execution state; callers must supply the verifier record.
func (s *SQLiteStore) SaveVerificationVerdict(ctx context.Context, v VerificationVerdict) error {
	if err := s.ensureVerificationVerdictTable(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO work_verification_verdicts(work_id, result, verifier_id, evidence_ref, verified_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(work_id) DO UPDATE SET
    result = excluded.result,
    verifier_id = excluded.verifier_id,
    evidence_ref = excluded.evidence_ref,
    verified_at = excluded.verified_at`,
		v.WorkID, v.Result, v.VerifierID, v.EvidenceRef,
		v.VerifiedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save verification verdict: %w", err)
	}
	return nil
}

// GetVerificationVerdict returns the durable verifier record for workID.
// A Work with no independent verdict returns (nil, nil), preserving the
// distinction between execution completion and verified outcome.
func (s *SQLiteStore) GetVerificationVerdict(ctx context.Context, workID string) (*VerificationVerdict, error) {
	if err := s.ensureVerificationVerdictTable(ctx); err != nil {
		return nil, err
	}
	var v VerificationVerdict
	var verifiedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT work_id, result, verifier_id, evidence_ref, verified_at
FROM work_verification_verdicts
WHERE work_id = ?`, workID).Scan(
		&v.WorkID, &v.Result, &v.VerifierID, &v.EvidenceRef, &verifiedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get verification verdict: %w", err)
	}
	v.VerifiedAt, err = time.Parse(time.RFC3339Nano, verifiedAt)
	if err != nil {
		return nil, fmt.Errorf("parse verification verdict verified_at: %w", err)
	}
	return &v, nil
}
