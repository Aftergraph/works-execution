package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrEconomicSourceGenerationRegression = errors.New("economic source generation regression")
	ErrEconomicSourceEquivocation         = errors.New("economic source generation equivocation")
	ErrEconomicSourceTimeRegression       = errors.New("economic source observation time regression")
	ErrEconomicSourceCursorKindChange     = errors.New("economic source cursor kind change")
	ErrEconomicSourceConcurrentAdvance    = errors.New("economic source concurrent advance")
)

const economicSourceGenerationSchema =
	"CREATE TABLE IF NOT EXISTS economic_source_generations (" +
	"source_class TEXT NOT NULL," +
	"source_id TEXT NOT NULL," +
	"generation INTEGER NOT NULL CHECK(generation > 0)," +
	"cursor_kind TEXT NOT NULL," +
	"cursor_value TEXT NOT NULL," +
	"observed_at_unix INTEGER NOT NULL CHECK(observed_at_unix > 0)," +
	"evidence_hash TEXT NOT NULL," +
	"record_digest TEXT NOT NULL," +
	"capture_hash TEXT NOT NULL," +
	"revision INTEGER NOT NULL CHECK(revision > 0)," +
	"state_digest TEXT NOT NULL," +
	"updated_at TEXT NOT NULL," +
	"PRIMARY KEY(source_class, source_id)," +
	"CHECK(source_class IN ('registry','custody','representation'))," +
	"CHECK(cursor_kind IN ('version','sequence','block','offset','etag'))" +
	");" +
	"CREATE INDEX IF NOT EXISTS idx_economic_source_generations_updated " +
	"ON economic_source_generations(updated_at);"

type EconomicSourceGenerationState struct {
	SourceClass    string
	SourceID       string
	Generation     int64
	CursorKind     string
	CursorValue    string
	ObservedAtUnix int64
	EvidenceHash   string
	RecordDigest   string
	CaptureHash    string
	Revision       int64
	StateDigest    string
}

type EconomicSourceGenerationTransition struct {
	Schema          string
	Decision        string
	PriorGeneration int64
	Current         EconomicSourceGenerationState
	ExternalEffects int
}

type EconomicSourceGenerationStore interface {
	AdvanceEconomicSourceGeneration(context.Context, EconomicSourceGenerationState) (*EconomicSourceGenerationTransition, error)
	GetEconomicSourceGeneration(context.Context, string, string) (*EconomicSourceGenerationState, error)
}

func (s *SQLiteStore) migrateEconomicSourceGenerations() error {
	_, err := s.db.Exec(economicSourceGenerationSchema)
	return err
}

func (s *SQLiteStore) EconomicSourceGenerationStore() EconomicSourceGenerationStore {
	return s
}

func validateEconomicSourceGenerationState(v EconomicSourceGenerationState) error {
	switch v.SourceClass {
	case "registry", "custody", "representation":
	default:
		return errors.New("economic source class invalid")
	}
	if strings.TrimSpace(v.SourceID) == "" {
		return errors.New("economic source id required")
	}
	if v.Generation <= 0 {
		return errors.New("economic source generation invalid")
	}
	switch v.CursorKind {
	case "version", "sequence", "block", "offset", "etag":
	default:
		return errors.New("economic source cursor kind invalid")
	}
	if strings.TrimSpace(v.CursorValue) == "" {
		return errors.New("economic source cursor value required")
	}
	if v.ObservedAtUnix <= 0 {
		return errors.New("economic source observation time invalid")
	}
	for name, value := range map[string]string{
		"evidence_hash": v.EvidenceHash,
		"record_digest": v.RecordDigest,
		"capture_hash":  v.CaptureHash,
	} {
		if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
			return fmt.Errorf("economic source %s invalid", name)
		}
		if _, err := hex.DecodeString(value[len("sha256:"):]); err != nil {
			return fmt.Errorf("economic source %s invalid", name)
		}
	}
	return nil
}

