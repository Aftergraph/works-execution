package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

const PreActionSnapshotEvidenceType = "pre_action_snapshot"

var (
	ErrInvalidPreActionSnapshot = errors.New("evidence: invalid pre-action snapshot")
	ErrPreActionSnapshotTampered = errors.New("evidence: pre-action snapshot digest mismatch")
)

// PreActionSnapshotInput is the immutable set of policy and context values that
// were in force before an execution attempt. It is deliberately separate from
// outcome evidence so consumers never have to infer pre-action state later.
type PreActionSnapshotInput struct {
	WorkID             string    `json:"work_id"`
	NodeID             string    `json:"node_id"`
	AttemptID          string    `json:"attempt_id"`
	RunID              string    `json:"run_id"`
	ExecutionContextID string    `json:"execution_context_id"`\n\tTraceID            string    `json:"trace_id"`

	ConfidenceThreshold float64 `json:"confidence_threshold"`
	VerificationDepth   int     `json:"verification_depth"`
	RetryCeiling        int     `json:"retry_ceiling"`

	MinConfidence   float64 `json:"min_confidence"`
	MinVerification int     `json:"min_verification"`
	MinRetries      int     `json:"min_retries"`

	CapturedAt time.Time `json:"captured_at"`
}

// PreActionSnapshot is content-addressed. Digest covers every pre-action field
// except Digest itself. The digest is copied into Evidence.Result so the
// existing sealed Evidence hash transitively binds the free-form Details map.
type PreActionSnapshot struct {
	PreActionSnapshotInput
	Digest string `json:"digest"`
}

func validatePreActionInput(in PreActionSnapshotInput) error {
	if in.WorkID == "" || in.NodeID == "" || in.AttemptID == "" ||
		in.RunID == "" || in.ExecutionContextID == "" || in.TraceID == "" {
		return fmt.Errorf("%w: execution identity fields are required", ErrInvalidPreActionSnapshot)
	}
	if in.CapturedAt.IsZero() {
		return fmt.Errorf("%w: captured_at is required", ErrInvalidPreActionSnapshot)
	}
	if in.ConfidenceThreshold < 0 || in.ConfidenceThreshold > 1 ||
		in.MinConfidence < 0 || in.MinConfidence > 1 {
		return fmt.Errorf("%w: confidence values must be in [0,1]", ErrInvalidPreActionSnapshot)
	}
	if in.VerificationDepth < 0 || in.RetryCeiling < 0 ||
		in.MinVerification < 0 || in.MinRetries < 0 {
		return fmt.Errorf("%w: verification and retry values must be non-negative", ErrInvalidPreActionSnapshot)
	}
	return nil
}

func preActionDigest(in PreActionSnapshotInput) (string, error) {
	if err := validatePreActionInput(in); err != nil {
		return "", err
	}
	normalized := in
	normalized.CapturedAt = normalized.CapturedAt.UTC()
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("evidence: marshal pre-action snapshot: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CapturePreActionSnapshot freezes and content-addresses the pre-action input.
func CapturePreActionSnapshot(in PreActionSnapshotInput) (PreActionSnapshot, error) {
	in.CapturedAt = in.CapturedAt.UTC()
	digest, err := preActionDigest(in)
	if err != nil {
		return PreActionSnapshot{}, err
	}
	return PreActionSnapshot{
		PreActionSnapshotInput: in,
		Digest:                 digest,
	}, nil
}

// VerifyPreActionSnapshot checks required fields and recomputes the content
// digest. It is safe for research and verifier consumers to fail closed on
// false.
func VerifyPreActionSnapshot(snapshot PreActionSnapshot) bool {
	if snapshot.Digest == "" {
		return false
	}
	digest, err := preActionDigest(snapshot.PreActionSnapshotInput)
	return err == nil && digest == snapshot.Digest
}

// EvidenceRecord projects the snapshot onto the existing WORKS Evidence type.
// No evidence.schema/1.1 mutation is required: the canonical bundle already
// permits records, while the sealed Result field binds the snapshot digest.
func (snapshot PreActionSnapshot) EvidenceRecord(evidenceID string) (workgraph.Evidence, error) {
	if evidenceID == "" {
		return workgraph.Evidence{}, fmt.Errorf("%w: evidence id is required", ErrInvalidPreActionSnapshot)
	}
	if !VerifyPreActionSnapshot(snapshot) {
		return workgraph.Evidence{}, ErrPreActionSnapshotTampered
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		return workgraph.Evidence{}, fmt.Errorf("evidence: marshal pre-action details: %w", err)
	}
	var details map[string]any
	if err := json.Unmarshal(raw, &details); err != nil {
		return workgraph.Evidence{}, fmt.Errorf("evidence: decode pre-action details: %w", err)
	}

	record := workgraph.Evidence{
		ID:         evidenceID,
		NodeID:     snapshot.NodeID,
		AttemptID:  snapshot.AttemptID,
		Type:       PreActionSnapshotEvidenceType,
		Result:     snapshot.Digest,
		RecordedAt: snapshot.CapturedAt.UTC(),
		Signer:     "works-evidence",
		Details:    details,
	}
	record.Seal()
	return record, nil
}

// DecodePreActionSnapshot validates a persisted WORKS evidence record. Missing
// fields are rejected; consumers must never reconstruct them from outcomes.
func DecodePreActionSnapshot(record workgraph.Evidence) (PreActionSnapshot, error) {
	if record.Type != PreActionSnapshotEvidenceType {
		return PreActionSnapshot{}, fmt.Errorf("%w: unexpected evidence type %q", ErrInvalidPreActionSnapshot, record.Type)
	}
	if record.Result == "" || record.Details == nil {
		return PreActionSnapshot{}, fmt.Errorf("%w: missing digest or details", ErrInvalidPreActionSnapshot)
	}
	if verdict := workgraph.VerifyEvidence(record); verdict == "tampered" {
		return PreActionSnapshot{}, fmt.Errorf("%w: evidence seal invalid", ErrPreActionSnapshotTampered)
	}

	raw, err := json.Marshal(record.Details)
	if err != nil {
		return PreActionSnapshot{}, fmt.Errorf("evidence: marshal pre-action details: %w", err)
	}
	var snapshot PreActionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return PreActionSnapshot{}, fmt.Errorf("%w: decode details: %v", ErrInvalidPreActionSnapshot, err)
	}
	if snapshot.Digest == "" || snapshot.Digest != record.Result {
		return PreActionSnapshot{}, ErrPreActionSnapshotTampered
	}
	if !VerifyPreActionSnapshot(snapshot) {
		return PreActionSnapshot{}, ErrPreActionSnapshotTampered
	}
	if snapshot.NodeID != record.NodeID || snapshot.AttemptID != record.AttemptID {
		return PreActionSnapshot{}, fmt.Errorf("%w: evidence identity mismatch", ErrPreActionSnapshotTampered)
	}
	return snapshot, nil
}
