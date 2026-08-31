// Package provenance_test exercises the workflow-provenance attestation
// producer end-to-end against an in-memory fake store.
//
// Slice 5 / k-impl-005. We deliberately avoid SQLite here — the goal is
// to prove the producer's behavior in isolation. The handler-level
// persistence path is covered by handler_test.go via the real work store.

package provenance_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/standards"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/provenance"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// schemaName is the basename of the workflow-provenance JSON Schema as
// embedded by internal/standards.
const schemaName = "workflow-provenance.schema.json"

func newSigner(t *testing.T) *provenance.Signer {
	t.Helper()
	s, err := provenance.NewSigner([]byte("test-key-do-not-use-in-prod-32bytes"), "test-key-v1")
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return s
}

func terminalWork() *workgraph.Work {
	return &workgraph.Work{
		ID:    "wrk_test_001",
		State: workgraph.StateSucceeded,
		Source: workgraph.Source{
			Type:       "cli",
			Repository: "acme/widgets",
			Revision:   "abc1234",
			Ref:        "refs/heads/main",
			Actor:      "alice",
		},
		Objective: workgraph.Objective{Type: "verify_change", Description: "smoke"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{
				"a": {ID: "a", Run: "go test ./..."},
			},
		},
		Requirements: workgraph.Requirements{OS: "linux", Arch: "amd64", Confidence: "development"},
		Policy:       workgraph.Policy{ForkPolicy: "deny", TrustClass: "standard"},
		CreatedAt:    time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 8, 1, 12, 1, 30, 0, time.UTC),
		Attempts: []workgraph.Attempt{
			{
				ID:         "att_1",
				NodeID:     "a",
				WorkerID:   "wrkr_local",
				StartedAt:  time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
				FinishedAt: time.Date(2026, 8, 1, 12, 1, 30, 0, time.UTC),
				ExitCode:   0,
				Status:     "succeeded",
			},
		},
		Artifacts: []workgraph.Artifact{
			{
				ID:       "deadbeef00000000000000000000000000000000000000000000000000000000",
				NodeID:   "a",
				MimeType: "text/plain",
				Size:     42,
				Path:     "/tmp/artifacts/a/deadbeef.txt",
			},
		},
	}
}

// fakeStore is an in-memory Store that satisfies both the package-level
// Store interface and the WorkStore alias. It tracks every Save so tests
// can assert the persistence contract.
type fakeStore struct {
	mu         sync.Mutex
	provenance map[string]provenance.ProvenanceRow
	works      map[string]*provenance.WorkSnapshot
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		provenance: map[string]provenance.ProvenanceRow{},
		works:      map[string]*provenance.WorkSnapshot{},
	}
}

func (f *fakeStore) putWork(w *workgraph.Work) *provenance.WorkSnapshot {
	return f.putWorkWithLeases(w, nil)
}

func (f *fakeStore) putWorkWithLeases(w *workgraph.Work, leases []workgraph.Lease) *provenance.WorkSnapshot {
	snap := &provenance.WorkSnapshot{
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
		Leases:       leases,
	}
	f.works[w.ID] = snap
	return snap
}

