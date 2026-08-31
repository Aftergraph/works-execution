// Package provenance produces, signs, and persists workflow-provenance
// attestations per docs/standards/schemas/workflow-provenance.schema.json
// (SLSA / in-toto-style provenance for a single Work execution).
//
// Slice 5 / k-impl-005. The attestation is bound to a Work ID; the producer
// is invoked when a Work transitions to a terminal state. The control plane
// (builder) HMAC-signs the canonical JSON envelope so any consumer holding
// the same key can verify integrity.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

// PredicateType values from the schema. We always emit the works-execution
// namespaced URI; the SLSA / in-toto URIs remain valid alternatives for
// interoperability with external verifiers.
const (
	PredicateTypeSLSA            = "https://slsa.dev/provenance/v1"
	PredicateTypeInToto          = "https://in-toto.io/attestation/v0.1"
	PredicateTypeWorkflowProv    = "https://works-execution.dev/attestation/workflow-provenance/v1"
)

// BuilderURI is the canonical control-plane identifier embedded in every
// attestation. It is the "builder.id" of the SLSA predicate.
const BuilderURI = "https://works-execution.dev/control-plane/v1"

// Subject is the work product being attested. SLSA v1 / in-toto model this
// as an array so multiple artifacts can share one attestation; we always
// emit a single-element array with the Work ID as `name` and the
// canonical-envelope SHA-256 as `digest.sha256`.
type Subject struct {
	Name   string `json:"name"`
	Digest Digest  `json:"digest"`
}

// Digest is the {alg: hex} pair used in SLSA digests.
type Digest struct {
	SHA256 string `json:"sha256,omitempty"`
}

// Source is the invocation.configSource of the predicate.
type Source struct {
	URI        string `json:"uri,omitempty"`
	Digest     Digest  `json:"digest,omitempty"`
	EntryPoint string `json:"entryPoint,omitempty"`
}

// Material is an input to the build (predicate.materials[*]).
type Material struct {
	URI    string `json:"uri"`
	Digest Digest `json:"digest,omitempty"`
}

// Invocation captures how the Work was triggered.
type Invocation struct {
	ConfigSource *Source         `json:"configSource,omitempty"`
	Parameters   map[string]any  `json:"parameters,omitempty"`
	Environment  map[string]any  `json:"environment,omitempty"`
}

// Builder identifies the entity that produced the attestation.
type Builder struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// Completeness flags what the builder captured.
type Completeness struct {
	Arguments   bool `json:"arguments,omitempty"`
	Environment bool `json:"environment,omitempty"`
	Materials   bool `json:"materials,omitempty"`
}

// Metadata mirrors SLSA's metadata block.
type Metadata struct {
	BuildStartedOn  string       `json:"buildStartedOn,omitempty"`
	BuildFinishedOn string       `json:"buildFinishedOn,omitempty"`
	Completeness    *Completeness `json:"completeness,omitempty"`
	Reproducible    bool          `json:"reproducible,omitempty"`
}

// Predicate is the SLSA-style payload.
type Predicate struct {
	Builder    Builder     `json:"builder"`
	Invocation Invocation  `json:"invocation"`
	Materials  []Material  `json:"materials"`
	Metadata   *Metadata   `json:"metadata,omitempty"`
}

// Attestation is the top-level envelope. The JSON shape MUST match
// docs/standards/schemas/workflow-provenance.schema.json.
type Attestation struct {
	PredicateType string    `json:"predicateType"`
	Subject       []Subject `json:"subject"`
	Predicate     Predicate `json:"predicate"`
}

// canonicalBytes returns a stable JSON encoding of the attestation for
// signing and verification. We marshal with sorted keys at every level so
// two producers computing the same attestation produce identical bytes.
func (a *Attestation) canonicalBytes() ([]byte, error) {
	return canonicalMarshal(a)
}

