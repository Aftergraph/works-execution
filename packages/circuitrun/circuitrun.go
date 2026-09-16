package circuitrun

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// CanonicalizeSpec normalizes a CircuitSpec JSON object into deterministic
// JSON bytes and returns the SHA-256 digest of those exact bytes.
func CanonicalizeSpec(raw json.RawMessage) ([]byte, string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, "", fmt.Errorf("decode circuit spec: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, "", errors.New("circuit spec must be a JSON object")
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, "", errors.New("circuit spec must contain exactly one valid JSON value")
	}

	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize circuit spec: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}

// Input is the immutable binding request supplied to WORKS persistence.
type Input struct {
	CircuitID   string          `json:"circuit_id"`
	CircuitSpec json.RawMessage `json:"circuit_spec"`
	WorkID      string          `json:"work_id"`
	MissionID   string          `json:"mission_id"`
}

// Run is the persisted exact CircuitSpec-to-Work binding.
type Run struct {
	ID                string          `json:"id"`
	CircuitID         string          `json:"circuit_id"`
	CircuitSpec       json.RawMessage `json:"circuit_spec"`
	CircuitSpecSHA256 string          `json:"circuit_spec_sha256"`
	WorkID            string          `json:"work_id"`
	MissionID         string          `json:"mission_id"`
	CreatedAt         time.Time       `json:"created_at"`
}

func (in Input) Validate() error {
	if strings.TrimSpace(in.CircuitID) == "" {
		return errors.New("circuit_id is required")
	}
	if strings.TrimSpace(in.WorkID) == "" {
		return errors.New("work_id is required")
	}
	if strings.TrimSpace(in.MissionID) == "" {
		return errors.New("mission_id is required")
	}
	if !strings.HasPrefix(in.MissionID, "mis_") {
		return errors.New("mission_id must use mis_ prefix")
	}
	if len(bytes.TrimSpace(in.CircuitSpec)) == 0 {
		return errors.New("circuit_spec is required")
	}
	canonical, _, err := CanonicalizeSpec(in.CircuitSpec)
	if err != nil {
		return err
	}
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
		CircuitID     string `json:"circuit_id"`
	}
	if err := json.Unmarshal(canonical, &envelope); err != nil {
		return fmt.Errorf("decode circuit envelope: %w", err)
	}
	if envelope.SchemaVersion != "circuit-spec/0.1" {
		return errors.New("circuit_spec schema_version must be circuit-spec/0.1")
	}
	if envelope.CircuitID != in.CircuitID {
		return errors.New("circuit_spec circuit_id does not match input")
	}
	return nil
}