func (f *fakeStore) GetProvenance(_ context.Context, id string) (*provenance.ProvenanceRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.provenance[id]; ok {
		cp := p
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeStore) SaveProvenance(_ context.Context, p provenance.ProvenanceRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provenance[p.WorkID] = p
	return nil
}

func (f *fakeStore) GetProvenanceWork(_ context.Context, id string) (*provenance.WorkSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.works[id]
	if !ok {
		return nil, provenance.ErrNoWork
	}
	cp := *w
	return &cp, nil
}

// TestProduce_HappyPath verifies the full lifecycle: Produce builds,
// signs, and persists; the persisted row round-trips through the store.
func TestProduce_HappyPath(t *testing.T) {
	signer := newSigner(t)
	prod := provenance.New(signer)
	prod.Now = func() time.Time { return time.Date(2026, 8, 1, 12, 2, 0, 0, time.UTC) }

	st := newFakeStore()
	st.putWork(terminalWork())

	ctx := context.Background()
	row, err := prod.Produce(ctx, st, "wrk_test_001")
	if err != nil {
		t.Fatalf("produce: %v", err)
	}

	if row.WorkID != "wrk_test_001" {
		t.Errorf("work_id: got %q", row.WorkID)
	}
	if len(row.Attestation) == 0 {
		t.Error("attestation empty")
	}
	if len(row.Signature) == 0 {
		t.Error("signature empty")
	}
	if row.KeyID != "test-key-v1" {
		t.Errorf("key_id: got %q", row.KeyID)
	}
	if row.BuilderID != provenance.BuilderURI {
		t.Errorf("builder_id: got %q", row.BuilderID)
	}

	// Verify the signature against the canonical envelope.
	if err := signer.Verify(row.Attestation, string(row.Signature)); err != nil {
		t.Errorf("signature verify: %v", err)
	}

	// The persisted row matches what we got back.
	got, err := st.GetProvenance(ctx, "wrk_test_001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected persisted row")
	}
	if string(got.Signature) != string(row.Signature) {
		t.Error("signature not persisted")
	}
}

// TestProduce_SchemaValidation asserts the envelope validates against
// docs/standards/schemas/workflow-provenance.schema.json.
func TestProduce_SchemaValidation(t *testing.T) {
	signer := newSigner(t)
	prod := provenance.New(signer)

	st := newFakeStore()
	st.putWork(terminalWork())
	row, err := prod.Produce(context.Background(), st, "wrk_test_001")
	if err != nil {
		t.Fatalf("produce: %v", err)
	}

	if err := standards.ValidateBytes(schemaName, row.Attestation); err != nil {
		t.Errorf("attestation does not validate against schema: %v", err)
	}
}

// TestProduce_RefusesNonTerminal confirms the producer enforces the
// terminal-state invariant.
func TestProduce_RefusesNonTerminal(t *testing.T) {
	prod := provenance.New(newSigner(t))
	st := newFakeStore()
	w := terminalWork()
	w.State = workgraph.StateRunning
	st.putWork(w)

	_, err := prod.Produce(context.Background(), st, w.ID)
	if !errors.Is(err, provenance.ErrWorkNotTerminal) {
		t.Fatalf("expected ErrWorkNotTerminal, got %v", err)
	}
}

// TestProduce_SignatureTamperRejected proves the verifier rejects any
// modification of the envelope, even a one-byte change.
func TestProduce_SignatureTamperRejected(t *testing.T) {
	signer := newSigner(t)
	st := newFakeStore()
	st.putWork(terminalWork())
	row, err := provenance.New(signer).Produce(context.Background(), st, "wrk_test_001")
	if err != nil {
		t.Fatalf("produce: %v", err)
	}
	tampered := append([]byte{}, row.Attestation...)
	tampered[10] ^= 0x01
	if err := signer.Verify(tampered, string(row.Signature)); err == nil {
		t.Error("expected verifier to reject tampered envelope")
	}
}

