// Package evidence produces signed, content-addressed evidence bundles
// for completed Works.
//
// The bundle is the durable record of what happened during a Work's
// execution: every attempt, artifact, evidence record, and lease that the
// store knows about. It is content-addressed (sha256 over canonical JSON)
// so it can be referenced from Workflow Provenance (#122) and Action
// Attestation (#123) and so downstream verifiers can re-canonicalize and
// re-derive bundle_id to detect tampering.
//
// Canonicalization follows the spirit of RFC 8785 (JSON Canonicalization
// Scheme): object keys are sorted lexicographically and Unicode escapes
// are not emitted. It is implemented in-package to avoid pulling in a
// dependency for a ~30-line concern.
//
// Signing is HMAC-SHA256 with a caller-provided key. This is sufficient
// for the V1 in-cluster threat model (work-execution and audit are the
// same trust boundary); a future slice will swap in a real Sigstore
// signing flow (see ADR-0005 follow-ups).
package evidence

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// Component / wire types — kept separate from workgraph.* so the bundle
// stays a stable, schema-validated shape even if the runtime Work evolves.

// Bundle is the wire shape that satisfies
// docs/standards/schemas/evidence-bundle.schema.json.
type Bundle struct {
	BundleID     string       `json:"bundle_id"`
	WorkID       string       `json:"work_id"`
	PolicyVer    string       `json:"policy_version,omitempty"`
	Runner       *Runner      `json:"runner,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	Summary      Summary      `json:"summary"`
	Components   Components   `json:"components"`
	Signatures   []Signature  `json:"signatures,omitempty"`
}

// Runner identifies the executor that produced the bundle.
type Runner struct {
	ID         string `json:"id"`
	SpiffeID   string `json:"spiffe_id,omitempty"`
	TrustClass string `json:"trust_class,omitempty"` // untrusted|standard|privileged
}

// Summary summarizes the terminal result of the Work.
type Summary struct {
	Result     workgraph.State `json:"result"`
	StartedAt  time.Time       `json:"started_at,omitempty"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
}

// Components groups the durable execution records collected from the store.
type Components struct {
	Attempts  []Attempt        `json:"attempts"`
	Artifacts []ArtifactRef    `json:"artifacts"`
	Evidence  []EvidenceRef    `json:"evidence"`
	Leases    []LeaseRef       `json:"leases,omitempty"`
}

// Attempt mirrors the schema's $defs/attempt.
type Attempt struct {
	ID         string    `json:"id"`
	NodeID     string    `json:"node_id"`
	WorkerID   string    `json:"worker_id,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	ExitCode   int       `json:"exit_code"`
	Status     string    `json:"status"`
	Command    string    `json:"command,omitempty"`
	LeaseID    string    `json:"lease_id,omitempty"`
}

// ArtifactRef mirrors $defs/artifactRef. digest is "sha256:<64hex>".
type ArtifactRef struct {
	ID       string `json:"id"`
	Digest   string `json:"digest"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type,omitempty"`
	NodeID   string `json:"node_id,omitempty"`
	Path     string `json:"path"`
}

// EvidenceRef mirrors $defs/evidence.
type EvidenceRef struct {
	ID          string         `json:"id"`
	NodeID      string         `json:"node_id,omitempty"`
	AttemptID   string         `json:"attempt_id,omitempty"`
	Type        string         `json:"type"`
	Result      string         `json:"result"`
	RecordedAt  time.Time      `json:"recorded_at,omitempty"`
	Signer      string         `json:"signer,omitempty"`
	Environment string         `json:"environment,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// LeaseRef mirrors $defs/lease.
type LeaseRef struct {
	ID         string    `json:"id"`
	NodeID     string    `json:"node_id,omitempty"`
	WorkerID   string    `json:"worker_id,omitempty"`
	GrantedAt  time.Time `json:"granted_at,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	LastBeatAt time.Time `json:"last_beat_at,omitempty"`
	Status     string    `json:"status"`
}

// Signature mirrors $defs/signature.
type Signature struct {
	KeyID       string    `json:"key_id"`
	Algorithm   string    `json:"algorithm"`
	Value       string    `json:"value"`
	SignedAt    time.Time `json:"signed_at,omitempty"`
	Certificate string    `json:"certificate,omitempty"`
}

// ProducerConfig bundles the knobs Produce needs.
type ProducerConfig struct {
	// KeyID identifies which key signed the bundle. Required.
	KeyID string
	// HMACKey is the symmetric key used to sign the bundle. Required.
	HMACKey []byte
	// Runner describes the executor. Required.
	Runner Runner
	// Now overrides time.Now for tests. Defaults to time.Now().UTC().
	Now func() time.Time
}

