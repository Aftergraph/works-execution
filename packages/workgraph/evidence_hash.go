package workgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// G1 — Evidence hash-kæde: deterministisk SHA-256 over identitet + udfald.
//
// Hash-feltet sættes af Seal() ved evidence-oprettelse (worker/leases/takeover)
// og verificeres af consumers (TG mission-detail, verify-path). Tamper-detektion:
// RecomputedHash() != Hash betyder et felt er ændret efter sealing.
//
// Details er EKSKLUDERET: fri-form metadata må variere uden at bryde hashen —
// hashen dækker identitet (id/node/attempt/signer/environment) + udfald
// (type/result/tidspunkt/artifact). Samme lov som TG audit-kæden: udfaldet
// er forseglet, konteksten kan beriges.

// EvidenceHash computes the deterministic SHA-256 hash over the sealed fields
// of an Evidence record (canonical field order, pipe-delimited).
func (e Evidence) RecomputedHash() string {
	parts := []string{
		e.ID,
		e.NodeID,
		e.AttemptID,
		e.Type,
		e.Result,
		e.RecordedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		e.ArtifactID,
		e.Signer,
		e.Environment,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// EvidenceSeal sets Hash = RecomputedHash() on the evidence record.
// Called centrally at evidence-creation (worker/leases/takeover) so every
// evidence item carries a verifiable integrity hash from birth.
func (e *Evidence) Seal() {
	e.Hash = e.RecomputedHash()
}

// HashValid reports whether the stored Hash matches the recomputed hash.
// An empty stored Hash is treated as unsealed (not broken) — legacy evidence
// predating G1 has no hash; consumers must display "unsealed" honestly.
func (e Evidence) HashValid() (valid, unsealed bool) {
	if e.Hash == "" {
		return false, true
	}
	return e.Hash == e.RecomputedHash(), false
}

// VerifyEvidence is a package-level convenience: returns a human-readable
// verification verdict used by TG consumers ("ok" / "tampered" / "unsealed").
func VerifyEvidence(e Evidence) string {
	valid, unsealed := e.HashValid()
	if unsealed {
		return "unsealed"
	}
	if valid {
		return "ok"
	}
	return "tampered"
}

var _ = fmt.Sprintf // keep fmt import (canonical formatting helper)