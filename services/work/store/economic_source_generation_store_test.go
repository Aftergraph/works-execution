package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func sourceGenerationFixture() EconomicSourceGenerationState {
	return EconomicSourceGenerationState{
		SourceClass:    "registry",
		SourceID:       "registry_1",
		Generation:     11,
		CursorKind:     "version",
		CursorValue:    "11",
		ObservedAtUnix:  1_999_900,
		EvidenceHash:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecordDigest:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CaptureHash:    "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
}

func openEconomicSourceStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "works.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st, path
}

func TestEconomicSourceGenerationSchemaV15(t *testing.T) {
	st, _ := openEconomicSourceStore(t)
	defer st.Close()

	var got int
	if err := st.db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&got); err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if got != 15 || SchemaVersion != 15 {
		t.Fatalf("expected schema v15, got ledger=%d const=%d", got, SchemaVersion)
	}
	var table string
	if err := st.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='economic_source_generations'").Scan(&table); err != nil {
		t.Fatalf("economic source table missing: %v", err)
	}
}

func TestEconomicSourceGenerationAdvancesAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	st, path := openEconomicSourceStore(t)
	first := sourceGenerationFixture()
	tr, err := st.AdvanceEconomicSourceGeneration(ctx, first)
	if err != nil { t.Fatal(err) }
	if tr.Decision != "ADVANCED" || tr.PriorGeneration != 0 || tr.Current.Revision != 1 {
		t.Fatalf("transition=%+v", tr)
	}
	if tr.Current.StateDigest == "" || tr.ExternalEffects != 0 {
		t.Fatalf("transition=%+v", tr)
	}

	second := first
	second.Generation = 12
	second.CursorValue = "12"
	second.ObservedAtUnix = 1_999_910
	second.EvidenceHash = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	second.RecordDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	second.CaptureHash = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	tr, err = st.AdvanceEconomicSourceGeneration(ctx, second)
	if err != nil { t.Fatal(err) }
	if tr.Decision != "ADVANCED" || tr.PriorGeneration != 11 || tr.Current.Revision != 2 {
		t.Fatalf("transition=%+v", tr)
	}
	if err := st.Close(); err != nil { t.Fatal(err) }

	st, err = Open(path)
	if err != nil { t.Fatalf("reopen: %v", err) }
	defer st.Close()
	got, err := st.GetEconomicSourceGeneration(ctx, "registry", "registry_1")
	if err != nil { t.Fatal(err) }
	if got == nil || got.Generation != 12 || got.Revision != 2 || got.CaptureHash != second.CaptureHash {
		t.Fatalf("got=%+v", got)
	}
}

func TestEconomicSourceGenerationExactReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, _ := openEconomicSourceStore(t)
	defer st.Close()
	first := sourceGenerationFixture()
	if _, err := st.AdvanceEconomicSourceGeneration(ctx, first); err != nil { t.Fatal(err) }
	tr, err := st.AdvanceEconomicSourceGeneration(ctx, first)
	if err != nil { t.Fatal(err) }
	if tr.Decision != "IDEMPOTENT_REPLAY" || tr.Current.Revision != 1 {
		t.Fatalf("transition=%+v", tr)
	}
}

func TestEconomicSourceGenerationRejectsRegressionAndEquivocation(t *testing.T) {
	ctx := context.Background()
	st, _ := openEconomicSourceStore(t)
	defer st.Close()
	first := sourceGenerationFixture()
	if _, err := st.AdvanceEconomicSourceGeneration(ctx, first); err != nil { t.Fatal(err) }

	regressed := first
	regressed.Generation = 10
	if _, err := st.AdvanceEconomicSourceGeneration(ctx, regressed); !errors.Is(err, ErrEconomicSourceGenerationRegression) {
		t.Fatalf("expected regression, got %v", err)
	}

	equivocated := first
	equivocated.EvidenceHash = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, err := st.AdvanceEconomicSourceGeneration(ctx, equivocated); !errors.Is(err, ErrEconomicSourceEquivocation) {
		t.Fatalf("expected equivocation, got %v", err)
	}
}

func TestEconomicSourceGenerationRejectsTimeRollbackAndCursorKindDrift(t *testing.T) {
	ctx := context.Background()
	st, _ := openEconomicSourceStore(t)
	defer st.Close()
	first := sourceGenerationFixture()
	if _, err := st.AdvanceEconomicSourceGeneration(ctx, first); err != nil { t.Fatal(err) }

	timeRollback := first
	timeRollback.Generation = 12
	timeRollback.CursorValue = "12"
	timeRollback.ObservedAtUnix = first.ObservedAtUnix - 1
	if _, err := st.AdvanceEconomicSourceGeneration(ctx, timeRollback); !errors.Is(err, ErrEconomicSourceTimeRegression) {
		t.Fatalf("expected time regression, got %v", err)
	}

	cursorDrift := first
	cursorDrift.Generation = 12
	cursorDrift.CursorKind = "offset"
	cursorDrift.CursorValue = "12"
	cursorDrift.ObservedAtUnix = first.ObservedAtUnix + 1
	if _, err := st.AdvanceEconomicSourceGeneration(ctx, cursorDrift); !errors.Is(err, ErrEconomicSourceCursorKindChange) {
		t.Fatalf("expected cursor-kind change, got %v", err)
	}
}

func TestEconomicSourceGenerationRejectsCorruptPersistedDigest(t *testing.T) {
	ctx := context.Background()
	st, _ := openEconomicSourceStore(t)
	defer st.Close()
	first := sourceGenerationFixture()
	if _, err := st.AdvanceEconomicSourceGeneration(ctx, first); err != nil { t.Fatal(err) }
	if _, err := st.db.Exec("UPDATE economic_source_generations SET state_digest='sha256:0000000000000000000000000000000000000000000000000000000000000000' WHERE source_class='registry' AND source_id='registry_1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetEconomicSourceGeneration(ctx, "registry", "registry_1"); err == nil {
		t.Fatal("expected state-digest corruption detection")
	}
}
