package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// workspaceIdentity separates caller retry identity from immutable creation
// intent. The key hash groups all replays of one idempotency key; the spec
// fingerprint distinguishes conflicting reuse of that key after process
// restart without requiring provider-local durable state.
func workspaceIdentity(spec Spec) (keyHash, specHash string, err error) {
	if err := spec.Validate(); err != nil {
		return "", "", err
	}

	keyDigest := sha256.Sum256([]byte(spec.IdempotencyKey))

	type creationIntent struct {
		WorkID   string            `json:"work_id"`
		Org      string            `json:"org"`
		Name     string            `json:"name"`
		Baseline SourceRef         `json:"baseline"`
		Mode     Mode              `json:"mode"`
		Labels   map[string]string `json:"labels,omitempty"`
		TTL      int64             `json:"ttl_ns,omitempty"`
	}
	intent := creationIntent{
		WorkID: spec.WorkID,
		Org: spec.Org,
		Name: spec.Name,
		Baseline: spec.Baseline,
		Mode: spec.Mode,
		Labels: spec.Labels,
		TTL: int64(spec.TTL),
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return "", "", err
	}
	specDigest := sha256.Sum256(raw)

	// 96 bits each keeps provider identifiers compact while making accidental
	// collisions negligible. Providers still compare the complete derived
	// names under the same key prefix and fail closed on any mismatch.
	return hex.EncodeToString(keyDigest[:12]), hex.EncodeToString(specDigest[:12]), nil
}
