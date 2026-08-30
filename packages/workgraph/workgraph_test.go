package workgraph_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// helper: a minimal valid work for testing
func mustWork(t *testing.T, nodes map[string]workgraph.Node) *workgraph.Work {
	t.Helper()
	if nodes == nil {
		nodes = map[string]workgraph.Node{
			"a": {ID: "a", Run: "echo a"},
		}
	}
	w := &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:  workgraph.Graph{Nodes: nodes},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("invalid test fixture: %v", err)
	}
	return w
}

func TestNewID_UniqueAndPrefixed(t *testing.T) {
	a := workgraph.NewID("wrk")
	b := workgraph.NewID("wrk")
	if a == b {
		t.Fatalf("NewID returned duplicate: %s", a)
	}
	if len(a) < len("wrk_")+16 {
		t.Fatalf("NewID too short: %q", a)
	}
	if a[:4] != "wrk_" {
		t.Fatalf("NewID missing prefix: %q", a)
	}
}

func TestState_IsTerminal(t *testing.T) {
	cases := map[workgraph.State]bool{
		workgraph.StateCreated:   false,
		workgraph.StatePlanning:  false,
		workgraph.StateQueued:    false,
		workgraph.StateRunning:   false,
		workgraph.StateVerifying: false,
		workgraph.StateSucceeded: true,
		workgraph.StateFailed:    true,
		workgraph.StateCancelled: true,
		workgraph.StateBlocked:   false,
	}
	for s, want := range cases {
		if got := s.IsTerminal(); got != want {
			t.Errorf("State(%s).IsTerminal() = %v, want %v", s, got, want)
		}
	}
}

func TestCanTransition_HappyPath(t *testing.T) {
	chain := []workgraph.State{
		workgraph.StateCreated, workgraph.StatePlanning, workgraph.StateQueued,
		workgraph.StateRunning, workgraph.StateVerifying, workgraph.StateSucceeded,
	}
	for i := 0; i < len(chain)-1; i++ {
		if !workgraph.CanTransition(chain[i], chain[i+1]) {
			t.Errorf("expected %s -> %s allowed", chain[i], chain[i+1])
		}
	}
}

func TestCanTransition_CancelFromAnyNonTerminal(t *testing.T) {
	nonTerminal := []workgraph.State{
		workgraph.StateCreated, workgraph.StatePlanning, workgraph.StateQueued,
		workgraph.StateRunning, workgraph.StateVerifying, workgraph.StateBlocked,
	}
	for _, s := range nonTerminal {
		if !workgraph.CanTransition(s, workgraph.StateCancelled) {
			t.Errorf("expected %s -> CANCELLED allowed", s)
		}
	}
}

func TestCanTransition_NoTransitionFromTerminal(t *testing.T) {
	terminal := []workgraph.State{workgraph.StateSucceeded, workgraph.StateFailed, workgraph.StateCancelled}
	for _, s := range terminal {
		for _, to := range []workgraph.State{
			workgraph.StateCreated, workgraph.StatePlanning, workgraph.StateQueued,
			workgraph.StateRunning, workgraph.StateVerifying, workgraph.StateSucceeded,
			workgraph.StateFailed, workgraph.StateCancelled, workgraph.StateBlocked,
		} {
			if workgraph.CanTransition(s, to) {
				t.Errorf("expected %s -> %s to be denied (terminal)", s, to)
			}
		}
	}
}

func TestCanTransition_NoBackwardJumps(t *testing.T) {
	if workgraph.CanTransition(workgraph.StateRunning, workgraph.StateQueued) {
		t.Error("RUNNING -> QUEUED should be denied")
	}
	if workgraph.CanTransition(workgraph.StateVerifying, workgraph.StateRunning) {
		t.Error("VERIFYING -> RUNNING should be denied")
	}
}

func TestValidate_MissingID(t *testing.T) {
	w := &workgraph.Work{
		State:     workgraph.StateCreated,
		Objective: workgraph.Objective{Type: "x"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := w.Validate(); err == nil {
		t.Error("expected error for missing ID")
	}
}

func TestValidate_MissingObjectiveType(t *testing.T) {
	w := &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := w.Validate(); err == nil {
		t.Error("expected error for missing objective.type")
	}
}

func TestValidate_NoNodes(t *testing.T) {
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Objective: workgraph.Objective{Type: "x"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{}},
	}
	if err := w.Validate(); err == nil {
		t.Error("expected error for empty graph")
	}
}

func TestValidate_NodeRunMissing(t *testing.T) {
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Objective: workgraph.Objective{Type: "x"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"a": {ID: "a"},
		}},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "run is required") {
		t.Errorf("expected 'run is required' error, got %v", err)
	}
}

func TestValidate_DanglingDependency(t *testing.T) {
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Objective: workgraph.Objective{Type: "x"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"a": {ID: "a", Run: "true", Needs: []string{"b"}},
		}},
	}
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Errorf("expected 'not declared' error, got %v", err)
	}
}

func TestValidateTransition_Denied(t *testing.T) {
	w := mustWork(t, nil)
	w.State = workgraph.StateSucceeded
	err := w.ValidateTransition(workgraph.StateRunning)
	if err == nil || !errors.Is(err, workgraph.ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestReadyNodes_AllowsEntryNodes(t *testing.T) {
	w := mustWork(t, map[string]workgraph.Node{
		"a": {ID: "a", Run: "true"},
		"b": {ID: "b", Run: "true"},
		"c": {ID: "c", Run: "true", Needs: []string{"a", "b"}},
	})
	w.State = workgraph.StateQueued
	ready := w.ReadyNodes()
	if !containsSlice(ready, "a") || !containsSlice(ready, "b") {
		t.Errorf("expected a,b in ready, got %v", ready)
	}
	if containsSlice(ready, "c") {
		t.Errorf("did not expect c in ready, got %v", ready)
	}
}

func TestReadyNodes_AdvancesAfterSuccess(t *testing.T) {
	w := mustWork(t, map[string]workgraph.Node{
		"a": {ID: "a", Run: "true"},
		"b": {ID: "b", Run: "true", Needs: []string{"a"}},
	})
	w.State = workgraph.StateQueued
	w.Attempts = []workgraph.Attempt{
		{ID: workgraph.NewID("att"), NodeID: "a", Status: "succeeded"},
	}
	ready := w.ReadyNodes()
	if !containsSlice(ready, "b") {
		t.Errorf("expected b ready after a succeeded, got %v", ready)
	}
}

func TestReadyNodes_SkipsInFlight(t *testing.T) {
	w := mustWork(t, map[string]workgraph.Node{
		"a": {ID: "a", Run: "true"},
	})
	w.State = workgraph.StateRunning
	w.Attempts = []workgraph.Attempt{
		{ID: workgraph.NewID("att"), NodeID: "a", Status: "running"},
	}
	ready := w.ReadyNodes()
	if containsSlice(ready, "a") {
		t.Errorf("did not expect a in ready while in flight, got %v", ready)
	}
}

func TestReadyNodes_NotReadyFromWrongState(t *testing.T) {
	w := mustWork(t, map[string]workgraph.Node{
		"a": {ID: "a", Run: "true"},
	})
	w.State = workgraph.StateCreated
	ready := w.ReadyNodes()
	if len(ready) != 0 {
		t.Errorf("expected no ready nodes from CREATED, got %v", ready)
	}
}

// containsSlice reports whether sub is in ss.
func containsSlice(ss []string, sub string) bool {
	for _, s := range ss {
		if s == sub {
			return true
		}
	}
	return false
}