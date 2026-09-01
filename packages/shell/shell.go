// Package shell implements the NOW-shell contract surface (ADR-0025,
// shell.contracts/1.0): the typed validation law for every shell surface
// and the NOW projection over kernel Work objects.
//
// NOW is the works-system's live mission view: which missions exist, what
// needs human attention first, and what the budget clock is doing. It is a
// READ surface (T1) — privileged actions (approve/deny/kill/take/hand_back)
// belong to the COMMAND surface at T3 and are out of scope here.
//
// Freeze law encoded (shell.contracts/1.0, ADR-0025):
//   - a shell contract declaration is only ever executed by works_kernel
//   - pulse + local_only must never expose kill/approve/deny/take/hand_back
//   - pulse + T3_privileged must sit on the COMMAND surface
package shell

import (
	"errors"
	"fmt"
	"sort"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// ContractVersion is the frozen shell contract this package speaks.
const ContractVersion = "contract:shell.contracts/1.0"

// Surface enumerates the frozen surface names (shell.contracts/1.0).
type Surface string

const (
	SurfaceNOW     Surface = "NOW"
	SurfaceSPACE   Surface = "SPACE"
	SurfaceFOCUS   Surface = "FOCUS"
	SurfaceLIVE    Surface = "LIVE"
	SurfaceMEMORY  Surface = "MEMORY"
	SurfaceCOMMAND Surface = "COMMAND"
	SurfaceCONTEXT Surface = "CONTEXT"
	SurfaceSWITCH  Surface = "SWITCH"
	SurfaceACT     Surface = "ACT"
	SurfaceMOUNT   Surface = "MOUNT"
	SurfaceWORKS   Surface = "WORKS"
)

// System enumerates the shell's two systems.
type System string

const (
	SystemWorks System = "works"
	SystemPulse System = "pulse"
)

// Tier enumerates the frozen capability tiers.
type Tier string

const (
	TierT1Read      Tier = "T1_read"
	TierT2Action    Tier = "T2_action"
	TierT3Privilege Tier = "T3_privileged"
	TierLocalOnly   Tier = "local_only"
	TierNone        Tier = "none"
)

// CommandsAllowed is the frozen command vocabulary (shell.contracts/1.0).
var CommandsAllowed = map[string]bool{}

// privilegedCommandLaw is the frozen conditional: these commands must never
// appear on a pulse surface at local_only tier (ADR-0025).
var privilegedCommandLaw = map[string]bool{
	"kill":      true,
	"approve":   true,
	"deny":      true,
	"take":      true,
	"hand_back": true,
}

// knownCommands mirrors the frozen enum so unknown commands fail closed.
func init() {
	for _, c := range []string{
		"watch", "approve", "deny", "tell", "stop", "kill", "resume", "run",
		"cron", "take", "hand_back", "mount", "unmount", "export", "pair",
		"unpair", "pause", "open", "switch", "pin", "note", "timer", "action",
		"search", "grant", "revoke", "inspect_evidence",
	} {
		CommandsAllowed[c] = true
	}
}

// SurfaceContract is one validated (surface, system, tier, commands) tuple
// as materialized by the frozen schema. Executor is structural: the shell
// contract is only ever executed by works_kernel, so it is not caller-
// supplied and carries no caller-controlled state.
type SurfaceContract struct {
	Surface  Surface  `json:"surface"`
	System   System   `json:"system"`
	Tier     Tier     `json:"tier,omitempty"`
	Renders  []string `json:"renders"`
	Commands []string `json:"commands"`
	Executor string   `json:"executor,omitempty"`
}

// Validate enforces the frozen shell.contracts/1.0 law fail-closed:
// unknown commands, unknown executor, pulse+local_only privilege leakage,
// and pulse+T3 off-COMMAND placement are all contract violations.
func (s *SurfaceContract) Validate() error {
	if s.Surface == "" {
		return errors.New("shell.surface is required")
	}
	if s.System != SystemWorks && s.System != SystemPulse {
		return fmt.Errorf("shell.system must be works or pulse, got %q", s.System)
	}
	if s.Executor != "" && s.Executor != "works_kernel" {
		return fmt.Errorf("shell.executor must be works_kernel, got %q", s.Executor)
	}
	for _, c := range s.Commands {
		if !CommandsAllowed[c] {
			return fmt.Errorf("shell command %q not in frozen vocabulary", c)
		}
		if s.System == SystemPulse && s.Tier == TierLocalOnly && privilegedCommandLaw[c] {
			return fmt.Errorf(
				"shell.contracts/1.0 violation: pulse surface %s at local_only tier exposes privileged command %q",
				s.Surface, c)
		}
		if s.System == SystemPulse && s.Tier == TierT3Privilege && s.Surface != SurfaceCOMMAND {
			return fmt.Errorf(
				"shell.contracts/1.0 violation: pulse T3_privileged surface %s must be COMMAND",
				s.Surface)
		}
	}
	return nil
}

// NowRow is one mission in the NOW projection: the shell's read-only live
// view over a kernel Work. It is a projection — it never mutates the Work
// and never performs privileged actions.
type NowRow struct {
	WorkID        string  `json:"work_id"`
	ObjectiveType string  `json:"objective_type"`
	State         string  `json:"state"`
	NeedsHuman    bool    `json:"needs_human"`
	AttentionRank int     `json:"attention_rank"`
	ClockState    string  `json:"clock_state"`
	ClockRunning  bool    `json:"clock_running"`
	ConsumedEUR   float64 `json:"consumed_eur"`
	CeilingEUR    float64 `json:"ceiling_eur,omitempty"`
	HardStop      string  `json:"hard_stop,omitempty"`
}

// NowProjection renders the NOW surface over the given Works and their
// budgets (nil budget => non-mission CI Work; clock fields stay zero).
//
// Ordering law (k-now-01): WAITING_HUMAN missions come first in stable work-id
// order, then RUNNING (clock running), then every other state in stable
// work-id order. Rank is 1-based; rank 1 is the top of the NOW surface.
func NowProjection(works []*workgraph.Work, ledgers map[string]*workgraph.BudgetLedger) []*NowRow {
	type rowKey struct {
		row     *NowRow
		waiting bool
		running bool
	}
	rows := make([]rowKey, 0, len(works))
	for _, w := range works {
		if w == nil {
			continue
		}
		row := &NowRow{
			WorkID:        w.ID,
			ObjectiveType: w.Objective.Type,
			State:         string(w.State),
		}
		ledger := ledgers[w.ID]
		if ledger != nil {
			row.ConsumedEUR = ledger.Consumed
			row.CeilingEUR = ledger.Ceiling.ComputeEUR
			row.ClockState = ledger.ClockState
			row.ClockRunning = ledger.ClockState == "RUNNING"
			row.HardStop = ledger.HardStop
		}
		waiting := w.State == workgraph.StateWaitingHuman
		running := w.State == workgraph.StateRunning
		row.NeedsHuman = waiting
		rows = append(rows, rowKey{row: row, waiting: waiting, running: running})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch {
		case a.waiting != b.waiting:
			return a.waiting // WAITING_HUMAN first
		case a.running != b.running:
			return a.running // then RUNNING
		default:
			return a.row.WorkID < b.row.WorkID // stable work-id order
		}
	})
	out := make([]*NowRow, 0, len(rows))
	for rank, r := range rows {
		r.row.AttentionRank = rank + 1
		out = append(out, r.row)
	}
	return out
}
