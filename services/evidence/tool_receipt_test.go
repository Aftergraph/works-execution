package evidence

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validToolReceipt() ToolExecutionReceipt {
	return ToolExecutionReceipt{
		SchemaVersion:      "aftergraph.tool-receipt/v1",
		InvocationID:       "inv-1",
		ToolID:             "relay.system.health",
		Capability:         "EXECUTION_OBSERVE",
		Runtime:            "relay",
		ExecutionContextID: "ctx-1",
		WorkID:             "wrk-1",
		AttemptID:          "att-1",
		EffectID:           "eff-1",
		StartedAt:          time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC),
		FinishedAt:         time.Date(2026, 9, 30, 1, 0, 1, 0, time.UTC),
		Outcome:            "success",
		ReceiptDigest:      strings.Repeat("a", 64),
	}
}

func validToolBinding() ToolReceiptBinding {
	return ToolReceiptBinding{
		ExecutionContextID: "ctx-1",
		WorkID:             "wrk-1",
		AttemptID:          "att-1",
		EffectID:           "eff-1",
	}
}

func TestToolReceiptEvidenceRefPreservesCausalIdentityWithoutSelfVerification(t *testing.T) {
	ref, err := ToolReceiptEvidenceRef(validToolReceipt(), validToolBinding(), time.Time{})
	if err != nil {
		t.Fatalf("ToolReceiptEvidenceRef: %v", err)
	}
	if ref.Type != "tool_execution_receipt" || ref.Result != "observed_success" {
		t.Fatalf("unexpected evidence classification: %+v", ref)
	}
	if ref.AttemptID != "att-1" {
		t.Fatalf("attempt binding lost: %q", ref.AttemptID)
	}
	if got := ref.Details["effect_id"]; got != "eff-1" {
		t.Fatalf("effect binding lost: %v", got)
	}
	if got := ref.Details["independently_verified"]; got != false {
		t.Fatalf("WORKS self-upgraded receipt verification: %v", got)
	}
	if got := ref.Details["credential_material_exposed"]; got != false {
		t.Fatalf("credential invariant lost: %v", got)
	}
}

func TestToolReceiptEvidenceRefRejectsCausalMismatch(t *testing.T) {
	r := validToolReceipt()
	r.EffectID = "eff-other"
	if _, err := ToolReceiptEvidenceRef(r, validToolBinding(), time.Time{}); !errors.Is(err, ErrToolReceiptBindingMismatch) {
		t.Fatalf("got %v, want ErrToolReceiptBindingMismatch", err)
	}
}

func TestToolReceiptEvidenceRefRejectsCredentialExposure(t *testing.T) {
	r := validToolReceipt()
	r.CredentialMaterialExposed = true
	if _, err := ToolReceiptEvidenceRef(r, validToolBinding(), time.Time{}); !errors.Is(err, ErrToolReceiptCredentialLeak) {
		t.Fatalf("got %v, want ErrToolReceiptCredentialLeak", err)
	}
}

func TestToolReceiptEvidenceRefRejectsBadDigestAndOutcome(t *testing.T) {
	r := validToolReceipt()
	r.ReceiptDigest = "not-a-digest"
	if _, err := ToolReceiptEvidenceRef(r, validToolBinding(), time.Time{}); !errors.Is(err, ErrToolReceiptInvalid) {
		t.Fatalf("bad digest: got %v", err)
	}
	r = validToolReceipt()
	r.Outcome = "verified"
	if _, err := ToolReceiptEvidenceRef(r, validToolBinding(), time.Time{}); !errors.Is(err, ErrToolReceiptInvalid) {
		t.Fatalf("bad outcome: got %v", err)
	}
}
