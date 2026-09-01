package workgraph_test

// k-mission-01 tests — ADR-0008/0009 + work.schema/1.0 + kernel.budget/1.0.
//
// Freeze law under test:
//   - mission = Work with contract fields filled (one object, not a new type)
//   - mission-only states require the complete contract (fail-closed)
//   - CI Works keep frozen behavior (no forward states, no contract required)
//   - budget ledger: reservation ≤ ceiling, waiting_human pauses clock
//     (kernel-recognized only), hard-stop clamps, late bills absorbed
import (
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func missionWork(id string) *workgraph.Work {
	return &workgraph.Work{
		ID:        "work:" + strings.Repeat(id, 32)[:32],
		Objective: workgraph.Objective{Type: "custom"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"do": {ID: "do", Run: "echo hi"},
		}},
		State: workgraph.StateQueued,
		Mission: &workgraph.MissionContract{
			BudgetCeiling: &workgraph.BudgetCeiling{ComputeEUR: 5.0, WallClockH: 2},
			Verification: []workgraph.VerificationCriterion{
				{Criterion: "output/leads.csv has >=20 rows", Kind: "deterministic"},
			},
			PurposeBindings: []string{"no external email without approval"},
			KillSwitch:      "always",
		},
	}
}

func TestMissionWorkWithFullContractIsValid(t *testing.T) {
	w := missionWork("a")
	if !w.IsMission() {
		t.Fatal("work with budget ceiling must report IsMission")
	}
	if err := w.ValidateMissionWork(); err != nil {
		t.Fatalf("full contract rejected: %v", err)
	}
	if err := w.ValidateTransition(workgraph.StateRunning); err != nil {
		t.Fatalf("mission queued->running rejected: %v", err)
	}
	w.State = workgraph.StateRunning
	if err := w.ValidateTransition(workgraph.StateWaitingHuman); err != nil {
		t.Fatalf("mission running->WAITING_HUMAN rejected: %v", err)
	}
}

func TestMissionWithoutVerificationFailsClosed(t *testing.T) {
	w := missionWork("b")
	w.Mission.Verification = nil // incomplete contract
	if err := w.ValidateMissionWork(); err == nil {
		t.Fatal("incomplete mission contract accepted — ADR-0008 fail-closed broken")
	}
}

func TestMissionWithZeroBudgetFailsClosed(t *testing.T) {
	w := missionWork("c")
	w.Mission.BudgetCeiling = &workgraph.BudgetCeiling{ComputeEUR: 0, WallClockH: 0}
	if err := w.ValidateMissionWork(); err == nil {
		t.Fatal("zero budget ceiling accepted — kernel.budget ceiling law broken")
	}
}

func TestMissionUnknownVerificationKindFailsClosed(t *testing.T) {
	w := missionWork("d")
	w.Mission.Verification[0].Kind = "vibes"
	if err := w.ValidateMissionWork(); err == nil {
		t.Fatal("unknown verification kind accepted")
	}
}

func TestMissionKillSwitchRequired(t *testing.T) {
	w := missionWork("e")
	w.Mission.KillSwitch = ""
	if err := w.ValidateMissionWork(); err == nil {
		t.Fatal("mission without kill_switch accepted — ADR-0008 fail-closed broken")
	}
}

func TestCIWorkCanNeverEnterMissionStates(t *testing.T) {
	w := &workgraph.Work{
		ID:        "work:" + strings.Repeat("f", 32),
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"vet": {ID: "vet", Run: "go vet ./..."},
		}},
		State: workgraph.StateRunning,
		// Mission: nil — a CI Work
	}
	if w.IsMission() {
		t.Fatal("CI Work reported IsMission")
	}
	if err := w.ValidateTransition(workgraph.StateWaitingHuman); err == nil {
		t.Fatal("CI Work transitioned to WAITING_HUMAN — freeze law broken (kernel must not emit forward states on CI works)")
	}
	if err := w.ValidateTransition(workgraph.StateSuspended); err == nil {
		t.Fatal("CI Work transitioned to SUSPENDED")
	}
	w.State = workgraph.StateWaitingHuman // simulate an invalid persisted state
	if err := w.ValidateMissionWork(); err == nil {
		t.Fatal("CI Work in mission-only state passed ValidateMissionWork")
	}
}

func TestLegacyTransitionsUnchanged(t *testing.T) {
	w := &workgraph.Work{
		ID:        "work:" + strings.Repeat("1", 32),
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"vet": {ID: "vet", Run: "go vet ./..."},
		}},
		State: workgraph.StateQueued,
	}
	if err := w.ValidateTransition(workgraph.StateRunning); err != nil {
		t.Fatalf("legacy queued->running broken: %v", err)
	}
	w.State = workgraph.StateRunning
	if err := w.ValidateTransition(workgraph.StateVerifying); err != nil {
		t.Fatalf("legacy running->verifying broken: %v", err)
	}
	if err := w.ValidateTransition(workgraph.StateWaitingHuman); err == nil {
		t.Fatal("legacy work reached WAITING_HUMAN without mission contract")
	}
}

func TestBudgetLedgerReservationRespectsCeiling(t *testing.T) {
	b := workgraph.BudgetLedger{
		WorkID:     "work:" + strings.Repeat("2", 32),
		Ceiling:    workgraph.BudgetCeiling{ComputeEUR: 5.0},
		ClockState: "RUNNING",
	}
	if !b.CanReserve(3.0) {
		t.Fatal("3 Eur reservation under 5 EUR ceiling refused")
	}
	b.Reserved += 3.0
	if b.CanReserve(3.0) {
		t.Fatal("3 EUR additional reservation allowed past ceiling — sum(reserved) <= ceiling law broken")
	}
	if !b.CanReserve(2.0) {
		t.Fatal("2 EUR reservation (sum == ceiling) refused — boundary must be inclusive")
	}
}

