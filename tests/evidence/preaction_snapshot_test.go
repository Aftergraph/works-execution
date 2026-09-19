package evidence_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/services/evidence"
)

func validPreActionInput() evidence.PreActionSnapshotInput {
	return evidence.PreActionSnapshotInput{
		WorkID:             "wrk:0123456789abcdef0123456789abcdef",
		NodeID:             "node-verify",
		AttemptID:          "att:0123456789abcdef0123456789abcdef",
		RunID:              "run-001",
		ExecutionContextID:  "ctx-001",
		ConfidenceThreshold: 0.92,
		VerificationDepth:   4,
		RetryCeiling:        3,
		MinConfidence:       0.86,
		MinVerification:     3,
		MinRetries:          2,
		CapturedAt:          time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
	}
}

func TestPreActionSnapshotCaptureIsDigestBoundAndRoundTrips(t *testing.T) {
	snapshot, err := evidence.CapturePreActionSnapshot(validPreActionInput())
	if err != nil {
		t.Fatalf("CapturePreActionSnapshot: %v", err)
	}
	if snapshot.Digest == "" {
		t.Fatal("snapshot digest missing")
	}
	if !evidence.VerifyPreActionSnapshot(snapshot) {
		t.Fatal("fresh snapshot must verify")
	}

	record, err := snapshot.EvidenceRecord("evd-preaction-1")
	if err != nil {
		t.Fatalf("EvidenceRecord: %v", err)
	}
	if record.Type != evidence.PreActionSnapshotEvidenceType {
		t.Fatalf("record type = %q", record.Type)
	}
	if record.Result != snapshot.Digest {
		t.Fatalf("record result must bind snapshot digest")
	}

	decoded, err := evidence.DecodePreActionSnapshot(record)
	if err != nil {
		t.Fatalf("DecodePreActionSnapshot: %v", err)
	}
	if decoded != snapshot {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", decoded, snapshot)
	}
}

func TestPreActionSnapshotTamperFailsClosed(t *testing.T) {
	snapshot, err := evidence.CapturePreActionSnapshot(validPreActionInput())
	if err != nil {
		t.Fatal(err)
	}

	record, err := snapshot.EvidenceRecord("evd-preaction-2")
	if err != nil {
		t.Fatal(err)
	}
	record.Details["confidence_threshold"] = 0.10

	if _, err := evidence.DecodePreActionSnapshot(record); err == nil {
		t.Fatal("tampered snapshot must fail closed")
	}
}

func TestPreActionSnapshotMissingRequiredFieldFailsClosed(t *testing.T) {
	input := validPreActionInput()
	input.ExecutionContextID = ""
	if _, err := evidence.CapturePreActionSnapshot(input); err == nil {
		t.Fatal("missing execution_context_id must fail closed")
	}

	input = validPreActionInput()
	input.VerificationDepth = -1
	if _, err := evidence.CapturePreActionSnapshot(input); err == nil {
		t.Fatal("negative verification depth must fail closed")
	}
}

func TestPreActionSnapshotConsumerCannotInferMissingFields(t *testing.T) {
	snapshot, err := evidence.CapturePreActionSnapshot(validPreActionInput())
	if err != nil {
		t.Fatal(err)
	}
	record, err := snapshot.EvidenceRecord("evd-preaction-3")
	if err != nil {
		t.Fatal(err)
	}
	delete(record.Details, "retry_ceiling")

	if _, err := evidence.DecodePreActionSnapshot(record); err == nil {
		t.Fatal("consumer must reject incomplete snapshot instead of inferring retry_ceiling")
	}
}

func TestPreActionSnapshotJSONContainsResearchReplayFields(t *testing.T) {
	snapshot, err := evidence.CapturePreActionSnapshot(validPreActionInput())
	if err != nil {
		t.Fatal(err)
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
		"work_id", "node_id", "attempt_id", "run_id", "execution_context_id",
		"confidence_threshold", "verification_depth", "retry_ceiling",
		"min_confidence", "min_verification", "min_retries",
		"captured_at", "digest",
	} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing wire field %q", key)
		}
	}
}
