package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// testKey is a deterministic 32-byte key for stable HMAC tests.
func testKey(t *testing.T) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte("evidence-internal-test-key"))
	return sum[:]
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "ev-int.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedSucceeded(t *testing.T, st store.Store) *workgraph.Work {
	t.Helper()
	ctx := context.Background()
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		Source:    workgraph.Source{Type: "cli", Repository: "acme/x"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}},
		},
	}
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	lease, _, err := st.GrantLease(ctx, w.ID, "a", "wkr", 5*time.Second)
	if err != nil {
		t.Fatalf("GrantLease: %v", err)
	}
	artifact := &workgraph.Artifact{
		ID:       strings.Repeat("a", 64),
		NodeID:   "a",
		MimeType: "text/plain",
		Size:     1,
		Path:     "/tmp/o",
	}
	if _, err := st.CompleteLease(ctx, lease.ID, 0, artifact, nil); err != nil {
		t.Fatalf("CompleteLease: %v", err)
	}
	// CompleteLease already transitions RUNNING -> VERIFYING -> SUCCEEDED
	// only when the work graph has been fully satisfied; in our minimal
	// single-node graph that returns SUCCEEDED. Verify and only transition
	// if we are still in a non-terminal state.
	w2, err := st.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if !w2.State.IsTerminal() {
		if _, err := st.UpdateState(ctx, w.ID, workgraph.StateSucceeded); err != nil {
			t.Fatalf("UpdateState SUCCEEDED: %v", err)
		}
		w2, err = st.GetWork(ctx, w.ID)
		if err != nil {
			t.Fatalf("GetWork: %v", err)
		}
	}
	return w2
}

func TestProduce_BundleIDFormat(t *testing.T) {
	st := openStore(t)
	w := seedSucceeded(t, st)
	b, err := Produce(context.Background(), st, w.ID, ProducerConfig{
		KeyID: "k1", HMACKey: testKey(t),
		Runner: Runner{ID: "r1", TrustClass: "standard"},
		Now:    func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
	re := regexp.MustCompile(`^evb_[a-f0-9]{32}$`)
	if !re.MatchString(b.BundleID) {
		t.Errorf("bundle_id format: %q", b.BundleID)
	}
}

func TestProduce_DeterministicBundleID(t *testing.T) {
	st := openStore(t)
	w := seedSucceeded(t, st)
	mk := func() string {
		b, err := Produce(context.Background(), st, w.ID, ProducerConfig{
			KeyID: "k1", HMACKey: testKey(t),
			Runner: Runner{ID: "r1"},
			Now:    func() time.Time { return time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC) },
		})
		if err != nil {
			t.Fatalf("Produce: %v", err)
		}
		return b.BundleID
	}
	a, b := mk(), mk()
	if a != b {
		t.Errorf("non-deterministic bundle_id: %s vs %s", a, b)
	}
}

func TestProduce_DifferentIDsAcrossWorkIDs(t *testing.T) {
	st := openStore(t)
	w1 := seedSucceeded(t, st)
	w2 := seedSucceeded(t, st)
	b1, _ := Produce(context.Background(), st, w1.ID, ProducerConfig{
		KeyID: "k", HMACKey: testKey(t), Runner: Runner{ID: "r"},
		Now: func() time.Time { return time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC) },
	})
	b2, _ := Produce(context.Background(), st, w2.ID, ProducerConfig{
		KeyID: "k", HMACKey: testKey(t), Runner: Runner{ID: "r"},
		Now: func() time.Time { return time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC) },
	})
	if b1.BundleID == b2.BundleID {
		t.Errorf("expected different bundle_ids")
	}
}

