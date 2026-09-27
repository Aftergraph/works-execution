package verifiedstate

import (
	"errors"
	"reflect"
	"testing"
)

func TestCheckpointRoundTripThroughGenericHandoffState(t *testing.T) {
	nodes := []Node{
		verifiedNode("repo-head", "git:a"),
		verifiedNode("build", "build:a"),
	}
	edges := []Edge{{From: "repo-head", To: "build", Type: EdgeData}}
	cp, err := NewCheckpoint(nodes, edges, Snapshot{"repo-head": "git:a"})
	if err != nil {
		t.Fatal(err)
	}

	state := map[string]any{"legacy": "kept"}
	if err := AttachToStateSnapshot(state, cp); err != nil {
		t.Fatal(err)
	}
	if state["legacy"] != "kept" {
		t.Fatal("checkpoint attachment mutated unrelated handoff state")
	}

	got, present, err := CheckpointFromStateSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("checkpoint not found after attach")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round-tripped checkpoint invalid: %v", err)
	}
	if !reflect.DeepEqual(got.Snapshot, Snapshot{"repo-head": "git:a"}) {
		t.Fatalf("snapshot = %#v", got.Snapshot)
	}
}

func TestCheckpointRejectsNonCanonicalGraph(t *testing.T) {
	cp := Checkpoint{
		Schema: CheckpointSchema,
		Nodes: []Node{{ID: "candidate", State: StateCandidate}},
		Snapshot: Snapshot{"candidate": "v1"},
	}
	if err := cp.Validate(); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("Validate = %v, want ErrInvalidCheckpoint", err)
	}
}

func TestCheckpointRejectsFingerprintContradiction(t *testing.T) {
	cp := Checkpoint{
		Schema: CheckpointSchema,
		Nodes: []Node{verifiedNode("repo-head", "git:a")},
		Snapshot: Snapshot{"repo-head": "git:b"},
	}
	if err := cp.Validate(); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("Validate = %v, want ErrInvalidCheckpoint", err)
	}
}

func TestCheckpointReconcileKeepsCanonicalFingerprintSeparateFromObservation(t *testing.T) {
	cp, err := NewCheckpoint(
		[]Node{
			verifiedNode("repo-head", "git:a"),
			verifiedNode("build", "build:a"),
		},
		[]Edge{{From: "repo-head", To: "build", Type: EdgeData}},
		Snapshot{"repo-head": "git:a"},
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := cp.Reconcile(Snapshot{"repo-head": "git:b"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Impact{
		{NodeID: "build", State: StateStale},
		{NodeID: "repo-head", State: StateStale},
	}
	if !reflect.DeepEqual(got.Impacted, want) {
		t.Fatalf("impacted = %#v, want %#v", got.Impacted, want)
	}
	if cp.Nodes[0].Fingerprint != "git:a" {
		t.Fatalf("live observation overwrote canonical fingerprint: %q", cp.Nodes[0].Fingerprint)
	}
}

func TestCheckpointLegacyAndMalformedDetection(t *testing.T) {
	if _, present, err := CheckpointFromStateSnapshot(map[string]any{"legacy": true}); err != nil || present {
		t.Fatalf("legacy handoff = present:%v err:%v, want absent/nil", present, err)
	}
	_, present, err := CheckpointFromStateSnapshot(map[string]any{
		HandoffStateKey: map[string]any{
			"schema": "verified-state-checkpoint/999",
			"nodes": []any{},
			"snapshot": map[string]any{},
		},
	})
	if !present || !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("malformed checkpoint = present:%v err:%v", present, err)
	}
}