// TestProduce_MaterialsSorted asserts the materials URI list is sorted —
// a key reproducibility invariant.
func TestProduce_MaterialsSorted(t *testing.T) {
	prod := provenance.New(newSigner(t))
	st := newFakeStore()
	st.putWork(terminalWork())

	row, err := prod.Produce(context.Background(), st, "wrk_test_001")
	if err != nil {
		t.Fatalf("produce: %v", err)
	}

	var env struct {
		Predicate struct {
			Materials []struct {
				URI    string `json:"uri"`
				Digest struct {
					SHA256 string `json:"sha256"`
				} `json:"digest"`
			} `json:"materials"`
		} `json:"predicate"`
	}
	if err := json.Unmarshal(row.Attestation, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	uris := make([]string, 0, len(env.Predicate.Materials))
	for _, m := range env.Predicate.Materials {
		uris = append(uris, m.URI)
	}
	for i := 1; i < len(uris); i++ {
		if uris[i-1] > uris[i] {
			t.Errorf("materials not sorted at index %d: %q > %q", i, uris[i-1], uris[i])
		}
	}
}

// TestProduce_IncludesLeasesAsMaterials proves the producer rolls leases
// into predicate.materials with a stable digest.
func TestProduce_IncludesLeasesAsMaterials(t *testing.T) {
	prod := provenance.New(newSigner(t))
	st := newFakeStore()
	w := terminalWork()
	st.putWorkWithLeases(w, []workgraph.Lease{
		{
			ID:        "lse_1",
			WorkID:    w.ID,
			NodeID:    "a",
			WorkerID:  "wrkr_local",
			AttemptID: "att_1",
			Status:    workgraph.LeaseReleased,
		},
	})

	row, err := prod.Produce(context.Background(), st, w.ID)
	if err != nil {
		t.Fatalf("produce: %v", err)
	}
	var env struct {
		Predicate struct {
			Materials []struct {
				URI    string `json:"uri"`
				Digest struct {
					SHA256 string `json:"sha256"`
				} `json:"digest"`
			} `json:"materials"`
		} `json:"predicate"`
	}
	if err := json.Unmarshal(row.Attestation, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, m := range env.Predicate.Materials {
		if m.URI == "works-execution://leases/lse_1" && m.Digest.SHA256 != "" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected lease material lse_1, got %+v", env.Predicate.Materials)
	}
}

// TestProduce_Idempotent asserts the producer overwrites any previous
// attestation for the same Work — matching the schema migration's
// "monotonic per Work" semantics.
func TestProduce_Idempotent(t *testing.T) {
	prod := provenance.New(newSigner(t))
	st := newFakeStore()
	st.putWork(terminalWork())
	ctx := context.Background()

	row1, err := prod.Produce(ctx, st, "wrk_test_001")
	if err != nil {
		t.Fatalf("first produce: %v", err)
	}
	row2, err := prod.Produce(ctx, st, "wrk_test_001")
	if err != nil {
		t.Fatalf("second produce: %v", err)
	}
	if string(row1.Signature) != string(row2.Signature) {
		t.Error("expected identical signatures for idempotent produce")
	}
	stored, err := st.GetProvenance(ctx, "wrk_test_001")
	if err != nil || stored == nil {
		t.Fatalf("get: %v %+v", err, stored)
	}
	if string(stored.Signature) != string(row1.Signature) {
		t.Error("persisted signature does not match the second produce")
	}
}

// TestSigner_NewSignerRejectsEmptyKey asserts the signer fails closed
// when constructed with an empty key.
func TestSigner_NewSignerRejectsEmptyKey(t *testing.T) {
	if _, err := provenance.NewSigner(nil, "x"); err == nil {
		t.Error("expected error for empty key")
	}
	if _, err := provenance.NewSigner([]byte{}, "x"); err == nil {
		t.Error("expected error for zero-length key")
	}
}

// TestStoreAdapter_RoundTrip pins down the SQLite-backed adapter
// (proves the work store's SaveProvenance / GetProvenance / LeasesByWorkID
// shape the producer expects).
func TestStoreAdapter_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	w := terminalWork()
	if err := st.CreateWork(context.Background(), w); err != nil {
		t.Fatalf("create: %v", err)
	}
	adapter := &provenance.StoreAdapter{Inner: st}
	snap, err := adapter.GetProvenanceWork(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("load work: %v", err)
	}
	if snap.ID != w.ID {
		t.Errorf("snapshot id: got %q", snap.ID)
	}
	if snap.State != workgraph.StateSucceeded {
		t.Errorf("snapshot state: got %s", snap.State)
	}

	// Now persist a provenance row.
	row := provenance.ProvenanceRow{
		WorkID:      w.ID,
		Attestation: []byte(`{"predicateType":"x","subject":[],"predicate":{}}`),
		Signature:   []byte("deadbeef"),
		KeyID:       "k",
		BuilderID:   "b",
		ProducedAt:  time.Now().UTC(),
	}
	if err := adapter.SaveProvenance(context.Background(), row); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := adapter.GetProvenance(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || string(got.Signature) != "deadbeef" {
		t.Errorf("persisted signature mismatch: %+v", got)
	}
}