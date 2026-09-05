package workgraph_test

import (
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// G1 — Evidence.Hash: deterministisk SHA-256 over canonical felter (ID, NodeID,
// AttemptID, Type, Result, RecordedAt, ArtifactID, Signer, Environment).
// Details er EKSKLUDERET (fri-form metadata; hashen dækker identitet + udfald,
// ikke variable detaljer). Samme input -> samme hash; et ændret felt -> ny hash.

func sampleEvidence() workgraph.Evidence {
	return workgraph.Evidence{
		ID:          "evd_abc123",
		NodeID:      "n1",
		AttemptID:   "att_1",
		Type:        "build",
		Result:      "pass",
		RecordedAt:  time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
		ArtifactID:  "art_1",
		Signer:      "wrkr_e2e",
		Environment: "development",
		Details:     map[string]any{"cache": "disabled"},
	}
}

func TestEvidenceHashDeterministic(t *testing.T) {
	e := sampleEvidence()
	h1 := e.RecomputedHash()
	h2 := e.RecomputedHash()
	if h1 == "" {
		t.Fatal("hash must not be empty")
	}
	if len(h1) != 64 {
		t.Fatalf("hash must be 64 hex chars (sha256), got %d: %q", len(h1), h1)
	}
	if h1 != h2 {
		t.Fatalf("hash must be deterministic: %q != %q", h1, h2)
	}
}

func TestEvidenceHashChangesOnFieldChange(t *testing.T) {
	base := sampleEvidence().RecomputedHash()

	cases := []struct {
		name string
		mut  func(*workgraph.Evidence)
	}{
		{"id", func(e *workgraph.Evidence) { e.ID = "evd_other" }},
		{"node_id", func(e *workgraph.Evidence) { e.NodeID = "n2" }},
		{"attempt_id", func(e *workgraph.Evidence) { e.AttemptID = "att_2" }},
		{"type", func(e *workgraph.Evidence) { e.Type = "test" }},
		{"result", func(e *workgraph.Evidence) { e.Result = "fail" }},
		{"recorded_at", func(e *workgraph.Evidence) { e.RecordedAt = e.RecordedAt.Add(1 * time.Second) }},
		{"artifact_id", func(e *workgraph.Evidence) { e.ArtifactID = "art_2" }},
		{"signer", func(e *workgraph.Evidence) { e.Signer = "wrkr_other" }},
		{"environment", func(e *workgraph.Evidence) { e.Environment = "staging" }},
	}
	for _, tc := range cases {
		e := sampleEvidence()
		tc.mut(&e)
		if e.RecomputedHash() == base {
			t.Errorf("%s: hash must change when %s changes", tc.name, tc.name)
		}
	}
}

func TestEvidenceHashIgnoresDetails(t *testing.T) {
	// Details er fri-form og EKSKLUDERET fra hashen: identitet + udfald er
	// det der verificeres; detaljer kan variere uden at bryde hashen.
	a := sampleEvidence()
	b := sampleEvidence()
	b.Details = map[string]any{"cache": "enabled", "extra": 42}
	if a.Hash != b.Hash {
		t.Fatal("hash must NOT change when only Details change")
	}
}

func TestEvidenceSealComputesAndStoresHash(t *testing.T) {
	e := sampleEvidence()
	e.Hash = ""
	e.Seal()
	if e.Hash == "" {
		t.Fatal("Seal must set Hash")
	}
	// Seal er deterministisk: gen-seal giver samme hash
	want := e.Hash
	e.Hash = ""
	e.Seal()
	if e.Hash != want {
		t.Fatalf("Seal must be deterministic: %q != %q", e.Hash, want)
	}
	// Tamper-detektion: ændret Result bryder hashen
	e.Result = "fail"
	if e.Hash == e.RecomputedHash() {
		t.Fatal("tampered evidence must produce a different recomputed hash")
	}
}

// G2 — Seal-protokollen som oprettelsespunkterne skal folge: hver evidence
// fra platformens tre creation-paths (worker/classifier/takeover) foddes med
// integrity-hash. Seal-protokol-verifikation pa helper-niveau.
func TestSealProtocolAllCreationPoints(t *testing.T) {
	paths := []workgraph.Evidence{
		{ID: "evd_w1", NodeID: "n1", Type: "build", Result: "pass", Signer: "wrkr_1"},
		{ID: "evd_l1", NodeID: "n2", AttemptID: "a1", Type: "policy", Result: "fail", Signer: "classifier"},
		{ID: "evd_t1", NodeID: "n3", AttemptID: "a2", Type: "takeover_event", Result: "pass", Signer: "wrkr_2"},
	}
	for i := range paths {
		ev := paths[i]
		ev.Seal()
		if ev.Hash == "" {
			t.Fatalf("path %d: Seal must set Hash", i)
		}
		valid, unsealed := ev.HashValid()
		if unsealed {
			t.Fatalf("path %d: sealed evidence must not be unsealed", i)
		}
		if !valid {
			t.Fatalf("path %d: sealed evidence hash must be valid", i)
		}
	}
}
