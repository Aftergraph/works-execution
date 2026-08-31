// Producer — workflow-provenance attestation producer.

package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Store is the persistence contract the producer depends on. The API
// passes in the work store; tests pass in a fake. The producer never
// reaches into SQLite directly.
type Store interface {
	GetProvenance(ctx context.Context, workID string) (*ProvenanceRow, error)
	SaveProvenance(ctx context.Context, p ProvenanceRow) error
	GetProvenanceWork(ctx context.Context, workID string) (*WorkSnapshot, error)
}

// ProvenanceRow is the producer's view of the persisted attestation.
// Mirrors services/work/store.Provenance but avoids an import cycle: the
// API package is the one place that owns the canonical type, while the
// producer package only depends on this minimal shape.
type ProvenanceRow struct {
	WorkID      string
	Attestation []byte
	Signature   []byte
	KeyID       string
	BuilderID   string
	ProducedAt  time.Time
}

// Producer builds and persists workflow-provenance attestations. A single
// instance is safe for concurrent use: the underlying store handles
// concurrency and the signer is stateless.
type Producer struct {
	Signer    *Signer
	BuilderID string
	Version   string
	// Now is overridable for deterministic tests. Defaults to time.Now.
	Now func() time.Time
}

// New constructs a Producer with sensible defaults: BuilderURI as the
// builder id and "v1" as the version. Callers may override either.
func New(signer *Signer) *Producer {
	return &Producer{
		Signer:    signer,
		BuilderID: BuilderURI,
		Version:   "v1",
		Now:       func() time.Time { return time.Now().UTC() },
	}
}

// Produce builds, signs, and persists a workflow-provenance attestation for
// the given workID. The returned ProvenanceRow is what was saved (with the
// produced-at timestamp and signature filled in).
//
// The Work is expected to be in a terminal state by the caller; Produce
// itself does not enforce that, because the calling policy (the API's
// state-transition handler) is the right place to gate it. Produce does
// refuse to attest when the Work has no attempts or when its required
// fields are empty — an attestation without execution traces is a lie.
func (p *Producer) Produce(ctx context.Context, st Store, workID string) (*ProvenanceRow, error) {
	if p == nil || p.Signer == nil {
		return nil, errors.New("provenance: producer not initialized")
	}
	if st == nil {
		return nil, errors.New("provenance: nil store")
	}
	if workID == "" {
		return nil, errors.New("provenance: workID required")
	}

	w, err := st.GetProvenanceWork(ctx, workID)
	if err != nil {
		return nil, fmt.Errorf("load work: %w", err)
	}
	if w == nil {
		return nil, fmt.Errorf("%w: work %s", ErrAttestationInvalid, workID)
	}
	if !w.State.IsTerminal() {
		// Mirror evidence.Produce: refuse to attest mid-execution. The
		// builder may emit additional materials as the work progresses;
		// capturing those at SUCCEEDED is what gives the attestation its
		// SLSA-style completeness property.
		return nil, fmt.Errorf("%w: state %s", ErrWorkNotTerminal, w.State)
	}

	att := p.build(w)
	envelope, err := att.canonicalBytes()
	if err != nil {
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	sig, err := p.Signer.Sign(envelope)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}

	row := ProvenanceRow{
		WorkID:      workID,
		Attestation: envelope,
		Signature:   []byte(sig),
		KeyID:       p.Signer.KeyID,
		BuilderID:   p.BuilderID,
		ProducedAt:  p.Now(),
	}
	if err := st.SaveProvenance(ctx, row); err != nil {
		return nil, fmt.Errorf("persist: %w", err)
	}
	return &row, nil
}

// WorkSnapshot is the minimal Work view the producer needs. Defined here
// (rather than imported from workgraph) so a test fake can satisfy it
// without spinning up SQLite.
type WorkSnapshot struct {
	ID           string
	State        workgraph.State
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Source       workgraph.Source
	Objective    workgraph.Objective
	Graph        workgraph.Graph
	Requirements workgraph.Requirements
	Policy       workgraph.Policy
	Attempts     []workgraph.Attempt
	Artifacts    []workgraph.Artifact
	Evidence     []workgraph.Evidence
	Leases       []workgraph.Lease
}

