package circuitrun

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// EffectBindingInput names only pre-existing durable identities.
type EffectBindingInput struct {
	CircuitRunID     string `json:"circuit_run_id"`
	WorksExecutionID string `json:"works_execution_id"`
}

// EffectBinding is the WORKS-owned immutable CircuitRun→dispatch effect seam.
type EffectBinding struct {
	ID                  string    `json:"id"`
	CircuitRunID        string    `json:"circuit_run_id"`
	WorkID              string    `json:"work_id"`
	MissionID           string    `json:"mission_id"`
	WorksExecutionID    string    `json:"works_execution_id"`
	RuntimeDispatchID   string    `json:"runtime_dispatch_id"`
	DispatchAttemptID   string    `json:"dispatch_attempt_id"`
	EffectID            string    `json:"effect_id"`
	VerificationSubject string    `json:"verification_subject"`
	CausalID            string    `json:"causal_id"`
	DispatchSHA256      string    `json:"dispatch_sha256"`
	BoundAt             time.Time `json:"bound_at"`
}

func (in EffectBindingInput) Validate() error {
	if !strings.HasPrefix(in.CircuitRunID, "crun_") || len(in.CircuitRunID) != len("crun_")+32 {
		return errors.New("circuit_run_id must use crun_<32 hex>")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(in.CircuitRunID, "crun_")); err != nil {
		return errors.New("circuit_run_id suffix must be hex")
	}
	if strings.TrimSpace(in.WorksExecutionID) == "" {
		return errors.New("works_execution_id is required")
	}
	return nil
}
