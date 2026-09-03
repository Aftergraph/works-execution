package audit

// Tests for the ADR-0024 obslaw wiring on the event side (k-052).
//
// Note: these tests open the work_audit_events table with a minimal
// inline DDL instead of importing services/work/store, because store
// imports this package (it consumes audit.Emitter) and a same-package
// test importing it back would be an import cycle.

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, same as production code

	"github.com/JonasAbde/works-execution/packages/obslaw"
)

func TestLawRecordProjection(t *testing.T) {
	rec, err := LawRecord(NewEvent(TypeWorkStateChanged, "work-1"))
	if err != nil {
		t.Fatalf("LawRecord: %v", err)
	}
	if rec.Kind != obslaw.KindEvent {
		t.Errorf("Kind = %q, want %q", rec.Kind, obslaw.KindEvent)
	}
	if rec.Signed {
		t.Error("Signed = true, want false (events can never be signed)")
	}
	if !rec.Trimmable {
		t.Error("Trimmable = false, want true (events are disposable by convention)")
	}
	if rec.CitesHash != "" {
		t.Errorf("CitesHash = %q, want empty (CloudEvent ids are 32-hex random, not sha256 digests)", rec.CitesHash)
	}
	if err := rec.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	want, err := obslaw.NewEvent("")
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if *rec != *want {
		t.Errorf("projection = %+v, want %+v", *rec, *want)
	}
}

func TestLawRecordNilEvent(t *testing.T) {
	if _, err := LawRecord(nil); err == nil {
		t.Fatal("LawRecord(nil) = nil error, want error")
	}
	if err := CheckEvent(nil); err == nil {
		t.Fatal("CheckEvent(nil) = nil error, want error")
	}
}

func TestCheckEventPassesForEveryNewEvent(t *testing.T) {
	types := []string{
		TypeWorkCreated,
		TypeWorkStateChanged,
		TypeWorkAttemptEnded,
		"com.works-execution.custom.whatever",
	}
	for _, typ := range types {
		e := NewEvent(typ, "work-42")
		if err := CheckEvent(e); err != nil {
			t.Errorf("CheckEvent(%s) = %v, want nil", typ, err)
		}
		// Mutating event fields must not change the law verdict: the
		// projection is a category assertion, not a content one.
		e.ID = ""
		e.Data = map[string]any{"x": 1}
		if err := CheckEvent(e); err != nil {
			t.Errorf("CheckEvent(%s, mutated fields) = %v, want nil", typ, err)
		}
	}
}

func TestEventLawTeethAgainstSignedEventsRefactor(t *testing.T) {
	// The drift scenario CheckEvent/Emit guard against: a future
	// refactor that lets events carry signatures. The kernel rejects
	// the very Record shape such a refactor would produce.
	signedEvent := &obslaw.Record{Kind: obslaw.KindEvent, Signed: true, Trimmable: true}
	if err := signedEvent.Validate(); !errors.Is(err, obslaw.ErrEventCannotBeSigned) {
		t.Fatalf("signed event Validate = %v, want ErrEventCannotBeSigned", err)
	}
	if _, err := obslaw.NewEvidence(true, ""); err != nil {
		t.Fatalf("sanity NewEvidence: %v", err)
	}
}

func newTestEmitter(t *testing.T) *SQLiteEmitter {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS work_audit_events (
    id             TEXT PRIMARY KEY,
    occurred_at    TEXT NOT NULL,
    source         TEXT NOT NULL,
    type           TEXT NOT NULL,
    subject        TEXT,
    work_id        TEXT,
    from_state     TEXT,
    to_state       TEXT,
    correlation_id TEXT,
    attempt_id     TEXT,
    spec_version   TEXT NOT NULL,
    data           TEXT
);`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return NewSQLiteEmitter(db, nil)
}

func TestEmitPersistsLawCleanEvent(t *testing.T) {
	em := newTestEmitter(t)
	ctx := context.Background()
	ev := NewEvent(TypeWorkStateChanged, "work-7")
	ev.WorkID = "work-7"
	ev.Data = StateTransitionData{WorkID: "work-7", FromState: "RUNNING", ToState: "SUCCEEDED"}
	if err := em.Emit(ctx, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	var n int
	if err := em.DB.QueryRow(`SELECT COUNT(*) FROM work_audit_events`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
}

func TestEmitPreservesExistingGuards(t *testing.T) {
	em := newTestEmitter(t)
	ctx := context.Background()
	if err := em.Emit(ctx, nil); err == nil {
		t.Error("Emit(nil) = nil error, want error")
	}
	missingID := &CloudEvent{Type: TypeWorkCreated}
	if err := em.Emit(ctx, missingID); err == nil {
		t.Error("Emit(event without ID) = nil error, want the pre-existing ID guard to fire")
	}
	missingType := &CloudEvent{ID: "evt_x"}
	if err := em.Emit(ctx, missingType); err == nil {
		t.Error("Emit(event without type) = nil error, want the pre-existing type guard to fire")
	}
}