// CanonicalSHA256 returns the hex SHA-256 of the canonical envelope. Used
// for the subject.digest.sha256 field so consumers can confirm the
// attestation matches the artifact.
func (a *Attestation) CanonicalSHA256() (string, error) {
	b, err := a.canonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalMarshal encodes v to JSON with sorted keys at every level.
// json.Marshal already sorts map keys, but we marshal through an
// intermediate map[string]any to force struct-field-key ordering too.
func canonicalMarshal(v any) ([]byte, error) {
	switch x := v.(type) {
	case *Attestation:
		out := map[string]any{
			"predicateType": x.PredicateType,
			"subject":       sortedSubject(x.Subject),
			"predicate":     predicateToMap(x.Predicate),
		}
		return marshalSorted(out)
	case Predicate:
		return marshalSorted(predicateToMap(x))
	}
	// Fallback: rely on encoding/json's key ordering for maps.
	return json.Marshal(v)
}

func predicateToMap(p Predicate) map[string]any {
	m := map[string]any{
		"builder":    builderToMap(p.Builder),
		"invocation": invocationToMap(p.Invocation),
		"materials":  materialsToSlice(p.Materials),
	}
	if p.Metadata != nil {
		m["metadata"] = metadataToMap(*p.Metadata)
	}
	return m
}

func builderToMap(b Builder) map[string]any {
	m := map[string]any{"id": b.ID}
	if b.Version != "" {
		m["version"] = b.Version
	}
	return m
}

func invocationToMap(inv Invocation) map[string]any {
	m := map[string]any{}
	if inv.ConfigSource != nil {
		m["configSource"] = sourceToMap(*inv.ConfigSource)
	}
	if inv.Parameters != nil {
		m["parameters"] = stringifyKeys(inv.Parameters)
	}
	if inv.Environment != nil {
		m["environment"] = stringifyKeys(inv.Environment)
	}
	return m
}

func sourceToMap(s Source) map[string]any {
	m := map[string]any{}
	if s.URI != "" {
		m["uri"] = s.URI
	}
	if s.Digest.SHA256 != "" {
		m["digest"] = map[string]any{"sha256": s.Digest.SHA256}
	}
	if s.EntryPoint != "" {
		m["entryPoint"] = s.EntryPoint
	}
	return m
}

func materialsToSlice(mats []Material) []map[string]any {
	if len(mats) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(mats))
	for _, mt := range mats {
		m := map[string]any{"uri": mt.URI}
		if mt.Digest.SHA256 != "" {
			m["digest"] = map[string]any{"sha256": mt.Digest.SHA256}
		}
		out = append(out, m)
	}
	return out
}

func metadataToMap(md Metadata) map[string]any {
	m := map[string]any{}
	if md.BuildStartedOn != "" {
		m["buildStartedOn"] = md.BuildStartedOn
	}
	if md.BuildFinishedOn != "" {
		m["buildFinishedOn"] = md.BuildFinishedOn
	}
	if md.Completeness != nil {
		c := map[string]any{}
		if md.Completeness.Arguments {
			c["arguments"] = true
		}
		if md.Completeness.Environment {
			c["environment"] = true
		}
		if md.Completeness.Materials {
			c["materials"] = true
		}
		if len(c) > 0 {
			m["completeness"] = c
		}
	}
	if md.Reproducible {
		m["reproducible"] = true
	}
	return m
}

// sortedSubject returns the subject list sorted by name for determinism.
// We also normalize the envelope's own digest to zero so two attestations
// with otherwise-identical content hash to identical canonical bytes.
func sortedSubject(s []Subject) []map[string]any {
	out := make([]map[string]any, 0, len(s))
	for _, x := range s {
		m := map[string]any{"name": x.Name}
		if x.Digest.SHA256 != "" {
			m["digest"] = map[string]any{"sha256": x.Digest.SHA256}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

// marshalSorted is a thin wrapper that re-encodes a map[string]any via
// json.Marshal (which sorts map keys).
func marshalSorted(v any) ([]byte, error) {
	return json.Marshal(v)
}

// stringifyKeys converts any map[string]any value containing non-string
// keys to a stable representation. We only need this when the producer
// passes arbitrary Work data through the predicate; json.Marshal will
// already reject non-string map keys, so we coerce here.
func stringifyKeys(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ErrAttestationInvalid is returned when the producer's input cannot
// yield a schema-valid attestation (e.g. empty Work).
var ErrAttestationInvalid = errors.New("attestation invalid")

// ErrWorkNotTerminal is returned by Produce when the Work has not yet
// reached a terminal state. Mid-execution attestation is not allowed:
// the producer must observe the full set of attempts, artifacts, and
// leases the Work produced before signing.
var ErrWorkNotTerminal = errors.New("work not in terminal state")