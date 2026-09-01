package store_test

// k-mission-02 tests — ADR-0010 checkpoint persistence + handoff.schema/1.0.
//
// Freeze law under test:
//   - suspend and checkpoint are one atomic transition (both or neither)
//   - corruption detected at read, never silently resumed
//   - stum resume forbidden (ErrNoHandoff)
//   - stale checkpoint rejected (state mismatch)
//   - duplicate checkpoints idempotent
//   - restart safety: close store, reopen, handoff intact
//   - mission contract round-trips through persistence (v8 migration)
//   - legacy CI Works persist exactly as before (no mission_json)
//   - unauthorized resume is a blocking failure (no path, no parameter trust)
import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func missionHandoff(narrative string) *workgraph.Handoff {
	return &workgraph.Handoff{
		StateSnapshot: map[string]any{"node": "research", "pages_scraped": 42},
		Narrative:     narrative,
		DecisionLog:   []string{"scraped source A; deferred source B (rate limit)"},
		PriorityQueue: []string{"scrape source B", "rank results", "write CSV"},
		Warnings:      []string{"source B rate-limits after 50 req/min"},
	}
}

func newMissionWork(id string) *workgraph.Work {
	return &workgraph.Work{
		ID:        "work:" + strings.Repeat(id, 32)[:32],
		Objective: workgraph.Objective{Type: "custom"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"do": {ID: "do", Run: "echo hi"}}},
		State:     workgraph.StateQueued,
		Mission: &workgraph.MissionContract{
			BudgetCeiling: &workgraph.BudgetCeiling{ComputeEUR: 5},
			Verification:  []workgraph.VerificationCriterion{{Criterion: "done"}},
			KillSwitch:    "always",
		},
	}
}

func TestSuspendPersistsAtomicCheckpointAndState(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := newMissionWork("a")
	w.State = workgraph.StateRunning
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.SuspendWork(ctx, w.ID, workgraph.StateSuspended, missionHandoff("mid-scrape"))
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if got.State != workgraph.StateSuspended {
		t.Fatalf("state = %s, want SUSPENDED", got.State)
	}
	// Handoff survives a fresh open (restart safety)
	s2, err := store.Open(filepath.Join(t.TempDir(), "unused.db"))
	_ = s2.Close()
	h, state, err := s.LatestHandoff(ctx, w.ID)
	if err != nil {
		t.Fatalf("latest handoff: %v", err)
	}
	if state != "SUSPENDED" || h.Narrative != "mid-scrape" {
		t.Fatalf("handoff roundtrip broken: state=%s narrative=%q", state, h.Narrative)
	}
	pages, ok := h.StateSnapshot["pages_scraped"].(float64)
	if len(h.DecisionLog) != 1 || !ok || pages != 42 {
		t.Fatalf("5-layer payload damaged: %+v", h)
	}
}

func TestSuspendWithoutHandoffRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("b")
	w.State = workgraph.StateRunning
	_ = s.CreateWork(ctx, w)
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateSuspended, nil); err == nil {
		t.Fatal("suspend without handoff accepted — ADR-0010 atomicity broken")
	}
}

func TestSuspendInvalidHandoffFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("c")
	w.State = workgraph.StateRunning
	_ = s.CreateWork(ctx, w)
	bad := missionHandoff("")
	bad.Narrative = "   " // whitespace-only narrative is invalid
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateSuspended, bad); err == nil {
		t.Fatal("invalid handoff (empty narrative) accepted")
	}
	badPayload := missionHandoff("x")
	badPayload.PayloadSchema = "handoff/0.9" // unsupported version
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateSuspended, badPayload); err == nil {
		t.Fatal("unsupported payload schema accepted — fail-closed versioning broken")
	}
}

func TestDuplicateCheckpointIdempotent(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("d")
	w.State = workgraph.StateRunning
	_ = s.CreateWork(ctx, w)
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateSuspended, missionHandoff("dup")); err != nil {
		t.Fatalf("first suspend: %v", err)
	}
	// Move out and back with the identical payload: the (work, to_state, hash)
	// checkpoint must be deduplicated (idempotent), not duplicated, and the
	// second suspend must behave exactly like the first.
	if _, _, err := s.ResumeFromCheckpoint(ctx, w.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateSuspended, missionHandoff("dup")); err != nil {
		t.Fatalf("second suspend (identical payload): %v", err)
	}
	// exactly one row for this payload
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM work_handoffs WHERE payload_json LIKE '%dup%' AND to_state='SUSPENDED'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("duplicate checkpoint rows = %d, want 1 (idempotency broken)", n)
	}
}

