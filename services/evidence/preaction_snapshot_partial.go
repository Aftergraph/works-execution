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

// PreActionSnapshotPartialEvidenceType is the record_kind discriminator for the
// partial pre-action family (Governance #175, Option 2 owner disposition).
//
// It exists because only ONE of the six Headroom research dimensions
// (retry_ceiling) has a canonical in-force Runtime equivalent; the other five
// (confidence_threshold, verification_depth, min_confidence, min_verification,
// min_retries) genuinely do not exist in the execution/dispatch path
// (Aftergraph/runtime#158, docs/PREACTION-FACT-MAPPING-158.md). The full
// primitive requires all six as value types, so wiring honest Runtime output
// into it would coerce the five absent dimensions to 0 — fabricating in-force
// values that #158 forbids inventing.
//
// The partial kind carries ONLY the dimensions with an in-force equivalent
// (today: retry_ceiling) plus the adopted execution-context/1.0 correlation.
// The five absent dimensions are ABSENT FROM THE STRUCT ENTIRELY, so they can
// never marshal as 0. Consumers fail closed on the missing dimensions by record
// *type* (the decoder rejects a foreign record_kind), never by reading a zero.
//
// INVARIANT: this is additive at the family level. It does NOT mutate
// evidence.schema/1.1 (digest f46ba9cd... must never move) and does NOT touch
// the frozen pre_action_snapshot primitive. It rides the same canonical
// type:"policy"/result:"skip" envelope and the same free-form Details map that
// already carries record_kind, so no schema revision is required — which is
// precisely why Option 2 was chosen over Option 1 (nullable fields), per the
// owner disposition on Governance #175.
const PreActionSnapshotPartialEvidenceType = "pre_action_snapshot_partial"

var (
	ErrInvalidPreActionSnapshotPartial  = errors.New("evidence: invalid partial pre-action snapshot")
	ErrPreActionSnapshotPartialTampered = errors.New("evidence: partial pre-action snapshot digest mismatch")
)

// PreActionSnapshotPartialInput is the immutable set of pre-action values that
// have an in-force Runtime source. It is deliberately a SEPARATE struct from
// PreActionSnapshotInput (not a superset/subset): the five no-source dimensions
// are absent by construction, so an honest partial record cannot accidentally
// serialize them as zeros. Identity + correlation fields are required exactly as
// in the full primitive; the execution_context_id / trace_id patterns are shared
// (same package vars in preaction_snapshot.go).
type PreActionSnapshotPartialInput struct {
	WorkID             string `json:"work_id"`
	NodeID             string `json:"node_id"`
	AttemptID          string `json:"attempt_id"`
	RunID              string `json:"run_id"`
	ExecutionContextID string `json:"execution_context_id"`
	TraceID            string `json:"trace_id"`

	// RetryCeiling is the single Headroom dimension with a native Runtime
	// equivalent. It is present; the other five are structurally absent.
	RetryCeiling int `json:"retry_ceiling"`

	CapturedAt time.Time `json:"captured_at"`
}

// PreActionSnapshotPartial is content-addressed. The digest covers every partial
// field plus the record_kind discriminator (see preActionPartialDigest), so the
// partial's integrity is explicitly kind-scoped: a full record cannot be
// replayed as a partial and vice versa even if record_kind were tampered with
// after sealing (the Evidence seal hashes identity+outcome, never Details).
type PreActionSnapshotPartial struct {
	PreActionSnapshotPartialInput
	Digest string `json:"digest"`
}

func validatePreActionPartialInput(in PreActionSnapshotPartialInput) error {
	if in.WorkID == "" || in.NodeID == "" || in.AttemptID == "" ||
		in.RunID == "" || in.ExecutionContextID == "" || in.TraceID == "" {
		return fmt.Errorf("%w: execution identity fields are required", ErrInvalidPreActionSnapshotPartial)
	}
	if !executionContextIDPattern.MatchString(in.ExecutionContextID) {
		return fmt.Errorf("%w: execution_context_id must match execution-context/1.0", ErrInvalidPreActionSnapshotPartial)
	}
	if !traceIDPattern.MatchString(in.TraceID) {
		return fmt.Errorf("%w: trace_id must match execution-context/1.0", ErrInvalidPreActionSnapshotPartial)
	}
	if in.CapturedAt.IsZero() {
		return fmt.Errorf("%w: captured_at is required", ErrInvalidPreActionSnapshotPartial)
	}
	if in.RetryCeiling < 0 {
		return fmt.Errorf("%w: retry_ceiling must be non-negative", ErrInvalidPreActionSnapshotPartial)
	}
	return nil
}

