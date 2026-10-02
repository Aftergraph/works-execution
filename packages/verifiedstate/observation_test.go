package verifiedstate

import (
	"errors"
	"reflect"
	"testing"
)

func TestObservedCheckpointRequiresOneBindingPerMonitoredSubject(t *testing.T) {
	nodes := []Node{
		verifiedNode("repo-head", "git:Aftergraph/runtime@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		verifiedNode("build", "build:a"),
	}
	cp, err := NewObservedCheckpoint(
		nodes,
		[]Edge{{From: "repo-head", To: "build", Type: EdgeData}},
		Snapshot{"repo-head": "git:Aftergraph/runtime@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		[]ObservationBinding{{
			NodeID:  "repo-head",
			Kind:    ObservationGitRef,
			Locator: "github:Aftergraph/runtime#refs/heads/main",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Schema != CheckpointSchemaObserved {
		t.Fatalf("schema = %s", cp.Schema)
	}
	if err := cp.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestObservedCheckpointRejectsMissingDuplicateAndUnknownBindings(t *testing.T) {
	nodes := []Node{
		verifiedNode("repo-head", "git:a"),
		verifiedNode("authority", "auth:a"),
	}
	snapshot := Snapshot{"repo-head": "git:a", "authority": "auth:a"}

	cases := []struct {
		name     string
		bindings []ObservationBinding
	}{
		{
			name: "missing",
			bindings: []ObservationBinding{{
				NodeID: "repo-head", Kind: ObservationGitRef, Locator: "github:Aftergraph/runtime#refs/heads/main",
			}},
		},
		{
			name: "duplicate",
			bindings: []ObservationBinding{
				{NodeID: "repo-head", Kind: ObservationGitRef, Locator: "github:Aftergraph/runtime#refs/heads/main"},
				{NodeID: "repo-head", Kind: ObservationGitRef, Locator: "github:Aftergraph/runtime#refs/heads/main"},
			},
		},
		{
			name: "unknown-kind",
			bindings: []ObservationBinding{
				{NodeID: "repo-head", Kind: ObservationKind("MAGIC"), Locator: "x"},
				{NodeID: "authority", Kind: ObservationAuthority, Locator: "aie:auth_1"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewObservedCheckpoint(nodes, nil, snapshot, tc.bindings)
			if !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
			}
		})
	}
}

func TestV01CheckpointRejectsObservationBindings(t *testing.T) {
	cp, err := NewCheckpoint(
		[]Node{verifiedNode("repo-head", "git:a")},
		nil,
		Snapshot{"repo-head": "git:a"},
	)
	if err != nil {
		t.Fatal(err)
	}
	cp.Observations = []ObservationBinding{{
		NodeID: "repo-head", Kind: ObservationGitRef, Locator: "github:Aftergraph/runtime#refs/heads/main",
	}}
	if err := cp.Validate(); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
	}
}

func TestObservedCheckpointRoundTripsThroughHandoffState(t *testing.T) {
	cp, err := NewObservedCheckpoint(
		[]Node{verifiedNode("repo-head", "git:a")},
		nil,
		Snapshot{"repo-head": "git:a"},
		[]ObservationBinding{{
			NodeID: "repo-head", Kind: ObservationGitRef, Locator: "github:Aftergraph/runtime#refs/heads/main",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{}
	if err := AttachToStateSnapshot(state, cp); err != nil {
		t.Fatal(err)
	}
	got, present, err := CheckpointFromStateSnapshot(state)
	if err != nil || !present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if !reflect.DeepEqual(got.Observations, cp.Observations) {
		t.Fatalf("observations = %#v, want %#v", got.Observations, cp.Observations)
	}
}