func TestBudgetLedgerConsumeHardStopsAndClamps(t *testing.T) {
	b := workgraph.BudgetLedger{
		WorkID:     "work:" + strings.Repeat("3", 32),
		Ceiling:    workgraph.BudgetCeiling{ComputeEUR: 5.0},
		ClockState: "RUNNING",
	}
	if exceeded := b.Consume(4.0); exceeded {
		t.Fatal("premature hard stop at 4/5 EUR")
	}
	if exceeded := b.Consume(1.5); !exceeded {
		t.Fatal("hard stop not triggered at ceiling (4.0 + 1.5 >= 5.0)")
	}
	if b.Consumed != 5.0 {
		t.Fatalf("consumed = %v, want clamped 5.0 (operator cannot be billed past ceiling)", b.Consumed)
	}
	if b.HardStop != "compute" || b.ClockState != "STOPPED" {
		t.Fatalf("hard stop flags wrong: stop=%s clock=%s", b.HardStop, b.ClockState)
	}
	// post-stop consumption never applies
	if exceeded := b.Consume(10.0); exceeded || b.Consumed != 5.0 {
		t.Fatal("consumption after STOPPED mutated the ledger — hard-stop race law broken")
	}
}

func TestBudgetClockPauseOnlyOnKernelTransition(t *testing.T) {
	b := workgraph.BudgetLedger{
		WorkID:     "work:" + strings.Repeat("4", 32),
		Ceiling:    workgraph.BudgetCeiling{ComputeEUR: 5.0},
		ClockState: "PAUSED_WAITING_HUMAN",
	}
	// While paused, consumption must not advance the clock's cost (fair billing).
	if exceeded := b.Consume(1.0); exceeded {
		t.Fatal("paused clock reported exceeded — paused clock must not meter")
	}
	if b.Consumed != 0 {
		t.Fatalf("consumed advanced while clock paused: %v (waiting_human abuse)", b.Consumed)
	}
	b.ResumeClock()
	if b.ClockState != "RUNNING" {
		t.Fatalf("resume from human approval failed: %s", b.ClockState)
	}
	if exceeded := b.Consume(1.0); exceeded || b.Consumed != 1.0 {
		t.Fatalf("post-resume consumption wrong: %v", b.Consumed)
	}
}

func TestPauseClockCannotBeForcedByAgentClaim(t *testing.T) {
	b := workgraph.BudgetLedger{
		WorkID:     "work:" + strings.Repeat("4", 32),
		Ceiling:    workgraph.BudgetCeiling{ComputeEUR: 5.0},
		ClockState: "STOPPED", // hard-stopped
	}
	// An agent claiming "I am waiting for a human" after a hard stop must not
	// re-open the clock (freeze adversarial law: waiting_human cannot escape
	// budget semantics).
	b.PauseClock()
	b.ResumeClock()
	if b.ClockState != "STOPPED" {
		t.Fatalf("STOPPED clock re-opened to %s — budget escape", b.ClockState)
	}
}

func TestLateBillsRecordedNeverBreached(t *testing.T) {
	b := workgraph.BudgetLedger{
		WorkID:     "work:" + strings.Repeat("5", 32),
		Ceiling:    workgraph.BudgetCeiling{ComputeEUR: 5.0},
		Consumed:   5.0,
		ClockState: "STOPPED",
		HardStop:   "compute",
	}
	b.RecordLateBill(0.4, "provider billing after teardown")
	if len(b.LateBillEntries) != 1 {
		t.Fatal("late bill not recorded — operator absorption evidence missing")
	}
	if b.Consumed != 5.0 || b.ClockState != "STOPPED" {
		t.Fatalf("late bill mutated user-facing consumption/clock: consumed=%v clock=%s",
			b.Consumed, b.ClockState)
	}
}

func TestMissionSchemaFriendlyWireShape(t *testing.T) {
	// Wire shape must match work.schema/1.0 (mission.* contract fields at top
	// level are NOT used; the frozen schema validates the whole Work object —
	// we keep contract fields under "mission" so N-1 readers see unknown-field
	// tolerance, per proto.charter/1.0).
	w := missionWork("6")
	if w.Mission == nil || w.Mission.BudgetCeiling == nil {
		t.Fatal("mission contract missing")
	}
	if w.Mission.KillSwitch != "always" && w.Mission.KillSwitch != "policy" {
		t.Fatalf("kill_switch enum broken: %q", w.Mission.KillSwitch)
	}
}

func TestBudgetExhaustedResumesOnlyViaSuspend(t *testing.T) {
	w := missionWork("7")
	w.State = workgraph.StateRunning
	if err := w.ValidateTransition(workgraph.StateBudgetExhausted); err != nil {
		t.Fatalf("running->BUDGET_EXHAUSTED rejected for mission: %v", err)
	}
	w.State = workgraph.StateBudgetExhausted
	// Runtime cannot self-resume to RUNNING (ADR-0009: only human budget grant)
	if err := w.ValidateTransition(workgraph.StateRunning); err == nil {
		t.Fatal("BUDGET_EXHAUSTED self-resumed to RUNNING — budget escape law broken")
	}
	// The allowed path: human grants budget -> checkpoint resume
	if err := w.ValidateTransition(workgraph.StateSuspended); err != nil {
		t.Fatalf("BUDGET_EXHAUSTED->SUSPENDED (checkpoint resume path) rejected: %v", err)
	}
	w.State = workgraph.StateSuspended
	if err := w.ValidateTransition(workgraph.StateRunning); err != nil {
		t.Fatalf("SUSPENDED->RUNNING (resume from checkpoint) rejected: %v", err)
	}
}