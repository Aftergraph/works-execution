// Package store wraps services/work/store with the provenance-producer's
// narrow contract. The actual persistence lives in services/work/store;
// this file gives the producer a typed seam and lets tests inject fakes
// without pulling SQLite.

package provenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// ErrNoWork is returned when a Work snapshot cannot be produced because
// the Work ID is unknown.
var ErrNoWork = errors.New("provenance: work not found")

// WorkStore is an alias for the full producer-side contract. Kept for
// readability at call sites that want to be explicit about whether they
// need Work-hydration or only attestation I/O.
type WorkStore = Store

// StoreAdapter adapts the work store into the producer's Store contract.
type StoreAdapter struct {
	Inner store.Store
}

// SaveProvenance persists the row in the work store.
func (a *StoreAdapter) SaveProvenance(ctx context.Context, p ProvenanceRow) error {
	if a.Inner == nil {
		return errors.New("provenance: nil inner store")
	}
	return a.Inner.SaveProvenance(ctx, store.Provenance{
		WorkID:      p.WorkID,
		Attestation: p.Attestation,
		Signature:   p.Signature,
		KeyID:       p.KeyID,
		BuilderID:   p.BuilderID,
		ProducedAt:  p.ProducedAt,
	})
}

// GetProvenance fetches an existing attestation; returns nil if none yet.
func (a *StoreAdapter) GetProvenance(ctx context.Context, workID string) (*ProvenanceRow, error) {
	if a.Inner == nil {
		return nil, errors.New("provenance: nil inner store")
	}
	p, err := a.Inner.GetProvenance(ctx, workID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ProvenanceRow{
		WorkID:      p.WorkID,
		Attestation: p.Attestation,
		Signature:   p.Signature,
		KeyID:       p.KeyID,
		BuilderID:   p.BuilderID,
		ProducedAt:  p.ProducedAt,
	}, nil
}

// GetProvenanceWork hydrates a full Work snapshot suitable for the
// producer. It queries attempts, artifacts, evidence, and leases; callers
// that only need the JSON envelope should use SaveProvenance/GetProvenance
// directly.
func (a *StoreAdapter) GetProvenanceWork(ctx context.Context, workID string) (*WorkSnapshot, error) {
	if a.Inner == nil {
		return nil, errors.New("provenance: nil inner store")
	}
	w, err := a.Inner.GetWork(ctx, workID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	leases, err := a.Inner.LeasesByWorkID(ctx, workID)
	if err != nil {
		return nil, fmt.Errorf("load leases: %w", err)
	}
	lv := make([]workgraph.Lease, 0, len(leases))
	for _, l := range leases {
		if l != nil {
			lv = append(lv, *l)
		}
	}
	return &WorkSnapshot{
		ID:           w.ID,
		State:        w.State,
		CreatedAt:    w.CreatedAt,
		UpdatedAt:    w.UpdatedAt,
		Source:       w.Source,
		Objective:    w.Objective,
		Graph:        w.Graph,
		Requirements: w.Requirements,
		Policy:       w.Policy,
		Attempts:     w.Attempts,
		Artifacts:    w.Artifacts,
		Evidence:     w.Evidence,
		Leases:       lv,
	}, nil
}