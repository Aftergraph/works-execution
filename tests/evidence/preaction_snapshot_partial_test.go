package evidence_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/services/evidence"
)

// The absent Headroom dimensions: no in-force Runtime source (runtime#158).
// A partial record must NEVER serialize these as 0 — they are structurally gone.
var absentHeadroomDimensions = []string{
	"confidence_threshold",
	"verification_depth",
	"min_confidence",
	"min_verification",
	"min_retries",
}

func validPartialInput() evidence.PreActionSnapshotPartialInput {
	return evidence.PreActionSnapshotPartialInput{
		WorkID:             "wrk_0123456789abcdef0123456789abcdef",
		NodeID:             "node-verify",
		AttemptID:          "att_0123456789abcdef0123456789abcdef",
		RunID:              "run-001",
		ExecutionContextID: "ctx_11111111111111111111111111111111",
		TraceID:            "trc_22222222222222222222222222222222",
		RetryCeiling:       3,
		CapturedAt:         time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
	}
}

// Core enactment of Governance #175 Option 2: the partial wire shape carries the
// in-force dimension (retry_ceiling) + adopted correlation, and the five
// no-source dimensions are ABSENT — not zeroed.
func TestPreActionSnapshotPartialCarriesOnlyInForceDimensions(t *testing.T) {
	snapshot, err := evidence.CapturePreActionSnapshotPartial(validPartialInput())
	if err != nil {
		t.Fatalf("CapturePreActionSnapshotPartial: %v", err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{
		"work_id", "node_id", "attempt_id", "run_id",
		"execution_context_id", "trace_id", "retry_ceiling", "captured_at", "digest",
	} {
		if _, ok := got[key]; !ok {
			t.Fatalf("partial wire missing in-force field %q", key)
		}
	}
	for _, key := range absentHeadroomDimensions {
		if v, ok := got[key]; ok {
			t.Fatalf("partial wire must NOT carry absent dimension %q (found %v); absence must never be coerced to 0", key, v)
		}
	}
}

// retry_ceiling = 0 is a legitimate in-force value; it must not drag the absent
// dimensions back onto the wire as zeros.
func TestPreActionSnapshotPartialZeroRetryCeilingStillOmitsAbsentDims(t *testing.T) {
	input := validPartialInput()
	input.RetryCeiling = 0
	snapshot, err := evidence.CapturePreActionSnapshotPartial(input)
	if err != nil {
		t.Fatalf("zero retry_ceiling is valid: %v", err)
	}
	raw, _ := json.Marshal(snapshot)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if v, ok := got["retry_ceiling"]; !ok || v != float64(0) {
		t.Fatalf("retry_ceiling=0 must be present as 0, got %v (present=%v)", v, ok)
	}
	for _, key := range absentHeadroomDimensions {
		if _, ok := got[key]; ok {
			t.Fatalf("absent dimension %q leaked onto the wire", key)
		}
	}
}

func TestPreActionSnapshotPartialRoundTrips(t *testing.T) {
	snapshot, err := evidence.CapturePreActionSnapshotPartial(validPartialInput())
	if err != nil {
		t.Fatalf("CapturePreActionSnapshotPartial: %v", err)
	}
	if snapshot.Digest == "" {
		t.Fatal("partial digest missing")
	}
	if !evidence.VerifyPreActionSnapshotPartial(snapshot) {
		t.Fatal("fresh partial must verify")
	}

	record, err := snapshot.EvidenceRecord("evd-preaction-partial-1")
	if err != nil {
		t.Fatalf("EvidenceRecord: %v", err)
	}
	if got := record.Details["record_kind"]; got != evidence.PreActionSnapshotPartialEvidenceType {
		t.Fatalf("record_kind = %v; want %q", got, evidence.PreActionSnapshotPartialEvidenceType)
	}
	decoded, err := evidence.DecodePreActionSnapshotPartial(record)
	if err != nil {
		t.Fatalf("DecodePreActionSnapshotPartial: %v", err)
	}
	if decoded != snapshot {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", decoded, snapshot)
	}
}

// The partial must stay informational: policy/skip, never an approving pass.
func TestPreActionSnapshotPartialRecordStaysPolicySkip(t *testing.T) {
	snapshot, err := evidence.CapturePreActionSnapshotPartial(validPartialInput())
	if err != nil {
		t.Fatal(err)
	}
	record, err := snapshot.EvidenceRecord("evd-preaction-partial-2")
	if err != nil {
		t.Fatal(err)
	}
	if record.Type != "policy" || record.Result != "skip" {
		t.Fatalf("partial wire must stay policy/skip, got %s/%s", record.Type, record.Result)
	}
	if record.Result == "pass" {
		t.Fatal("informational partial snapshot must not satisfy pass-based approval")
	}
}

func TestPreActionSnapshotPartialTamperFailsClosed(t *testing.T) {
	snapshot, err := evidence.CapturePreActionSnapshotPartial(validPartialInput())
	if err != nil {
		t.Fatal(err)
	}
	record, err := snapshot.EvidenceRecord("evd-preaction-partial-3")
	if err != nil {
		t.Fatal(err)
	}
	record.Details["retry_ceiling"] = float64(99)
	if _, err := evidence.DecodePreActionSnapshotPartial(record); err == nil {
		t.Fatal("tampered retry_ceiling must fail closed on the kind-scoped digest")
	}
}

// Mutual exclusivity, naive: a genuine full record is rejected by the partial
// decoder, and a genuine partial record is rejected by the full decoder, purely
// on the record_kind check.
func TestPreActionSnapshotPartialForeignKindRejectedByBothDecoders(t *testing.T) {
	fullSnap, err := evidence.CapturePreActionSnapshot(validPreActionInput())
	if err != nil {
		t.Fatal(err)
	}
	fullRec, err := fullSnap.EvidenceRecord("evd-full-foreign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.DecodePreActionSnapshotPartial(fullRec); err == nil {
		t.Fatal("partial decoder must reject a full pre_action_snapshot record")
	}

	partialSnap, err := evidence.CapturePreActionSnapshotPartial(validPartialInput())
	if err != nil {
		t.Fatal(err)
	}
	partialRec, err := partialSnap.EvidenceRecord("evd-partial-foreign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.DecodePreActionSnapshot(partialRec); err == nil {
		t.Fatal("full decoder must reject a pre_action_snapshot_partial record")
	}
}

// Mutual exclusivity, adversarial: relabeling record_kind AFTER sealing must
// still fail closed. The Evidence seal covers identity+outcome only (never
// Details), so the record_kind check alone is insufficient — the kind-scoped
// digest is the layer that catches a relabeled replay in either direction.
func TestPreActionSnapshotPartialCrossKindReplayRejected(t *testing.T) {
	// Full record relabeled as partial: the five absent dims are present and the
	// stored digest is the FULL digest, so the partial kind-scoped recompute
	// differs -> reject.
	fullSnap, err := evidence.CapturePreActionSnapshot(validPreActionInput())
	if err != nil {
		t.Fatal(err)
	}
	fullRec, err := fullSnap.EvidenceRecord("evd-full-relabel")
	if err != nil {
		t.Fatal(err)
	}
	fullRec.Details["record_kind"] = evidence.PreActionSnapshotPartialEvidenceType
	if _, err := evidence.DecodePreActionSnapshotPartial(fullRec); err == nil {
		t.Fatal("a full record relabeled as partial must fail the kind-scoped digest")
	}

	// Partial record relabeled as full: the five dims decode to zero-values and
	// the stored digest is the PARTIAL digest, so the full recompute differs ->
	// reject. This is the fabrication trap #175 closes, defended in depth.
	partialSnap, err := evidence.CapturePreActionSnapshotPartial(validPartialInput())
	if err != nil {
		t.Fatal(err)
	}
	partialRec, err := partialSnap.EvidenceRecord("evd-partial-relabel")
	if err != nil {
		t.Fatal(err)
	}
	partialRec.Details["record_kind"] = evidence.PreActionSnapshotEvidenceType
	if _, err := evidence.DecodePreActionSnapshot(partialRec); err == nil {
		t.Fatal("a partial record relabeled as full must fail the full digest")
	}
}

func TestPreActionSnapshotPartialMissingCorrelationFailsClosed(t *testing.T) {
	input := validPartialInput()
	input.ExecutionContextID = ""
	if _, err := evidence.CapturePreActionSnapshotPartial(input); err == nil {
		t.Fatal("missing execution_context_id must fail closed")
	}

	input = validPartialInput()
	input.TraceID = ""
	if _, err := evidence.CapturePreActionSnapshotPartial(input); err == nil {
		t.Fatal("missing trace_id must fail closed")
	}

	input = validPartialInput()
	input.ExecutionContextID = "ctx-not-canonical"
	if _, err := evidence.CapturePreActionSnapshotPartial(input); err == nil {
		t.Fatal("non-canonical execution_context_id must fail closed")
	}
}

func TestPreActionSnapshotPartialNegativeRetryCeilingFailsClosed(t *testing.T) {
	input := validPartialInput()
	input.RetryCeiling = -1
	if _, err := evidence.CapturePreActionSnapshotPartial(input); err == nil {
		t.Fatal("negative retry_ceiling must fail closed")
	}
}

// The partial must not be reconstructible into a full record: a consumer cannot
// invent the five absent dimensions from a partial. This is the #158 law
// ("never invent values") enforced at the type boundary.
func TestPreActionSnapshotPartialCannotBePromotedToFull(t *testing.T) {
	partialSnap, err := evidence.CapturePreActionSnapshotPartial(validPartialInput())
	if err != nil {
		t.Fatal(err)
	}
	partialRec, err := partialSnap.EvidenceRecord("evd-partial-promote")
	if err != nil {
		t.Fatal(err)
	}
	// Even if a consumer copies the in-force fields into a full input and stamps
	// the full kind, the digest will not match a genuine full capture of the
	// same identity unless the five absent dims are invented -> so promotion is
	// never silent. Decode of the partial record by the full decoder rejects.
	if _, err := evidence.DecodePreActionSnapshot(partialRec); err == nil {
		t.Fatal("a partial record must never decode as a full pre_action_snapshot")
	}
}