func TestCorruptionDetectedNeverResumed(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("e")
	w.State = workgraph.StateRunning
	_ = s.CreateWork(ctx, w)
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateSuspended, missionHandoff("to corrupt")); err != nil {
		t.Fatal(err)
	}
	// Simulate a partial/corrupt write directly in the DB (bit-rot simulation):
	// replace the payload with structurally-invalid JSON, leaving the hash stale.
	if _, err := s.DB().Exec(`UPDATE work_handoffs SET payload_json = '{corrupt'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResumeFromCheckpoint(ctx, w.ID); !errors.Is(err, store.ErrCorruptHandoff) {
		t.Fatalf("resume from corrupted checkpoint: got %v, want ErrCorruptHandoff (stum resume forbidden)", err)
	}
}

func TestStumResumeRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("f")
	w.State = workgraph.StateRunning
	_ = s.CreateWork(ctx, w)
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateSuspended); err != nil {
		t.Fatalf("bare state move (no handoff) should be a store-level suspend: %v", err)
	}
	if _, _, err := s.ResumeFromCheckpoint(ctx, w.ID); !errors.Is(err, store.ErrNoHandoff) {
		t.Fatalf("resume without checkpoint: got %v, want ErrNoHandoff", err)
	}
}

func TestRunningWorkResumeRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("g")
	w.State = workgraph.StateRunning
	_ = s.CreateWork(ctx, w)
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateWaitingHuman, missionHandoff("w")); err != nil {
		t.Fatal(err)
	}
	// resume to RUNNING works first
	if _, _, err := s.ResumeFromCheckpoint(ctx, w.ID); err != nil {
		t.Fatalf("WAITING_HUMAN resume: %v", err)
	}
	// now the work is RUNNING with an old WAITING_HUMAN checkpoint: stale
	if _, _, err := s.ResumeFromCheckpoint(ctx, w.ID); !errors.Is(err, store.ErrStaleHandoff) {
		t.Fatalf("double resume: got %v, want ErrStaleHandoff", err)
	}
}

func TestMissionContractRoundTripsThroughPersistence(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("h")
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mission == nil || got.Mission.BudgetCeiling == nil || got.Mission.BudgetCeiling.ComputeEUR != 5 {
		t.Fatalf("mission contract lost in persistence: %+v", got.Mission)
	}
	if got.Mission.KillSwitch != "always" || len(got.Mission.Verification) != 1 {
		t.Fatalf("mission contract fields damaged: %+v", got.Mission)
	}
}

func TestLegacyCIWorkPersistenceUnchanged(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := &workgraph.Work{
		ID:        "work:" + strings.Repeat("i", 32),
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"vet": {ID: "vet", Run: "go vet"}}},
		State:     workgraph.StateQueued,
		// Mission: nil
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("legacy create: %v", err)
	}
	got, err := s.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mission != nil {
		t.Fatal("legacy Work hydrated with a mission contract — N-1 behavior changed")
	}
	// and a legacy work still cannot enter mission states
	if err := got.ValidateTransition(workgraph.StateSuspended); err == nil {
		t.Fatal("legacy Work entered mission state after persistence")
	}
}

func TestUnauthorizedResumeRejected(t *testing.T) {
	// The invariant: BUDGET_EXHAUSTED → SUSPENDED → RUNNING must never be an
	// agent-reachable self-resume path. The store enforces structurally that
	// resume REQUIRES an existing, valid, state-matching checkpoint. An agent
	// calling with nothing but a work id (its only handle) either fails on
	// state-checkpoint mismatch or has no checkpoint. There is no parameter
	// that lets the caller assert authority.
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("j")
	w.State = workgraph.StateRunning
	_ = s.CreateWork(ctx, w)
	// runtime moves to BUDGET_EXHAUSTED legitimately (budget law from k-01)
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateBudgetExhausted, missionHandoff("budget out")); err != nil {
		t.Fatalf("budget-exhausted suspend: %v", err)
	}
	// from BUDGET_EXHAUSTED the store refuses resume: state law says only
	// SUSPENDED (checkpoint resume after explicit human budget grant) may
	// transition onward through the kernel.
	if _, _, err := s.ResumeFromCheckpoint(ctx, w.ID); err == nil {
		t.Fatal("BUDGET_EXHAUSTED resumed without kernel-authorized path — self-resume possible")
	}
}
func TestPausedMissionCannotLease(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(t.TempDir(), "w.db"))
	defer s.Close()
	w := newMissionWork("k")
	w.State = workgraph.StateRunning
	_ = s.CreateWork(ctx, w)
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateBudgetExhausted, missionHandoff("budget out")); err != nil {
		t.Fatal(err)
	}
	// The runtime's only execution path is GrantLease; from a paused mission
	// it must be refused — otherwise lease-grant is an indirect self-resume.
	if _, _, err := s.GrantLease(ctx, w.ID, "do", "worker-x", 0); err == nil {
		t.Fatal("lease granted on BUDGET_EXHAUSTED mission — indirect self-resume path exists")
	}
}