func economicSourceStateDigest(v EconomicSourceGenerationState) string {
	parts := []string{
		v.SourceClass,
		v.SourceID,
		strconv.FormatInt(v.Generation, 10),
		v.CursorKind,
		v.CursorValue,
		strconv.FormatInt(v.ObservedAtUnix, 10),
		v.EvidenceHash,
		v.RecordDigest,
		v.CaptureHash,
		strconv.FormatInt(v.Revision, 10),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func exactEconomicSourceReplay(a, b EconomicSourceGenerationState) bool {
	return a.SourceClass == b.SourceClass &&
		a.SourceID == b.SourceID &&
		a.Generation == b.Generation &&
		a.CursorKind == b.CursorKind &&
		a.CursorValue == b.CursorValue &&
		a.ObservedAtUnix == b.ObservedAtUnix &&
		a.EvidenceHash == b.EvidenceHash &&
		a.RecordDigest == b.RecordDigest &&
		a.CaptureHash == b.CaptureHash
}

func (s *SQLiteStore) AdvanceEconomicSourceGeneration(
	ctx context.Context,
	next EconomicSourceGenerationState,
) (*EconomicSourceGenerationTransition, error) {
	if err := validateEconomicSourceGenerationState(next); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("economic source generation begin: %w", err)
	}
	defer tx.Rollback()

	current, err := loadEconomicSourceGenerationTx(ctx, tx, next.SourceClass, next.SourceID)
	if err != nil {
		return nil, err
	}

	if current == nil {
		next.Revision = 1
		next.StateDigest = economicSourceStateDigest(next)
		_, err := tx.ExecContext(ctx,
			"INSERT INTO economic_source_generations "+
				"(source_class, source_id, generation, cursor_kind, cursor_value, observed_at_unix, "+
				"evidence_hash, record_digest, capture_hash, revision, state_digest, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			next.SourceClass, next.SourceID, next.Generation, next.CursorKind, next.CursorValue,
			next.ObservedAtUnix, next.EvidenceHash, next.RecordDigest, next.CaptureHash,
			next.Revision, next.StateDigest, time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return nil, fmt.Errorf("economic source generation insert: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("economic source generation commit: %w", err)
		}
		return &EconomicSourceGenerationTransition{
			Schema:          "aftergraph.economic-source-generation-transition/v1",
			Decision:        "ADVANCED",
			PriorGeneration: 0,
			Current:         next,
			ExternalEffects: 0,
		}, nil
	}

	if next.Generation < current.Generation {
		return nil, ErrEconomicSourceGenerationRegression
	}
	if next.Generation == current.Generation {
		if !exactEconomicSourceReplay(*current, next) {
			return nil, ErrEconomicSourceEquivocation
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("economic source generation commit replay: %w", err)
		}
		return &EconomicSourceGenerationTransition{
			Schema:          "aftergraph.economic-source-generation-transition/v1",
			Decision:        "IDEMPOTENT_REPLAY",
			PriorGeneration: current.Generation,
			Current:         *current,
			ExternalEffects: 0,
		}, nil
	}
	if next.ObservedAtUnix < current.ObservedAtUnix {
		return nil, ErrEconomicSourceTimeRegression
	}
	if next.CursorKind != current.CursorKind {
		return nil, ErrEconomicSourceCursorKindChange
	}

	next.Revision = current.Revision + 1
	next.StateDigest = economicSourceStateDigest(next)
	result, err := tx.ExecContext(ctx,
		"UPDATE economic_source_generations SET generation=?, cursor_kind=?, cursor_value=?, observed_at_unix=?, "+
			"evidence_hash=?, record_digest=?, capture_hash=?, revision=?, state_digest=?, updated_at=? "+
			"WHERE source_class=? AND source_id=? AND generation=? AND revision=?",
		next.Generation, next.CursorKind, next.CursorValue, next.ObservedAtUnix,
		next.EvidenceHash, next.RecordDigest, next.CaptureHash, next.Revision,
		next.StateDigest, time.Now().UTC().Format(time.RFC3339Nano),
		next.SourceClass, next.SourceID, current.Generation, current.Revision,
	)
	if err != nil {
		return nil, fmt.Errorf("economic source generation update: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("economic source generation rows affected: %w", err)
	}
	if rows != 1 {
		return nil, ErrEconomicSourceConcurrentAdvance
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("economic source generation commit advance: %w", err)
	}
	return &EconomicSourceGenerationTransition{
		Schema:          "aftergraph.economic-source-generation-transition/v1",
		Decision:        "ADVANCED",
		PriorGeneration: current.Generation,
		Current:         next,
		ExternalEffects: 0,
	}, nil
}

func (s *SQLiteStore) GetEconomicSourceGeneration(
	ctx context.Context,
	sourceClass string,
	sourceID string,
) (*EconomicSourceGenerationState, error) {
	row := s.readQueryRow(ctx,
		"SELECT source_class, source_id, generation, cursor_kind, cursor_value, observed_at_unix, "+
			"evidence_hash, record_digest, capture_hash, revision, state_digest "+
			"FROM economic_source_generations WHERE source_class=? AND source_id=?",
		sourceClass, sourceID,
	)
	return scanEconomicSourceGeneration(row)
}

func loadEconomicSourceGenerationTx(
	ctx context.Context,
	tx *sql.Tx,
	sourceClass string,
	sourceID string,
) (*EconomicSourceGenerationState, error) {
	row := tx.QueryRowContext(ctx,
		"SELECT source_class, source_id, generation, cursor_kind, cursor_value, observed_at_unix, "+
			"evidence_hash, record_digest, capture_hash, revision, state_digest "+
			"FROM economic_source_generations WHERE source_class=? AND source_id=?",
		sourceClass, sourceID,
	)
	return scanEconomicSourceGeneration(row)
}

type economicSourceGenerationScanner interface {
	Scan(...any) error
}

func scanEconomicSourceGeneration(row economicSourceGenerationScanner) (*EconomicSourceGenerationState, error) {
	var out EconomicSourceGenerationState
	err := row.Scan(
		&out.SourceClass, &out.SourceID, &out.Generation, &out.CursorKind, &out.CursorValue,
		&out.ObservedAtUnix, &out.EvidenceHash, &out.RecordDigest, &out.CaptureHash,
		&out.Revision, &out.StateDigest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if out.StateDigest != economicSourceStateDigest(out) {
		return nil, errors.New("economic source generation state digest mismatch")
	}
	return &out, nil
}
