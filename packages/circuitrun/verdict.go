package circuitrun

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// VerdictInput is a verifier attestation over a persisted CircuitRun.
// Subject and CircuitSpecSHA256 are derived by WORKS and are not caller fields.
type VerdictInput struct {
	CircuitRunID string    `json:"circuit_run_id"`
	Result       string    `json:"result"`
	VerifierID   string    `json:"verifier_id"`
	EvidenceRef  string    `json:"evidence_ref"`
	VerifiedAt   time.Time `json:"verified_at"`
}

// Verdict is the persisted exact-subject verifier attestation.
type Verdict struct {
	CircuitRunID      string    `json:"circuit_run_id"`
	CircuitSpecSHA256 string    `json:"circuit_spec_sha256"`
	Subject           string    `json:"subject"`
	Result            string    `json:"result"`
	VerifierID        string    `json:"verifier_id"`
	EvidenceRef       string    `json:"evidence_ref"`
	VerifiedAt        time.Time `json:"verified_at"`
}

func (in VerdictInput) Validate() error {
	if !strings.HasPrefix(in.CircuitRunID, "crun_") || len(in.CircuitRunID) != len("crun_")+32 {
		return errors.New("circuit_run_id must use crun_<32 hex>")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(in.CircuitRunID, "crun_")); err != nil {
		return errors.New("circuit_run_id suffix must be hex")
	}
	if in.Result != "ACCEPT" && in.Result != "REJECT" {
		return errors.New("result must be ACCEPT or REJECT")
	}
	if strings.TrimSpace(in.VerifierID) == "" {
		return errors.New("verifier_id is required")
	}
	if strings.TrimSpace(in.EvidenceRef) == "" {
		return errors.New("evidence_ref is required")
	}
	if in.VerifiedAt.IsZero() {
		return errors.New("verified_at is required")
	}
	return nil
}

func Subject(run Run) (string, error) {
	if !strings.HasPrefix(run.ID, "crun_") || len(run.ID) != len("crun_")+32 {
		return "", errors.New("circuit run id is invalid")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(run.ID, "crun_")); err != nil {
		return "", errors.New("circuit run id suffix must be hex")
	}
	if len(run.CircuitSpecSHA256) != 64 {
		return "", errors.New("circuit spec sha256 must be 64 hex chars")
	}
	if _, err := hex.DecodeString(run.CircuitSpecSHA256); err != nil {
		return "", errors.New("circuit spec sha256 must be hex")
	}
	return fmt.Sprintf("circuit-run:%s:spec-sha256:%s", run.ID, run.CircuitSpecSHA256), nil
}
