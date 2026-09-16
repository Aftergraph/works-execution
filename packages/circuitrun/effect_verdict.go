package circuitrun

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EffectVerdictInput struct {
	EffectReceiptID string    `json:"effect_receipt_id"`
	Result          string    `json:"result"`
	VerifierID      string    `json:"verifier_id"`
	EvidenceRef     string    `json:"evidence_ref"`
	VerifiedAt      time.Time `json:"verified_at"`
}

type EffectVerdict struct {
	EffectReceiptID     string    `json:"effect_receipt_id"`
	ReceiptSHA256       string    `json:"receipt_sha256"`
	VerificationSubject string    `json:"verification_subject"`
	Subject             string    `json:"subject"`
	Result              string    `json:"result"`
	VerifierID          string    `json:"verifier_id"`
	EvidenceRef         string    `json:"evidence_ref"`
	VerifiedAt          time.Time `json:"verified_at"`
}

func (in EffectVerdictInput) ValidateAgainstExecutor(executorID string) error {
	if !strings.HasPrefix(in.EffectReceiptID, "ercpt_") || len(in.EffectReceiptID) != len("ercpt_")+32 {
		return errors.New("effect_receipt_id must use ercpt_<32 hex>")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(in.EffectReceiptID, "ercpt_")); err != nil {
		return errors.New("effect_receipt_id suffix must be hex")
	}
	if in.Result != "ACCEPT" && in.Result != "REJECT" && in.Result != "INDETERMINATE" {
		return errors.New("result must be ACCEPT, REJECT or INDETERMINATE")
	}
	if strings.TrimSpace(in.VerifierID) == "" || strings.TrimSpace(in.EvidenceRef) == "" || in.VerifiedAt.IsZero() {
		return errors.New("verifier, evidence and verified_at are required")
	}
	if strings.TrimSpace(executorID) != "" && in.VerifierID == executorID {
		return errors.New("verifier must be independent from executor")
	}
	return nil
}

func EffectReceiptSubject(receipt EffectReceipt) (string, error) {
	if !strings.HasPrefix(receipt.ID, "ercpt_") || len(receipt.ID) != len("ercpt_")+32 {
		return "", errors.New("effect receipt id invalid")
	}
	if len(receipt.ReceiptSHA256) != 64 {
		return "", errors.New("effect receipt sha256 invalid")
	}
	if _, err := hex.DecodeString(receipt.ReceiptSHA256); err != nil {
		return "", errors.New("effect receipt sha256 invalid")
	}
	return fmt.Sprintf("effect-receipt:%s:sha256:%s", receipt.ID, receipt.ReceiptSHA256), nil
}
