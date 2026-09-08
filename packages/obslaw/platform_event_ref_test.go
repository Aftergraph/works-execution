package obslaw

import (
	"encoding/json"
	"strings"
	"testing"
)

func testCorrelation() PlatformCorrelation {
	return PlatformCorrelation{
		ExecutionContextID:  "ctx_11111111111111111111111111111111",
		TenantID:            "ten_11111111111111111111111111111111",
		PrincipalID:         "prn_11111111111111111111111111111111",
		MissionID:           "mission-platform-fabric-001",
		AuthorityLeaseID:    "auth_11111111111111111111111111111111",
		WorkID:              "wrk_11111111111111111111111111111111",
		AdmissionDecisionID: "pdr_11111111111111111111111111111111",
		TraceID:             "trc_11111111111111111111111111111111",
		ActionID:            "act_11111111111111111111111111111111",
	}
}

func testWorkEvent() WorkEvent {
	return WorkEvent{
		Source:     "works-org",
		Seq:        7,
		Type:       "work.state",
		Subject:    "work:00",
		PayloadRef: "artifact:work-state-7",
		Timestamp:  "2026-09-08T12:00:00Z",
		Version:    "1.0",
	}
}

func TestProjectPlatformEventRef(t *testing.T) {
	got, err := ProjectPlatformEventRef(testWorkEvent(), testCorrelation())
	if err != nil {
		t.Fatalf("ProjectPlatformEventRef: %v", err)
	}
	if got.Schema != "platform-event-ref/0.1" || got.Source != "works-execution" {
		t.Fatalf("unexpected projection identity: %+v", got)
	}
	if got.EventType != "work.state" || got.SubjectRef != "work:00" {
		t.Fatalf("native semantics drifted: %+v", got)
	}
	if got.Classification != "execution" {
		t.Fatalf("classification = %q, want execution", got.Classification)
	}
	if !strings.HasPrefix(got.EventID, "evt_") || len(got.EventID) != 36 {
		t.Fatalf("event id = %q", got.EventID)
	}
	if !strings.HasPrefix(got.IntegrityRef, "sha256:") || len(got.IntegrityRef) != 71 {
		t.Fatalf("integrity ref = %q", got.IntegrityRef)
	}
	if got.Correlation != testCorrelation() {
		t.Fatalf("correlation drifted: %+v", got.Correlation)
	}
}

func TestPlatformEventRefDeterministic(t *testing.T) {
	first, err := ProjectPlatformEventRef(testWorkEvent(), testCorrelation())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProjectPlatformEventRef(testWorkEvent(), testCorrelation())
	if err != nil {
		t.Fatal(err)
	}
	if first.EventID != second.EventID || first.IntegrityRef != second.IntegrityRef {
		t.Fatalf("projection must be deterministic: first=%+v second=%+v", first, second)
	}
}

func TestPlatformEventRefRequiresCausalActionIdentity(t *testing.T) {
	correlation := testCorrelation()
	correlation.ActionID = ""
	if _, err := ProjectPlatformEventRef(testWorkEvent(), correlation); err == nil || !strings.Contains(err.Error(), "action_id") {
		t.Fatalf("expected action_id rejection, got %v", err)
	}
}

func TestPlatformEventRefRejectsMalformedCanonicalIdentity(t *testing.T) {
	correlation := testCorrelation()
	correlation.TenantID = "tenant-a"
	if _, err := ProjectPlatformEventRef(testWorkEvent(), correlation); err == nil || !strings.Contains(err.Error(), "tenant_id") {
		t.Fatalf("expected canonical tenant rejection, got %v", err)
	}
}

func TestPlatformEventRefRejectsInvalidNativeEvent(t *testing.T) {
	event := testWorkEvent()
	event.Source = "invented-source"
	if _, err := ProjectPlatformEventRef(event, testCorrelation()); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("expected source rejection, got %v", err)
	}
}

func TestPlatformEventRefDoesNotUpgradeObservabilityIntoEvidenceOrAuthority(t *testing.T) {
	got, err := ProjectPlatformEventRef(testWorkEvent(), testCorrelation())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{`"authority"`, `"authority_grant"`, `"evidence"`, `"verified"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("projection contains forbidden semantic upgrade %s: %s", forbidden, text)
		}
	}
}

func TestPlatformEventRefFallbackPayloadReference(t *testing.T) {
	event := testWorkEvent()
	event.PayloadRef = ""
	got, err := ProjectPlatformEventRef(event, testCorrelation())
	if err != nil {
		t.Fatal(err)
	}
	if got.PayloadRef != "works:event:works-org:7" {
		t.Fatalf("payload ref = %q", got.PayloadRef)
	}
}
