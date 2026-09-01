package shell_test

import (
	"testing"

	"github.com/JonasAbde/works-execution/packages/shell"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func validWorksContract() *shell.SurfaceContract {
	return &shell.SurfaceContract{
		Surface:  shell.SurfaceNOW,
		System:   shell.SystemWorks,
		Tier:     shell.TierT1Read,
		Renders:  []string{"mission rows"},
		Commands: []string{"watch", "search"},
		Executor: "works_kernel",
	}
}

func TestValidWorksContractPasses(t *testing.T) {
	if err := validWorksContract().Validate(); err != nil {
		t.Fatalf("valid works contract rejected: %v", err)
	}
}

func TestUnknownCommandFailsClosed(t *testing.T) {
	c := validWorksContract()
	c.Commands = []string{"watch", "sudo_rm_rf"}
	if err := c.Validate(); err == nil {
		t.Fatal("unknown command accepted — frozen vocabulary not enforced")
	}
}

func TestUnknownSystemFailsClosed(t *testing.T) {
	c := validWorksContract()
	c.System = shell.System("alexa")
	if err := c.Validate(); err == nil {
		t.Fatal("unknown system accepted")
	}
}

func TestUnknownExecutorFailsClosed(t *testing.T) {
	c := validWorksContract()
	c.Executor = "some_agent"
	if err := c.Validate(); err == nil {
		t.Fatal("non-kernel executor accepted — ADR-0025 executor law broken")
	}
}

func TestPulseLocalOnlyCannotExposeKill(t *testing.T) {
	c := &shell.SurfaceContract{
		Surface:  shell.SurfaceNOW,
		System:   shell.SystemPulse,
		Tier:     shell.TierLocalOnly,
		Renders:  []string{"local status"},
		Commands: []string{"watch"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("baseline pulse local_only rejected: %v", err)
	}
	c.Commands = []string{"watch", "kill"}
	if err := c.Validate(); err == nil {
		t.Fatal("pulse local_only exposed kill — shell.contracts/1.0 conditional law broken")
	}
}

func TestPulseLocalOnlyCannotExposeAnyPrivilegedCommand(t *testing.T) {
	for _, cmd := range []string{"approve", "deny", "take", "hand_back", "kill"} {
		c := &shell.SurfaceContract{
			Surface:  shell.SurfaceNOW,
			System:   shell.SystemPulse,
			Tier:     shell.TierLocalOnly,
			Renders:  []string{"x"},
			Commands: []string{cmd},
		}
		if err := c.Validate(); err == nil {
			t.Fatalf("pulse local_only exposed privileged command %q", cmd)
		}
	}
}

func TestPulseT3MustBeCommandSurface(t *testing.T) {
	c := &shell.SurfaceContract{
		Surface:  shell.SurfaceCOMMAND,
		System:   shell.SystemPulse,
		Tier:     shell.TierT3Privilege,
		Renders:  []string{"command palette"},
		Commands: []string{"approve", "deny", "kill"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("pulse T3 COMMAND rejected: %v", err)
	}
	c.Surface = shell.SurfaceNOW
	if err := c.Validate(); err == nil {
		t.Fatal("pulse T3 on NOW accepted — must be COMMAND")
	}
}

func TestNowProjectionOrdersWaitingHumanFirst(t *testing.T) {
	works := []*workgraph.Work{
		{ID: "work:aaa", State: workgraph.StateRunning, Graph: graph()},
		{ID: "work:bbb", State: workgraph.StateWaitingHuman, Graph: graph()},
		{ID: "work:ccc", State: workgraph.StateQueued, Graph: graph()},
		{ID: "work:ddd", State: workgraph.StateWaitingHuman, Graph: graph()},
	}
	rows := shell.NowProjection(works, nil)
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(rows))
	}
	want := []struct {
		id    string
		human bool
		rank  int
	}{
		{"work:bbb", true, 1},
		{"work:ddd", true, 2},
		{"work:aaa", false, 3},
		{"work:ccc", false, 4},
	}
	for i, w := range want {
		got := rows[i]
		if got.WorkID != w.id || got.NeedsHuman != w.human || got.AttentionRank != w.rank {
			t.Errorf("rank %d: got %s needsHuman=%v rank=%d, want %s human=%v rank=%d",
				i+1, got.WorkID, got.NeedsHuman, got.AttentionRank, w.id, w.human, w.rank)
		}
	}
}

func TestNowProjectionBudgetClockLaw(t *testing.T) {
	w := &workgraph.Work{ID: "work:m1", State: workgraph.StateWaitingHuman, Graph: graph()}
	ledger := &workgraph.BudgetLedger{
		WorkID:     "work:m1",
		ClockState: "PAUSED_WAITING_HUMAN",
		Ceiling:    workgraph.BudgetCeiling{ComputeEUR: 5},
		Consumed:   1.5,
	}
	rows := shell.NowProjection([]*workgraph.Work{w}, map[string]*workgraph.BudgetLedger{"work:m1": ledger})
	row := rows[0]
	if row.ClockRunning {
		t.Fatal("clock must not run while PAUSED_WAITING_HUMAN (kernel.budget law)")
	}
	if row.ClockState != "PAUSED_WAITING_HUMAN" {
		t.Fatalf("clock_state = %q, want PAUSED_WAITING_HUMAN", row.ClockState)
	}
	if row.ConsumedEUR != 1.5 || row.CeilingEUR != 5.0 {
		t.Fatalf("budget fields wrong: consumed=%v ceiling=%v", row.ConsumedEUR, row.CeilingEUR)
	}
	if !row.NeedsHuman {
		t.Fatal("WAITING_HUMAN mission must need human attention")
	}
}

func TestNowProjectionStableOrderAndNilSafety(t *testing.T) {
	rows := shell.NowProjection([]*workgraph.Work{nil, {ID: "work:z", State: workgraph.StateQueued, Graph: graph()}}, nil)
	if len(rows) != 1 || rows[0].WorkID != "work:z" {
		t.Fatalf("nil work must be skipped, got %+v", rows)
	}
}

// graph returns a minimal valid Graph for projection tests.
func graph() workgraph.Graph {
	return workgraph.Graph{Nodes: map[string]workgraph.Node{
		"n": {ID: "n", Run: "echo"},
	}}
}