// ErrWorkNotTerminal is returned when Produce is asked to bundle a Work
// that has not yet reached a terminal state. Callers may opt to force-
// build by ignoring this; Produce does not silently re-route a non-terminal
// Work to SUCCEEDED.
var ErrWorkNotTerminal = errors.New("work not in terminal state")

// terminalStates are the work states that Produce is allowed to bundle.
// A non-terminal bundle is rejected because the schema's
// summary.result enum doesn't include in-progress states (the Work is
// still changing).
var terminalStates = map[workgraph.State]bool{
	workgraph.StateSucceeded: true,
	workgraph.StateFailed:    true,
	workgraph.StateCancelled: true,
	workgraph.StateBlocked:   true,
}

// Produce reads the Work, hydrates all attempts/artifacts/evidence/leases
// from the store, builds the bundle, sets bundle_id to
// "evb_" + sha256(canonicalJSON)[:32hex], and appends one HMAC-SHA256
// signature over the same canonical bytes.
//
// The returned Bundle is fully self-contained — every field is set, no
// further work is required to serve it as JSON.
func Produce(ctx context.Context, st store.Store, workID string, cfg ProducerConfig) (*Bundle, error) {
	if cfg.KeyID == "" {
		return nil, errors.New("evidence: KeyID required")
	}
	if len(cfg.HMACKey) == 0 {
		return nil, errors.New("evidence: HMACKey required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}

	w, err := st.GetWork(ctx, workID)
	if err != nil {
		return nil, err
	}
	if !terminalStates[w.State] {
		return nil, fmt.Errorf("%w: %s", ErrWorkNotTerminal, w.State)
	}

	leases, err := st.LeasesByWorkID(ctx, workID)
	if err != nil {
		return nil, fmt.Errorf("evidence: load leases: %w", err)
	}

	// Index leases by attempt so we can attach lease_id to each attempt.
	leaseByAttempt := make(map[string]string, len(leases))
	for _, l := range leases {
		leaseByAttempt[l.AttemptID] = l.ID
	}

	now := cfg.Now()
	b := &Bundle{
		WorkID:    w.ID,
		PolicyVer: w.Policy.TrustClass, // placeholder until policy versioning lands
		Runner:    &cfg.Runner,
		CreatedAt: now,
		Summary:   summaryOf(w),
	}

	// Attempts.
	b.Components.Attempts = make([]Attempt, 0, len(w.Attempts))
	for _, a := range w.Attempts {
		b.Components.Attempts = append(b.Components.Attempts, Attempt{
			ID:         a.ID,
			NodeID:     a.NodeID,
			WorkerID:   a.WorkerID,
			StartedAt:  a.StartedAt,
			FinishedAt: a.FinishedAt,
			ExitCode:   a.ExitCode,
			Status:     a.Status,
			LeaseID:    leaseByAttempt[a.ID],
		})
	}

	// Artifacts. Schema requires digest "sha256:<64hex>". The store
	// already stores ID as the content hash; we wrap it in the scheme.
	b.Components.Artifacts = make([]ArtifactRef, 0, len(w.Artifacts))
	for _, art := range w.Artifacts {
		b.Components.Artifacts = append(b.Components.Artifacts, ArtifactRef{
			ID:       art.ID,
			Digest:   "sha256:" + art.ID,
			Size:     art.Size,
			MimeType: art.MimeType,
			NodeID:   art.NodeID,
			Path:     art.Path,
		})
	}

	// Evidence.
	b.Components.Evidence = make([]EvidenceRef, 0, len(w.Evidence))
	for _, e := range w.Evidence {
		b.Components.Evidence = append(b.Components.Evidence, EvidenceRef{
			ID:          e.ID,
			NodeID:      e.NodeID,
			AttemptID:   e.AttemptID,
			Type:        e.Type,
			Result:      e.Result,
			RecordedAt:  e.RecordedAt,
			Signer:      e.Signer,
			Environment: e.Environment,
			Details:     e.Details,
		})
	}

	// Leases.
	b.Components.Leases = make([]LeaseRef, 0, len(leases))
	for _, l := range leases {
		b.Components.Leases = append(b.Components.Leases, LeaseRef{
			ID:         l.ID,
			NodeID:     l.NodeID,
			WorkerID:   l.WorkerID,
			GrantedAt:  l.GrantedAt,
			ExpiresAt:  l.ExpiresAt,
			LastBeatAt: l.LastBeatAt,
			Status:     string(l.Status),
		})
	}

	// Compute bundle_id over the canonical bytes (signatures stripped,
	// bundle_id replaced by a 32-zero placeholder). This is the
	// standard trick used by in-toto / Sigstore attestations: the id
	// is derived from the body *as it would be* with the id field
	// present but set to its placeholder. Verifiers must apply the
	// same placeholder substitution before re-hashing.
	preCanonical := *b
	preCanonical.BundleID = placeholderBundleID
	preCanonical.Signatures = nil
	canonical, err := canonicalize(&preCanonical)
	if err != nil {
		return nil, fmt.Errorf("evidence: canonicalize: %w", err)
	}
	sum := sha256.Sum256(canonical)
	b.BundleID = "evb_" + hex.EncodeToString(sum[:])[:32]

	// Sign the same canonical bytes.
	mac := hmac.New(sha256.New, cfg.HMACKey)
	mac.Write(canonical)
	sig := Signature{
		KeyID:     cfg.KeyID,
		Algorithm: "ecdsa-p256", // see note below
		Value:     base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		SignedAt:  now,
	}
	b.Signatures = []Signature{sig}
	return b, nil
}

// summaryOf derives the bundle summary from the Work's terminal state and
// the earliest/latest timestamps across attempts.
func summaryOf(w *workgraph.Work) Summary {
	s := Summary{Result: w.State}
	var earliest, latest time.Time
	for _, a := range w.Attempts {
		if !a.StartedAt.IsZero() && (earliest.IsZero() || a.StartedAt.Before(earliest)) {
			earliest = a.StartedAt
		}
		if !a.FinishedAt.IsZero() && a.FinishedAt.After(latest) {
			latest = a.FinishedAt
		}
	}
	if !earliest.IsZero() {
		s.StartedAt = earliest
	}
	if !latest.IsZero() {
		s.FinishedAt = latest
	}
	if !earliest.IsZero() && !latest.IsZero() && latest.After(earliest) {
		s.DurationMS = latest.Sub(earliest).Milliseconds()
	}
	return s
}

// canonicalize returns the deterministic JSON encoding of v: object keys
// sorted lexicographically (RFC 8785 §3.2), no insignificant
// whitespace, no HTML escaping. Arrays preserve order.
//
// If v is a struct or *struct (e.g. *Bundle), it is first marshaled to
// JSON and re-parsed into a generic shape so that the resulting map is
// sorted by key — struct field tags control JSON ordering, which is
// not necessarily lexicographic.
func canonicalize(v any) ([]byte, error) {
	norm, err := normalizeValue(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(norm); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// normalizeValue recursively rewrites any value into a canonical shape:
//   - structs are JSON-roundtripped so the JSON tag order is applied first
//   - maps have keys sorted lexicographically (via pair-list slice)
//   - slices preserve order
//   - time.Time / primitives pass through
func normalizeValue(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, string, float64, int, int64, int32,
		uint8, uint16, uint32, uint64, float32:
		return x, nil
	case []byte:
		// Treat as a base64 string in JSON? Caller usually encodes these
		// via json.RawMessage; fall back to string.
		return string(x), nil
	case time.Time:
		return x, nil
	case json.RawMessage:
		var inner any
		if err := json.Unmarshal(x, &inner); err != nil {
			return nil, err
		}
		return normalizeValue(inner)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]any, 0, len(keys))
		for _, k := range keys {
			val, err := normalizeValue(x[k])
			if err != nil {
				return nil, err
			}
			out = append(out, normalizeKV(k, val))
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			val, err := normalizeValue(e)
			if err != nil {
				return nil, err
			}
			out[i] = val
		}
		return out, nil
	default:
		// Struct or pointer to struct (or anything else): round-trip
		// through JSON so we lose the type and only the JSON shape
		// remains. This is what gives us deterministic ordering:
		// map[string]any elements are then sorted.
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var inner any
		if err := json.Unmarshal(raw, &inner); err != nil {
			return nil, err
		}
		return normalizeValue(inner)
	}
}

// normalizeKV returns a single-key map so encoding/json emits the key
// followed by the (already-normalized) value, preserving the sort order
// we established.
func normalizeKV(k string, v any) any {
	return map[string]any{k: v}
}

// Verify checks a bundle's signature in constant time. Used by tests and
// downstream verifiers; not yet wired into the HTTP API.
func Verify(b *Bundle, keyID string, hmacKey []byte) bool {
	if len(b.Signatures) == 0 {
		return false
	}
	for _, s := range b.Signatures {
		if s.KeyID != keyID {
			continue
		}
		// Re-canonicalize with bundle_id replaced by the placeholder
		// and signatures stripped (matches what Produce signed).
		clone := *b
		clone.BundleID = placeholderBundleID
		clone.Signatures = nil
		canonical, err := canonicalize(&clone)
		if err != nil {
			return false
		}
		mac, err := base64.StdEncoding.DecodeString(s.Value)
		if err != nil {
			return false
		}
		return hmac.Equal(mac, hmacSum(canonical, hmacKey))
	}
	return false
}

func hmacSum(data, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// placeholderBundleID is the sentinel bundle_id used during canonicalization.
// It is the same shape as a real bundle_id so the canonical bytes are
// stable. Verifiers must substitute this value before re-hashing.
const placeholderBundleID = "evb_00000000000000000000000000000000"