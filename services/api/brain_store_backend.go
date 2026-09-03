package api

// k-050 seam — the concrete store-backend adapter that satisfies
// BrainBackend over *store.SQLiteStore (k-042).
//
// WHY AN ADAPTER EXISTS. k-042 (store) and k-043 (this surface) were built
// in parallel against the same frozen spec (brain.ns/1.0) and each encoded
// its side faithfully — but the store's method set is persistence-shaped
// (PutBrainObject(*store.BrainObject) error, Tombstoned(path), list-with-
// limit...) while the surface's BrainBackend is wire-shaped (input/output
// DTOs, revision allocation as an API law). The assertion in
// NewBrainServiceFromStore therefore can never succeed against the raw
// store. This file is the missing seam: it presents the wire shape and
// translates, and NewBrainServiceFromStore prefers it for *store.SQLiteStore
// while still accepting a hand-written fake (tests) or any other
// BrainBackend directly.
//
// Laws honored here (and enforced by the store underneath):
//   - revision allocation: next = latest+1, append-only, never an overwrite
//     (store: ErrBrainRevisionExists on collision, PK(path,revision)).
//   - evidence provenance: every persisted row carries evidence_ref; the
//     store refuses empty ones. A promotion has no caller-supplied evidence
//     by design (the HUMAN is the evidence) — the adapter derives
//     'human-stamp:<humanID>' so the provenance column is never blank.
//   - immutable stamp exception: k-041's construction-time exception —
//     promotion fields ride revision 1 IN PLACE, no new revision; the store
//     exposes that as one narrow guarded UPDATE (StampBrainPromotion).
//   - tombstones: append a new revision, content "{}" (no content), class
//     carried from the latest row, authoritative forced false (already
//     enforced by the handler's DTO; never set true here).

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/JonasAbde/works-execution/services/work/store"
)

// storeBrainBackend adapts *store.SQLiteStore to BrainBackend.
type storeBrainBackend struct{ ss *store.SQLiteStore }

var _ BrainBackend = (*storeBrainBackend)(nil)

// asBrainBackend returns the wire-shaped backend for known store types:
// the concrete *store.SQLiteStore goes through the adapter; anything that
// already satisfies BrainBackend (test fakes, future backends) is returned
// as-is; anything else is not a backend at all (nil).
func asBrainBackend(v any) BrainBackend {
	if ss, ok := v.(*store.SQLiteStore); ok {
		return &storeBrainBackend{ss: ss}
	}
	if bb, ok := v.(BrainBackend); ok {
		return bb
	}
	return nil
}

func (b *storeBrainBackend) PutBrainObject(ctx context.Context, in *BrainPut) (*BrainObject, error) {
	if in == nil {
		return nil, errors.New("brain seam: nil put")
	}
	contentJSON := in.ContentJSON
	if contentJSON == "" {
		// Tombstones and any content-less append persist empty content.
		contentJSON = "{}"
	}
	// Immutable promotion: the one in-place exception (k-041 §L5). The
	// handler marks it with Authoritative+Promotion+Revision=1.
	if in.Revision == 1 && in.Authoritative && in.Promotion == BrainPromotionHumanStamped &&
		in.Class == BrainClassImmutable {
		if err := b.ss.StampBrainPromotion(ctx, in.Path, in.HumanStamp); err != nil {
			return nil, err
		}
		return b.GetBrainObject(ctx, in.Path, 1)
	}
	latest, err := b.ss.LatestRevision(ctx, in.Path)
	if err != nil {
		return nil, err
	}
	rev := in.Revision
	if rev == 0 {
		rev = latest + 1
	}
	evidence := in.EvidenceRef
	if evidence == "" && in.Authoritative {
		evidence = "human-stamp:" + in.HumanStamp // the human IS the evidence
	}
	obj := &store.BrainObject{
		Path:          in.Path,
		Revision:      rev,
		Class:         in.Class,
		ContentJSON:   contentJSON,
		ContentHash:   in.ContentHash,
		Authoritative: in.Authoritative,
		Promotion:     mapPromotion(in.Promotion),
		HumanStamp:    in.HumanStamp,
		Tombstone:     in.Tombstone,
		EvidenceRef:   evidence,
		ExpiresAt:     in.ExpiresAt,
	}
	if err := b.ss.PutBrainObject(ctx, obj); err != nil {
		return nil, err
	}
	return projectBrainObject(obj), nil
}