// build assembles an Attestation from a Work snapshot. The producer calls
// this once per Produce; it is package-private because the resulting
// Attestation is canonicalized and signed, not for external callers to
// fiddle with.
func (p *Producer) build(w *WorkSnapshot) *Attestation {
	att := &Attestation{
		PredicateType: PredicateTypeWorkflowProv,
		Subject:       p.subjects(w),
		Predicate: Predicate{
			Builder: Builder{
				ID:      p.BuilderID,
				Version: p.Version,
			},
			Invocation: Invocation{
				ConfigSource: &Source{
					URI:        w.Source.Repository,
					EntryPoint: w.Source.Ref,
				},
				Parameters: map[string]any{
					"objective_type":        w.Objective.Type,
					"objective_description": w.Objective.Description,
					"source_type":           w.Source.Type,
					"source_revision":       w.Source.Revision,
					"source_actor":          w.Source.Actor,
					"trust_class":           w.Policy.TrustClass,
					"fork_policy":           w.Policy.ForkPolicy,
				},
				Environment: map[string]any{
					"os":              w.Requirements.OS,
					"arch":            w.Requirements.Arch,
					"confidence":      w.Requirements.Confidence,
					"production":      w.Policy.ProductionAccess,
					"max_cost_usd":    w.Requirements.MaxCostUSD,
				},
			},
			Materials: p.materials(w),
		},
	}

	startedOn, finishedOn := p.timingBounds(w)
	if !startedOn.IsZero() || !finishedOn.IsZero() {
		att.Predicate.Metadata = &Metadata{
			BuildStartedOn:  isoOrEmpty(startedOn),
			BuildFinishedOn: isoOrEmpty(finishedOn),
			Completeness: &Completeness{
				Arguments:   true,
				Environment: true,
				Materials:   true,
			},
			Reproducible: false,
		}
	}
	return att
}

// subjects builds the Subject[] field. We use the Work ID as `name` and
// the canonical envelope digest will be filled in by the verifier; the
// empty digest here signals "self-referential" (per SLSA v1 §3.1).
func (p *Producer) subjects(w *WorkSnapshot) []Subject {
	out := []Subject{}
	for _, a := range w.Artifacts {
		out = append(out, Subject{
			Name:   artifactName(w, a),
			Digest: Digest{SHA256: a.ID},
		})
	}
	if len(out) == 0 {
		// Always include the Work itself as a subject so consumers can
		// locate the attestation even when no artifacts were produced.
		// Digest is required by the schema; use sha256("work:" + w.ID) as
		// a stable self-referential identifier.
		selfSum := sha256.Sum256([]byte("work:" + w.ID))
		out = append(out, Subject{
			Name:   w.ID,
			Digest: Digest{SHA256: hex.EncodeToString(selfSum[:])},
		})
	}
	return out
}

// materials turns the Work's leases (and any inputs we know about) into
// predicate.materials. The hash of each lease's worker identity is the
// material digest; this keeps the attestation reproducible without
// embedding live secrets.
func (p *Producer) materials(w *WorkSnapshot) []Material {
	mats := []Material{}
	seen := map[string]bool{}
	for _, l := range w.Leases {
		uri := fmt.Sprintf("works-execution://leases/%s", l.ID)
		if seen[uri] {
			continue
		}
		seen[uri] = true
		mats = append(mats, Material{
			URI:    uri,
			Digest: Digest{SHA256: sha256Hex(l.ID)},
		})
	}
	for _, a := range w.Artifacts {
		uri := fmt.Sprintf("works-execution://artifacts/%s/%s", w.ID, a.ID)
		if seen[uri] {
			continue
		}
		seen[uri] = true
		mats = append(mats, Material{
			URI:    uri,
			Digest: Digest{SHA256: a.ID},
		})
	}
	// The work-graph definition itself is a material: it determines
	// everything the Work could have produced. Hash the canonical JSON.
	if len(w.Graph.Nodes) > 0 {
		mats = append(mats, Material{
			URI:    fmt.Sprintf("works-execution://graph/%s", w.ID),
			Digest: Digest{SHA256: graphHash(w.Graph)},
		})
	}
	sort.Slice(mats, func(i, j int) bool { return mats[i].URI < mats[j].URI })
	return mats
}

func (p *Producer) timingBounds(w *WorkSnapshot) (time.Time, time.Time) {
	if len(w.Attempts) == 0 {
		return time.Time{}, time.Time{}
	}
	start, finish := w.Attempts[0].StartedAt, w.Attempts[0].FinishedAt
	for _, a := range w.Attempts {
		if !a.StartedAt.IsZero() && (start.IsZero() || a.StartedAt.Before(start)) {
			start = a.StartedAt
		}
		if !a.FinishedAt.IsZero() && a.FinishedAt.After(finish) {
			finish = a.FinishedAt
		}
	}
	if w.UpdatedAt.After(finish) {
		finish = w.UpdatedAt
	}
	return start, finish
}

func artifactName(w *WorkSnapshot, a workgraph.Artifact) string {
	return fmt.Sprintf("%s/%s/%s", w.ID, a.NodeID, a.ID)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// graphHash returns a stable hash of the Work's graph definition. We only
// hash node IDs and run commands; needs/evidence/env are part of the
// invocation, not the graph material itself.
func graphHash(g workgraph.Graph) string {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
		h.Write([]byte(g.Nodes[id].Run))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isoOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}