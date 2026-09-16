package circuitrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EffectState string

const (
	EffectDispatched    EffectState = "DISPATCHED"
	EffectApplied       EffectState = "APPLIED"
	EffectFailed        EffectState = "FAILED"
	EffectCompensated   EffectState = "COMPENSATED"
	EffectIndeterminate EffectState = "INDETERMINATE"
)

type EffectReceiptInput struct {
	EffectBindingID string      `json:"effect_binding_id"`
	State           EffectState `json:"state"`
	ExecutorID      string      `json:"executor_id"`
	EvidenceRef     string      `json:"evidence_ref"`
	RecordedAt      time.Time   `json:"recorded_at"`
}

type EffectReceipt struct {
	ID                  string      `json:"id"`
	EffectBindingID     string      `json:"effect_binding_id"`
	CircuitRunID        string      `json:"circuit_run_id"`
	EffectID            string      `json:"effect_id"`
	VerificationSubject string      `json:"verification_subject"`
	DispatchSHA256      string      `json:"dispatch_sha256"`
	Sequence            int         `json:"sequence"`
	State               EffectState `json:"state"`
	ExecutorID          string      `json:"executor_id"`
	EvidenceRef         string      `json:"evidence_ref"`
	RecordedAt          time.Time   `json:"recorded_at"`
	ReceiptSHA256       string      `json:"receipt_sha256"`
}

func CanTransitionEffect(from, to EffectState) bool {
	switch from {
	case EffectDispatched:
		return to == EffectApplied || to == EffectFailed || to == EffectIndeterminate
	case EffectApplied:
		return to == EffectCompensated
	case EffectIndeterminate:
		return to == EffectApplied || to == EffectFailed
	default:
		return false
	}
}

func NewEffectReceipt(binding EffectBinding, sequence int, in EffectReceiptInput) (EffectReceipt, error) {
	if strings.TrimSpace(binding.ID) == "" || strings.TrimSpace(binding.CircuitRunID) == "" ||
		strings.TrimSpace(binding.EffectID) == "" || strings.TrimSpace(binding.VerificationSubject) == "" ||
		strings.TrimSpace(binding.DispatchSHA256) == "" || sequence <= 0 || in.RecordedAt.IsZero() {
		return EffectReceipt{}, errors.New("invalid effect receipt binding")
	}
	if in.EffectBindingID != binding.ID {
		return EffectReceipt{}, errors.New("effect receipt binding mismatch")
	}
	if in.State != EffectDispatched && (strings.TrimSpace(in.ExecutorID) == "" || strings.TrimSpace(in.EvidenceRef) == "") {
		return EffectReceipt{}, errors.New("effect outcome requires executor and evidence")
	}
	payload := struct {
		EffectBindingID, CircuitRunID, EffectID, VerificationSubject, DispatchSHA256 string
		Sequence                                                                     int
		State                                                                        EffectState
		ExecutorID, EvidenceRef, RecordedAt                                          string
	}{binding.ID, binding.CircuitRunID, binding.EffectID, binding.VerificationSubject, binding.DispatchSHA256,
		sequence, in.State, in.ExecutorID, in.EvidenceRef, in.RecordedAt.UTC().Format(time.RFC3339Nano)}
	b, err := json.Marshal(payload)
	if err != nil {
		return EffectReceipt{}, err
	}
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	return EffectReceipt{ID: fmt.Sprintf("ercpt_%s", digest[:32]), EffectBindingID: binding.ID,
		CircuitRunID: binding.CircuitRunID, EffectID: binding.EffectID, VerificationSubject: binding.VerificationSubject,
		DispatchSHA256: binding.DispatchSHA256, Sequence: sequence, State: in.State, ExecutorID: in.ExecutorID,
		EvidenceRef: in.EvidenceRef, RecordedAt: in.RecordedAt.UTC(), ReceiptSHA256: digest}, nil
}
