package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/circuitrun"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func circuitMissionWork() *workgraph.Work {
	return &workgraph.Work{
		ID: workgraph.NewID("wrk"), State: workgraph.StateCreated,
		Source:       workgraph.Source{Type: "test", Repository: "Aftergraph/test", Revision: "abc"},
		Objective:    workgraph.Objective{Type: "circuit_test"},
		Graph:        workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "echo a"}}},
		Requirements: workgraph.Requirements{OS: "linux", Arch: "amd64"},
		Policy:       workgraph.Policy{ForkPolicy: "deny", TrustClass: "standard"},
		Mission:      &workgraph.MissionContract{BudgetCeiling: &workgraph.BudgetCeiling{ComputeEUR: 1, WallClockH: 1}},
	}
}

func circuitInput(workID string) circuitrun.Input {
	return circuitrun.Input{CircuitID: "repair", WorkID: workID, MissionID: "mis_circuit_test",
		CircuitSpec: json.RawMessage(`{"mode":"CONSEQUENTIAL","schema_version":"circuit-spec/0.1","circuit_id":"repair","consequential":true,"nodes":[],"edges":[]}`)}
}

func TestCreateCircuitRunRoundTripsExactSubject(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	w := circuitMissionWork()
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	created, err := st.CreateCircuitRun(ctx, circuitInput(w.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "crun_") || len(created.CircuitSpecSHA256) != 64 {
		t.Fatalf("bad identity: %+v", created)
	}
	byID, err := st.GetCircuitRun(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	byWork, err := st.GetCircuitRunByWorkID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.ID != created.ID || byWork.ID != created.ID {
		t.Fatalf("round trip mismatch: %+v %+v", byID, byWork)
	}
	if string(byID.CircuitSpec) != string(created.CircuitSpec) {
		t.Fatalf("subject bytes changed: %s != %s", byID.CircuitSpec, created.CircuitSpec)
	}
}

func TestCreateCircuitRunRejectsUnknownOrNonMissionWork(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	if _, err := st.CreateCircuitRun(ctx, circuitInput("wrk_missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown work: %v", err)
	}
	w := circuitMissionWork()
	w.Mission = nil
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateCircuitRun(ctx, circuitInput(w.ID)); !errors.Is(err, ErrCircuitRunWorkNotMission) {
		t.Fatalf("non-mission: %v", err)
	}
}

func TestCreateCircuitRunIsIdempotentForExactBinding(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	w := circuitMissionWork()
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	in := circuitInput(w.ID)
	first, err := st.CreateCircuitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateCircuitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("retry changed binding: %+v %+v", first, second)
	}
}

func TestCreateCircuitRunRejectsConflictingRebind(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	w := circuitMissionWork()
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	in := circuitInput(w.ID)
	if _, err := st.CreateCircuitRun(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.CircuitSpec = json.RawMessage(`{"schema_version":"circuit-spec/0.1","circuit_id":"repair","mode":"CONSEQUENTIAL","consequential":true,"nodes":[{"operator_id":"x"}],"edges":[]}`)
	if _, err := st.CreateCircuitRun(ctx, in); !errors.Is(err, ErrCircuitRunConflict) {
		t.Fatalf("spec rebind: %v", err)
	}
	in = circuitInput(w.ID)
	in.MissionID = "mis_other"
	if _, err := st.CreateCircuitRun(ctx, in); !errors.Is(err, ErrCircuitRunConflict) {
		t.Fatalf("mission rebind: %v", err)
	}
}