func TestProduce_BundleIDEqualsSHA256OfCanonical(t *testing.T) {
	st := openStore(t)
	w := seedSucceeded(t, st)
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	b, err := Produce(context.Background(), st, w.ID, ProducerConfig{
		KeyID: "k", HMACKey: testKey(t), Runner: Runner{ID: "r"}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
	// Re-derive: clone without signatures, substitute placeholder
	// bundle_id (matches what Produce signed), canonicalize, sha256.
	clone := *b
	clone.BundleID = placeholderBundleID
	clone.Signatures = nil
	canonical, err := canonicalize(&clone)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	sum := sha256.Sum256(canonical)
	want := "evb_" + hex.EncodeToString(sum[:])[:32]
	if b.BundleID != want {
		t.Errorf("bundle_id mismatch:\n got  %s\n want %s", b.BundleID, want)
	}
}

func TestProduce_VerifyRoundTrip(t *testing.T) {
	st := openStore(t)
	w := seedSucceeded(t, st)
	b, err := Produce(context.Background(), st, w.ID, ProducerConfig{
		KeyID: "k1", HMACKey: testKey(t), Runner: Runner{ID: "r"},
	})
	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
	if !Verify(b, "k1", testKey(t)) {
		t.Fatalf("Verify: signature did not validate")
	}
	if Verify(b, "wrong-key", testKey(t)) {
		t.Errorf("Verify accepted wrong key id")
	}
	if Verify(b, "k1", []byte("wrong-key-bytes")) {
		t.Errorf("Verify accepted wrong key bytes")
	}
}

func TestProduce_TamperDetection(t *testing.T) {
	st := openStore(t)
	w := seedSucceeded(t, st)
	b, err := Produce(context.Background(), st, w.ID, ProducerConfig{
		KeyID: "k", HMACKey: testKey(t), Runner: Runner{ID: "r"},
	})
	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
	if !Verify(b, "k", testKey(t)) {
		t.Fatalf("Verify: pre-tamper failed")
	}
	b.Summary.Result = workgraph.StateFailed
	if Verify(b, "k", testKey(t)) {
		t.Errorf("Verify accepted tampered bundle")
	}
}

func TestProduce_KeyLinking(t *testing.T) {
	st := openStore(t)
	w := seedSucceeded(t, st)
	b, err := Produce(context.Background(), st, w.ID, ProducerConfig{
		KeyID: "k", HMACKey: testKey(t), Runner: Runner{ID: "r"},
	})
	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
	if len(b.Components.Attempts) == 0 || len(b.Components.Leases) == 0 {
		t.Fatalf("expected attempts + leases populated")
	}
	if b.Components.Attempts[0].LeaseID != b.Components.Leases[0].ID {
		t.Errorf("attempt not linked to lease: %q vs %q",
			b.Components.Attempts[0].LeaseID, b.Components.Leases[0].ID)
	}
}

func TestProduce_ComponentsShape(t *testing.T) {
	st := openStore(t)
	w := seedSucceeded(t, st)
	b, err := Produce(context.Background(), st, w.ID, ProducerConfig{
		KeyID: "k", HMACKey: testKey(t), Runner: Runner{ID: "r"},
	})
	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"bundle_id", "work_id", "created_at", "summary", "components"} {
		if _, ok := back[key]; !ok {
			t.Errorf("missing required field: %s", key)
		}
	}
	comp := back["components"].(map[string]any)
	for _, key := range []string{"attempts", "artifacts", "evidence"} {
		if _, ok := comp[key]; !ok {
			t.Errorf("components.%s missing", key)
		}
	}
}

func TestProduce_SummaryDuration(t *testing.T) {
	st := openStore(t)
	w := seedSucceeded(t, st)
	b, err := Produce(context.Background(), st, w.ID, ProducerConfig{
		KeyID: "k", HMACKey: testKey(t), Runner: Runner{ID: "r"},
	})
	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
	if b.Summary.DurationMS < 0 {
		t.Errorf("negative duration: %d", b.Summary.DurationMS)
	}
	if b.Summary.Result != workgraph.StateSucceeded {
		t.Errorf("result: got %s, want SUCCEEDED", b.Summary.Result)
	}
}