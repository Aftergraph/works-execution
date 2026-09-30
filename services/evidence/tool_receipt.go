package evidence

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

var toolReceiptDigestPattern = regexp.MustCompile("^[a-f0-9]{64}$")

var (
	ErrToolReceiptInvalid          = errors.New("tool receipt invalid")
	ErrToolReceiptBindingMismatch  = errors.New("tool receipt causal binding mismatch")
	ErrToolReceiptCredentialLeak   = errors.New("tool receipt reports credential material exposure")
)

// ToolExecutionReceipt is the minimum ToolFabric receipt projection WORKS needs
// to correlate execution evidence. WORKS does not independently verify this
// receipt here; independent receipt verification remains a verifier concern.
type ToolExecutionReceipt struct {
	SchemaVersion              string
	InvocationID               string
	ToolID                     string
	Capability                 string
	Runtime                    string
	ExecutionContextID         string
	WorkID                     string
	AttemptID                  string
	EffectID                   string
	StartedAt                  time.Time
	FinishedAt                 time.Time
	Outcome                    string
	ReceiptDigest              string
	CredentialMaterialExposed  bool
}

// ToolReceiptBinding is the durable causal identity WORKS expects.
type ToolReceiptBinding struct {
	ExecutionContextID string
	WorkID              string
	AttemptID           string
	EffectID            string
}

// ToolReceiptEvidenceRef converts a structurally valid, causally-bound
// ToolExecutionReceipt into a normal evidence reference. Result is deliberately
// "observed_<outcome>", not "verified": execution evidence cannot self-upgrade.
func ToolReceiptEvidenceRef(receipt ToolExecutionReceipt, expected ToolReceiptBinding, recordedAt time.Time) (EvidenceRef, error) {
	if receipt.SchemaVersion != "aftergraph.tool-receipt/v1" ||
		receipt.InvocationID == "" ||
		receipt.ToolID == "" ||
		receipt.Capability == "" ||
		receipt.Runtime == "" ||
		!toolReceiptDigestPattern.MatchString(receipt.ReceiptDigest) {
		return EvidenceRef{}, ErrToolReceiptInvalid
	}
	if receipt.CredentialMaterialExposed {
		return EvidenceRef{}, ErrToolReceiptCredentialLeak
	}
	if receipt.ExecutionContextID != expected.ExecutionContextID ||
		receipt.WorkID != expected.WorkID ||
		receipt.AttemptID != expected.AttemptID ||
		receipt.EffectID != expected.EffectID {
		return EvidenceRef{}, ErrToolReceiptBindingMismatch
	}
	switch receipt.Outcome {
	case "success", "denied", "error":
	default:
		return EvidenceRef{}, fmt.Errorf("%w: unsupported outcome %q", ErrToolReceiptInvalid, receipt.Outcome)
	}
	if recordedAt.IsZero() {
		recordedAt = receipt.FinishedAt
	}
	return EvidenceRef{
		ID:         "tool_receipt:" + receipt.ReceiptDigest,
		AttemptID:  receipt.AttemptID,
		Type:       "tool_execution_receipt",
		Result:     "observed_" + receipt.Outcome,
		RecordedAt: recordedAt.UTC(),
		Details: map[string]any{
			"schema_version":       receipt.SchemaVersion,
			"invocation_id":        receipt.InvocationID,
			"tool_id":              receipt.ToolID,
			"capability":           receipt.Capability,
			"runtime":              receipt.Runtime,
			"execution_context_id": receipt.ExecutionContextID,
			"work_id":              receipt.WorkID,
			"attempt_id":           receipt.AttemptID,
			"effect_id":            receipt.EffectID,
			"receipt_digest":       receipt.ReceiptDigest,
			"credential_material_exposed": false,
			"independently_verified":      false,
		},
	}, nil
}