// preActionPartialDigest binds record_kind into the digest preimage. This is a
// deliberate strengthening over the frozen single-kind primitive (whose preimage
// needs no kind binding): because full and partial share the policy/skip envelope
// and the record_kind map key, the kind-scoped digest makes cross-kind replay
// fail closed at the content layer, not only at the decoder's record_kind check.
func preActionPartialDigest(in PreActionSnapshotPartialInput) (string, error) {
	if err := validatePreActionPartialInput(in); err != nil {
		return "", err
	}
	normalized := in
	normalized.CapturedAt = normalized.CapturedAt.UTC()
	preimage := struct {
		RecordKind         string    `json:"record_kind"`
		WorkID             string    `json:"work_id"`
		NodeID             string    `json:"node_id"`
		AttemptID          string    `json:"attempt_id"`
		RunID              string    `json:"run_id"`
		ExecutionContextID string    `json:"execution_context_id"`
		TraceID            string    `json:"trace_id"`
		RetryCeiling       int       `json:"retry_ceiling"`
		CapturedAt         time.Time `json:"captured_at"`
	}{
		RecordKind:         PreActionSnapshotPartialEvidenceType,
		WorkID:             normalized.WorkID,
		NodeID:             normalized.NodeID,
		AttemptID:          normalized.AttemptID,
		RunID:              normalized.RunID,
		ExecutionContextID: normalized.ExecutionContextID,
		TraceID:            normalized.TraceID,
		RetryCeiling:       normalized.RetryCeiling,
		CapturedAt:         normalized.CapturedAt,
	}
	raw, err := json.Marshal(preimage)
	if err != nil {
		return "", fmt.Errorf("evidence: marshal partial pre-action snapshot: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CapturePreActionSnapshotPartial freezes and content-addresses the in-force-only
// pre-action input. It never accepts the five absent dimensions (they are not
// parameters), so no caller can fabricate them here.
func CapturePreActionSnapshotPartial(in PreActionSnapshotPartialInput) (PreActionSnapshotPartial, error) {
	in.CapturedAt = in.CapturedAt.UTC()
	digest, err := preActionPartialDigest(in)
	if err != nil {
		return PreActionSnapshotPartial{}, err
	}
	return PreActionSnapshotPartial{
		PreActionSnapshotPartialInput: in,
		Digest:                        digest,
	}, nil
}

// VerifyPreActionSnapshotPartial recomputes the kind-scoped content digest. Safe
// for research and verifier consumers to fail closed on false.
func VerifyPreActionSnapshotPartial(snapshot PreActionSnapshotPartial) bool {
	if snapshot.Digest == "" {
		return false
	}
	digest, err := preActionPartialDigest(snapshot.PreActionSnapshotPartialInput)
	return err == nil && digest == snapshot.Digest
}

// EvidenceRecord projects the partial snapshot onto the existing WORKS Evidence
// type. Like the full primitive it stays inside the canonical enum as
// policy/skip (informational, never approving); the sealed Details map carries
// record_kind = pre_action_snapshot_partial and the kind-scoped digest. No
// evidence.schema/1.1 mutation is required.
func (snapshot PreActionSnapshotPartial) EvidenceRecord(evidenceID string) (workgraph.Evidence, error) {
	if evidenceID == "" {
		return workgraph.Evidence{}, fmt.Errorf("%w: evidence id is required", ErrInvalidPreActionSnapshotPartial)
	}
	if !VerifyPreActionSnapshotPartial(snapshot) {
		return workgraph.Evidence{}, ErrPreActionSnapshotPartialTampered
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		return workgraph.Evidence{}, fmt.Errorf("evidence: marshal partial pre-action details: %w", err)
	}
	var details map[string]any
	if err := json.Unmarshal(raw, &details); err != nil {
		return workgraph.Evidence{}, fmt.Errorf("evidence: decode partial pre-action details: %w", err)
	}
	details["record_kind"] = PreActionSnapshotPartialEvidenceType

	record := workgraph.Evidence{
		ID:         evidenceID,
		NodeID:     snapshot.NodeID,
		AttemptID:  snapshot.AttemptID,
		Type:       "policy",
		Result:     "skip",
		RecordedAt: snapshot.CapturedAt.UTC(),
		Signer:     "works-evidence",
		Details:    details,
	}
	record.Seal()
	return record, nil
}

// DecodePreActionSnapshotPartial validates a persisted partial pre-action record.
// It rejects any record whose record_kind is not the partial discriminator, so a
// full pre_action_snapshot can never be swallowed as a partial (and, by the
// mirrored check in DecodePreActionSnapshot, a partial can never be swallowed as
// a full). Missing dimensions are never reconstructed: the five absent Headroom
// dimensions have no fields to decode into, so absence stays absence.
func DecodePreActionSnapshotPartial(record workgraph.Evidence) (PreActionSnapshotPartial, error) {
	if record.Type != "policy" || record.Result != "skip" {
		return PreActionSnapshotPartial{}, fmt.Errorf("%w: unexpected evidence type/result %q/%q", ErrInvalidPreActionSnapshotPartial, record.Type, record.Result)
	}
	if record.Details == nil || record.Details["record_kind"] != PreActionSnapshotPartialEvidenceType {
		return PreActionSnapshotPartial{}, fmt.Errorf("%w: missing record_kind or details", ErrInvalidPreActionSnapshotPartial)
	}
	if verdict := workgraph.VerifyEvidence(record); verdict == "tampered" {
		return PreActionSnapshotPartial{}, fmt.Errorf("%w: evidence seal invalid", ErrPreActionSnapshotPartialTampered)
	}

	raw, err := json.Marshal(record.Details)
	if err != nil {
		return PreActionSnapshotPartial{}, fmt.Errorf("evidence: marshal partial pre-action details: %w", err)
	}
	var snapshot PreActionSnapshotPartial
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return PreActionSnapshotPartial{}, fmt.Errorf("%w: decode details: %v", ErrInvalidPreActionSnapshotPartial, err)
	}
	if snapshot.Digest == "" || record.Details["digest"] != snapshot.Digest {
		return PreActionSnapshotPartial{}, ErrPreActionSnapshotPartialTampered
	}
	if !VerifyPreActionSnapshotPartial(snapshot) {
		return PreActionSnapshotPartial{}, ErrPreActionSnapshotPartialTampered
	}
	if snapshot.NodeID != record.NodeID || snapshot.AttemptID != record.AttemptID {
		return PreActionSnapshotPartial{}, fmt.Errorf("%w: evidence identity mismatch", ErrPreActionSnapshotPartialTampered)
	}
	return snapshot, nil
}