func (b *storeBrainBackend) GetBrainObject(ctx context.Context, path string, revision int) (*BrainObject, error) {
	o, err := b.ss.GetBrainObject(ctx, path, revision)
	if err != nil {
		return nil, err
	}
	return projectBrainObject(o), nil
}

func (b *storeBrainBackend) ListBrainPathsWithPrefix(ctx context.Context, prefix string) ([]string, error) {
	return b.ss.ListBrainPathsWithPrefix(ctx, prefix, 0) // store default cap
}

func (b *storeBrainBackend) LatestRevision(ctx context.Context, path string) (int, error) {
	return b.ss.LatestRevision(ctx, path)
}

func (b *storeBrainBackend) Tombstoned(ctx context.Context, path string, revision int) (bool, error) {
	if revision == 0 {
		return b.ss.Tombstoned(ctx, path)
	}
	o, err := b.ss.GetBrainObject(ctx, path, revision)
	if err != nil {
		return false, err
	}
	return o.Tombstone, nil
}

func (b *storeBrainBackend) CreateBrainMount(ctx context.Context, in *BrainMountPut) (*BrainMount, error) {
	m := &store.BrainMount{
		ID:         in.ID,
		Subject:    in.Subject,
		PathPrefix: in.PathPrefix,
		Scopes:     in.Scopes,
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  in.ExpiresAt,
	}
	if err := b.ss.CreateBrainMount(ctx, m); err != nil {
		return nil, err
	}
	return projectBrainMount(m), nil
}

func (b *storeBrainBackend) RevokeBrainMount(ctx context.Context, id string) error {
	return b.ss.RevokeBrainMount(ctx, id)
}

func (b *storeBrainBackend) ListBrainMounts(ctx context.Context, subject string) ([]*BrainMount, error) {
	rows, err := b.ss.ListBrainMounts(ctx, subject, false) // API hides revoked
	if err != nil {
		return nil, err
	}
	out := make([]*BrainMount, 0, len(rows))
	for _, m := range rows {
		out = append(out, projectBrainMount(m))
	}
	return out, nil
}

// mapPromotion defaults the empty enum member to "none" (the store's own
// default; explicit on the DTO keeps the wire honest).
func mapPromotion(p string) string {
	if p == "" {
		return BrainPromotionNone
	}
	return p
}

func projectBrainObject(o *store.BrainObject) *BrainObject {
	return &BrainObject{
		Path:          o.Path,
		Revision:      o.Revision,
		ContentHash:   o.ContentHash,
		ContentJSON:   o.ContentJSON,
		EvidenceRef:   o.EvidenceRef,
		Class:         o.Class,
		Authoritative: o.Authoritative,
		Promotion:     o.Promotion,
		HumanStamp:    o.HumanStamp,
		Tombstone:     o.Tombstone,
		ExpiresAt:     o.ExpiresAt,
	}
}

func projectBrainMount(m *store.BrainMount) *BrainMount {
	return &BrainMount{
		ID:         m.ID,
		Subject:    m.Subject,
		PathPrefix: m.PathPrefix,
		Scopes:     append([]string{}, m.Scopes...),
		ExpiresAt:  m.ExpiresAt,
	}
}

// jsonValid is a tiny guard used by the seam tests; exported nowhere.
func jsonValid(raw string) bool { return json.Valid([]byte(raw)) }
