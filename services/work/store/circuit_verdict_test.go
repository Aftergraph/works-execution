package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/circuitrun"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func terminalCircuitRun(t *testing.T, st *SQLiteStore) *circuitrun.Run {
	t.Helper()
	ctx := context.Background()
	w := circuitMissionWork()
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateCircuitRun(ctx, circuitInput(w.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []workgraph.State{
		workgraph.StateQueued, workgraph.StateRunning,
		workgraph.StateVerifying, workgraph.StateSucceeded,
	} {
		if _, err := st.UpdateState(ctx, w.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

func verdictInput(runID string) circuitrun.VerdictInput {
	return circuitrun.VerdictInput{
		CircuitRunID: runID, Result: "ACCEPT",
		VerifierID: "sentinel:test", EvidenceRef: "dvr_test",
		VerifiedAt: time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC),
	}
}
func TestCreateCircuitVerdictDerivesExactSubject(t *testing.T) {
	st, _ := openBrainStore(t)
	run := terminalCircuitRun(t, st)
	got, err := st.CreateCircuitVerdict(context.Background(), verdictInput(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	wantSubject, err := circuitrun.Subject(*run)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != wantSubject || got.CircuitSpecSHA256 != run.CircuitSpecSHA256 {
		t.Fatalf("subject mismatch: %+v run=%+v", got, run)
	}
	stored, err := st.GetCircuitVerdict(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Subject != got.Subject || stored.EvidenceRef != got.EvidenceRef {
		t.Fatalf("round trip mismatch: %+v != %+v", stored, got)
	}
}

func TestCreateCircuitVerdictRequiresTerminalWork(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	w := circuitMissionWork()
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateCircuitRun(ctx, circuitInput(w.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateCircuitVerdict(ctx, verdictInput(run.ID)); !errors.Is(err, ErrCircuitVerdictWorkNotTerminal) {
		t.Fatalf("non-terminal verdict: %v", err)
	}
}
func TestCreateCircuitVerdictIsIdempotentAndImmutable(t *testing.T) {
	st, _ := openBrainStore(t)
	run := terminalCircuitRun(t, st)
	ctx := context.Background()
	in := verdictInput(run.ID)
	first, err := st.CreateCircuitVerdict(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateCircuitVerdict(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Subject != second.Subject || !first.VerifiedAt.Equal(second.VerifiedAt) {
		t.Fatalf("idempotent retry changed verdict: %+v %+v", first, second)
	}
	in.EvidenceRef = "dvr_other"
	if _, err := st.CreateCircuitVerdict(ctx, in); !errors.Is(err, ErrCircuitVerdictConflict) {
		t.Fatalf("conflicting re-attestation: %v", err)
	}
}

func TestGetCircuitVerdictRejectsStoredSubjectDrift(t *testing.T) {
	st, _ := openBrainStore(t)
	run := terminalCircuitRun(t, st)
	ctx := context.Background()
	if _, err := st.CreateCircuitVerdict(ctx, verdictInput(run.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE circuit_verdicts SET subject = ? WHERE circuit_run_id = ?`,
		"circuit-run:tampered:spec-sha256:"+strings.Repeat("0", 64), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCircuitVerdict(ctx, run.ID); !errors.Is(err, ErrCircuitVerdictSubjectMismatch) {
		t.Fatalf("tampered subject: %v", err)
	}
}
func TestCircuitVerdictDoesNotPromoteWorkVerification(t *testing.T) {
	st, _ := openBrainStore(t)
	run := terminalCircuitRun(t, st)
	ctx := context.Background()
	if _, err := st.CreateCircuitVerdict(ctx, verdictInput(run.ID)); err != nil {
		t.Fatal(err)
	}
	workVerdict, err := st.GetVerificationVerdict(ctx, run.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if workVerdict != nil {
		t.Fatalf("CircuitVerdict must not create Work VerificationVerdict: %+v", workVerdict)
	}
}
func TestCircuitVerdictPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/works.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run := terminalCircuitRun(t, st)
	ctx := context.Background()
	want, err := st.CreateCircuitVerdict(ctx, verdictInput(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetCircuitVerdict(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != want.Subject || got.CircuitSpecSHA256 != want.CircuitSpecSHA256 ||
		got.Result != want.Result || got.VerifierID != want.VerifierID || got.EvidenceRef != want.EvidenceRef ||
		!got.VerifiedAt.Equal(want.VerifiedAt) {
		t.Fatalf("restart changed verdict: got=%+v want=%+v", got, want)
	}
}
