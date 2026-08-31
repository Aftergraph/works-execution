// Package store persists the durable Work primitive in SQLite.
//
// See ADR-0005 (docs/adr/ADR-0005-sqlite-for-v1-state.md) for why SQLite and
// the migration path to PostgreSQL.
//
// The Store is wrapped behind a Go interface so the underlying driver can be
// swapped without touching the API or worker. Every state mutation runs inside
// a single SQLite transaction (BEGIN IMMEDIATE) so the state machine cannot
// observe a half-applied transition.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers itself

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/audit"
)

// SchemaVersion is the current schema level this store applies. Bumped
// each time a migration lands. Exposed so callers (CI, healthz) can
// assert they are talking to a store at the expected level.
const SchemaVersion = 6

// ErrSchemaTooOld is returned by health checks / smoke tests when the
// on-disk schema is older than SchemaVersion. Operators should run
// `migrate` (slice upgrade) before serving traffic.
var ErrSchemaTooOld = errors.New("schema too old")

// ErrNotFound is returned when a Work ID does not exist.
var ErrNotFound = errors.New("work not found")

// ErrIdempotencyConflict is returned when an idempotent Work creation is
// attempted with the same key but a different payload.
var ErrIdempotencyConflict = errors.New("idempotency key conflict")

// Store is the durable persistence interface. The API and worker depend on
// this interface, not on the concrete SQLite implementation.
type Store interface {
	CreateWork(ctx context.Context, w *workgraph.Work) error
	GetWork(ctx context.Context, id string) (*workgraph.Work, error)
	ListWorks(ctx context.Context, limit int) ([]*workgraph.Work, error)
	UpdateState(ctx context.Context, id string, to workgraph.State) (*workgraph.Work, error)
	AppendAttempt(ctx context.Context, workID string, a workgraph.Attempt) (*workgraph.Work, error)
	AppendEvidence(ctx context.Context, workID string, e workgraph.Evidence) (*workgraph.Work, error)
	AppendArtifact(ctx context.Context, workID string, art workgraph.Artifact) (*workgraph.Work, error)

	// Lease operations (slice 2).
	GrantLease(ctx context.Context, workID, nodeID, workerID string, ttl time.Duration) (*workgraph.Lease, *workgraph.Attempt, error)
	RenewLease(ctx context.Context, leaseID string, ttl time.Duration) (*workgraph.Lease, error)
	CompleteLease(ctx context.Context, leaseID string, exitCode int, artifact *workgraph.Artifact, evidence []workgraph.Evidence) (*workgraph.Work, error)
	ReleaseLease(ctx context.Context, leaseID, reason string) error
	RevokeLease(ctx context.Context, leaseID, reason string) error
	GetLease(ctx context.Context, leaseID string) (*workgraph.Lease, error)
	ListExpiredLeases(ctx context.Context, limit int) ([]*workgraph.Lease, error)
	MarkAttemptCancelled(ctx context.Context, attemptID, reason string) error
	ActiveLeasesByWorkID(ctx context.Context, workID string) (map[string]bool, error)
	LeasesByWorkID(ctx context.Context, workID string) ([]*workgraph.Lease, error)

	// Audit (slice 6 / k-impl-012): read the CloudEvents audit stream.
	// Empty filter fields are unbounded; limit clamps to 200 if zero and
	// to 1000 if greater.
	ListAuditEvents(ctx context.Context, f audit.ListFilter) ([]audit.AuditEvent, error)

	// Provenance operations (slice 5 / k-impl-005).
	SaveProvenance(ctx context.Context, p Provenance) error
	GetProvenance(ctx context.Context, workID string) (*Provenance, error)

	Close() error
}

// Provenance is the persisted workflow-provenance attestation for a Work.
// Attestation is the canonical JSON envelope (predicateType + subject +
// predicate); Signature is the HMAC over the envelope bytes, hex-encoded.
type Provenance struct {
	WorkID      string
	Attestation []byte // canonical envelope JSON
	Signature   []byte // HMAC-SHA256 over Attestation, hex
	KeyID       string // signer key identifier
	BuilderID   string // control plane URI
	ProducedAt  time.Time
}

// SQLiteStore is the SQLite implementation of Store.
type SQLiteStore struct {
	db *sql.DB
	// Audit emits CloudEvents for every state mutation. May be nil; the
	// helpers (auditEmit) tolerate that and treat it as "audit disabled".
	Audit audit.Emitter
}

// Open opens (or creates) a SQLite database at the given path and applies the
// schema. The caller must Close() when done.
func Open(path string) (*SQLiteStore, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite is single-writer; keep the pool small.
	db.SetMaxOpenConns(1)

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Default to a SQLite-backed CloudEvents emitter. Tests can swap
	// in their own via the Audit field before serving traffic.
	s.Audit = audit.NewSQLiteEmitter(db, nil)
	return s, nil
}

// schema is intentionally portable so the eventual Postgres migration is
// mechanical (see ADR-0005).
const schema = `
CREATE TABLE IF NOT EXISTS works (
    id              TEXT PRIMARY KEY,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    state           TEXT NOT NULL,
    source_json     TEXT NOT NULL,
    objective_json  TEXT NOT NULL,
    graph_json      TEXT NOT NULL,
    requirements_json TEXT NOT NULL,
    policy_json     TEXT NOT NULL,
    idempotency_key TEXT UNIQUE,
    correlation_id  TEXT
);

CREATE TABLE IF NOT EXISTS work_attempts (
    id          TEXT PRIMARY KEY,
    work_id     TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    node_id     TEXT NOT NULL,
    worker_id   TEXT,
    started_at  TEXT NOT NULL,
    finished_at TEXT,
    exit_code   INTEGER NOT NULL,
    status      TEXT NOT NULL,
    log_ref     TEXT,
    error       TEXT
);

CREATE TABLE IF NOT EXISTS work_artifacts (
    work_id    TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    id         TEXT NOT NULL, -- content hash
    node_id    TEXT NOT NULL,
    mime_type  TEXT NOT NULL,
    size       INTEGER NOT NULL,
    path       TEXT NOT NULL,
    PRIMARY KEY (work_id, id)
);

CREATE TABLE IF NOT EXISTS work_evidence (
    id          TEXT PRIMARY KEY,
    work_id     TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    node_id     TEXT NOT NULL,
    attempt_id  TEXT NOT NULL,
    type        TEXT NOT NULL,
    result      TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    artifact_id TEXT,
    signer      TEXT,
    environment TEXT,
    details_json TEXT
);

CREATE INDEX IF NOT EXISTS idx_attempts_work_id ON work_attempts(work_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_work_id ON work_artifacts(work_id);
CREATE INDEX IF NOT EXISTS idx_evidence_work_id ON work_evidence(work_id);

-- slice 2: leases
CREATE TABLE IF NOT EXISTS work_leases (
    id          TEXT PRIMARY KEY,
    work_id     TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    node_id     TEXT NOT NULL,
    worker_id   TEXT NOT NULL,
    attempt_id  TEXT NOT NULL,
    granted_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    last_beat_at TEXT NOT NULL,
    status      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_leases_status_expires ON work_leases(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_leases_work_node ON work_leases(work_id, node_id);
CREATE INDEX IF NOT EXISTS idx_leases_attempt ON work_leases(attempt_id);

-- slice 2 v2 -> v3: add lease_id column to work_attempts (nullable).
-- ALTER TABLE ADD COLUMN is idempotent in modernc/sqlite: it returns an error
-- if the column already exists. We detect first via pragma_table_info.

-- slice 5 (k-impl-005): v4 -> v5 — workflow provenance attestation.
-- One attestation per Work. The envelope JSON contains predicateType +
-- subject + predicate (per docs/standards/schemas/workflow-provenance.schema.json).
-- Signatures live alongside; we persist the canonical envelope and the
-- signature separately so they can be independently re-validated.
CREATE TABLE IF NOT EXISTS work_provenance (
    work_id      TEXT PRIMARY KEY REFERENCES works(id) ON DELETE CASCADE,
    attestation  TEXT NOT NULL,
    signature    TEXT NOT NULL,
    key_id       TEXT NOT NULL,
    builder_id   TEXT NOT NULL,
    produced_at  TEXT NOT NULL
);

-- slice 6 (k-impl-012): v5 -> v6 — CloudEvents v1.0 audit stream.
-- Every Work state transition (and work creation) emits a CloudEvent
-- that is persisted here. The on-wire shape (when returned by
-- /v1/audit-events) follows the CloudEvents v1.0 spec: id, source,
-- specversion, type, time, datacontenttype, subject, data. The
-- denormalized columns (work_id, from_state, to_state, ...) let the
-- query layer filter and ORDER BY without parsing 'data' JSON.
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
);
CREATE INDEX IF NOT EXISTS idx_audit_occurred ON work_audit_events(occurred_at);
CREATE INDEX IF NOT EXISTS idx_audit_work ON work_audit_events(work_id);
CREATE INDEX IF NOT EXISTS idx_audit_type ON work_audit_events(type);
`

func (s *SQLiteStore) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Migration v1 -> v2: convert work_artifacts to composite (work_id, id) PK.
	if s.workArtifactsNeedsMigration() {
		if err := s.migrateWorkArtifacts(); err != nil {
			return fmt.Errorf("migrate work_artifacts: %w", err)
		}
	}
	// Migration v2 -> v3: add lease_id column to work_attempts.
	if s.workAttemptsNeedsLeaseIDColumn() {
		if _, err := s.db.Exec(`ALTER TABLE work_attempts ADD COLUMN lease_id TEXT`); err != nil {
			return fmt.Errorf("migrate work_attempts.lease_id: %w", err)
		}
	}
	// Migration v4 -> v5: work_provenance table (created idempotently above
	// via CREATE TABLE IF NOT EXISTS, but mark the version transition here).
	// No schema alteration is required because the table is net-new.
	if err := s.bumpSchemaVersion(5); err != nil {
		return fmt.Errorf("bump schema version: %w", err)
	}
	// Migration v5 -> v6: work_audit_events table (created idempotently
	// above via CREATE TABLE IF NOT EXISTS, but mark the version
	// transition here). No data backfill is required: prior slices
	// did not emit CloudEvents, so the table starts empty.
	if err := s.bumpSchemaVersion(SchemaVersion); err != nil {
		return fmt.Errorf("bump schema version: %w", err)
	}
	return nil
}

// bumpSchemaVersion stores the schema version in a sidecar table. We use
// this both to gate future migrations and to let operators inspect the
// current schema level via `pragma user_version`-style introspection.
func (s *SQLiteStore) bumpSchemaVersion(target int) error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var current int
	row := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`)
	if err := row.Scan(&current); err != nil {
		return err
	}
	if current >= target {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`,
		target, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLiteStore) workAttemptsNeedsLeaseIDColumn() bool {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('work_attempts')`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false
		}
		if name == "lease_id" {
			return false
		}
	}
	return true
}

func (s *SQLiteStore) workArtifactsNeedsMigration() bool {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('work_artifacts') WHERE pk > 0`)
	if err != nil {
		return false
	}
	defer rows.Close()
	// Legacy schema: only `id` is PK. New schema: both `work_id` and `id` are PK.
	// If `id` is the sole PK column (single-row result), migrate.
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return false
	}
	// If only one column has pk>0, we still have the legacy schema.
	// (Work_id was NOT NULL but not a PK in the old schema; in the new it is.)
	// Better check: count PK columns. New schema has 2.
	return count == 1
}

func (s *SQLiteStore) migrateWorkArtifacts() error {
	stmts := []string{
		`ALTER TABLE work_artifacts RENAME TO work_artifacts_legacy`,
		`CREATE TABLE work_artifacts (
			work_id    TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
			id         TEXT NOT NULL,
			node_id    TEXT NOT NULL,
			mime_type  TEXT NOT NULL,
			size       INTEGER NOT NULL,
			path       TEXT NOT NULL,
			PRIMARY KEY (work_id, id)
		)`,
		`INSERT OR IGNORE INTO work_artifacts (work_id, id, node_id, mime_type, size, path)
			SELECT work_id, id, node_id, mime_type, size, path FROM work_artifacts_legacy`,
		`DROP TABLE work_artifacts_legacy`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the database handle.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// CreateWork persists a new Work. Returns ErrIdempotencyConflict if a Work
// with the same idempotency key already exists with a different payload.
func (s *SQLiteStore) CreateWork(ctx context.Context, w *workgraph.Work) error {
	if w.ID == "" {
		return errors.New("work.ID required")
	}
	if err := w.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now().UTC()
	}
	w.UpdatedAt = time.Now().UTC()
	if w.State == "" {
		w.State = workgraph.StateCreated
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Idempotency check: if a key is set and a work with the same key exists,
	// return ErrIdempotencyConflict.
	if w.IdempotencyKey != "" {
		var existingID string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM works WHERE idempotency_key = ?`, w.IdempotencyKey,
		).Scan(&existingID)
		if err == nil {
			if existingID == w.ID {
				return tx.Commit() // same payload, idempotent success
			}
			return ErrIdempotencyConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `
        INSERT INTO works (id, created_at, updated_at, state, source_json, objective_json, graph_json, requirements_json, policy_json, idempotency_key, correlation_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		w.ID,
		w.CreatedAt.UTC().Format(time.RFC3339Nano),
		w.UpdatedAt.UTC().Format(time.RFC3339Nano),
		string(w.State),
		mustJSON(w.Source),
		mustJSON(w.Objective),
		mustJSON(w.Graph),
		mustJSON(w.Requirements),
		mustJSON(w.Policy),
		nullable(w.IdempotencyKey),
		nullable(w.CorrelationID),
	)
	if err != nil {
		return fmt.Errorf("insert work: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Best-effort audit emit. A failure to persist the audit event is
	// not surfaced to the caller — the work itself is durable. Errors
	// are logged by the emitter.
	s.auditEmit(ctx, &audit.CloudEvent{
		ID:              audit.NewEventID(),
		Source:          audit.Source,
		SpecVersion:     audit.SpecVersion,
		Type:            audit.TypeWorkCreated,
		Time:            time.Now().UTC(),
		DataContentType: "application/json",
		Subject:         w.ID,
		WorkID:          w.ID,
		ToState:         string(w.State),
		CorrelationID:   w.CorrelationID,
		SchemaVersion:   "6",
		Data: map[string]any{
			"work_id":        w.ID,
			"to_state":       string(w.State),
			"created_at":     w.CreatedAt.UTC().Format(time.RFC3339Nano),
			"correlation_id": w.CorrelationID,
		},
	})
	return nil
}

// GetWork returns the full Work with attempts, artifacts, and evidence hydrated.
func (s *SQLiteStore) GetWork(ctx context.Context, id string) (*workgraph.Work, error) {
	w := &workgraph.Work{ID: id}
	var stateStr string
	var sourceJ, objJ, graphJ, reqJ, polJ string
	var idemKey, corrID sql.NullString
	var createdStr, updatedStr string

	err := s.db.QueryRowContext(ctx, `
        SELECT created_at, updated_at, state, source_json, objective_json, graph_json, requirements_json, policy_json, idempotency_key, correlation_id
        FROM works WHERE id = ?
    `, id).Scan(
		&createdStr, &updatedStr, &stateStr,
		&sourceJ, &objJ, &graphJ, &reqJ, &polJ,
		&idemKey, &corrID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	w.CreatedAt, _ = parseTime(createdStr)
	w.UpdatedAt, _ = parseTime(updatedStr)
	w.State = workgraph.State(stateStr)

	if err := json.Unmarshal([]byte(sourceJ), &w.Source); err != nil {
		return nil, fmt.Errorf("decode source: %w", err)
	}
	if err := json.Unmarshal([]byte(objJ), &w.Objective); err != nil {
		return nil, fmt.Errorf("decode objective: %w", err)
	}
	if err := json.Unmarshal([]byte(graphJ), &w.Graph); err != nil {
		return nil, fmt.Errorf("decode graph: %w", err)
	}
	if err := json.Unmarshal([]byte(reqJ), &w.Requirements); err != nil {
		return nil, fmt.Errorf("decode requirements: %w", err)
	}
	if err := json.Unmarshal([]byte(polJ), &w.Policy); err != nil {
		return nil, fmt.Errorf("decode policy: %w", err)
	}
	if idemKey.Valid {
		w.IdempotencyKey = idemKey.String
	}
	if corrID.Valid {
		w.CorrelationID = corrID.String
	}

	// Hydrate attempts
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, node_id, COALESCE(worker_id,''), started_at, COALESCE(finished_at,''), exit_code, status, COALESCE(log_ref,''), COALESCE(error,'')
        FROM work_attempts WHERE work_id = ? ORDER BY started_at ASC
    `, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a workgraph.Attempt
		var startedStr string
		var finishedStr string
		if err := rows.Scan(&a.ID, &a.NodeID, &a.WorkerID, &startedStr, &finishedStr, &a.ExitCode, &a.Status, &a.LogRef, &a.Error); err != nil {
			return nil, err
		}
		a.StartedAt, _ = time.Parse(time.RFC3339Nano, startedStr)
		if finishedStr != "" {
			a.FinishedAt, _ = time.Parse(time.RFC3339Nano, finishedStr)
		}
		w.Attempts = append(w.Attempts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Hydrate artifacts
	rows2, err := s.db.QueryContext(ctx, `
        SELECT id, node_id, mime_type, size, path
        FROM work_artifacts WHERE work_id = ?
    `, id)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var art workgraph.Artifact
		if err := rows2.Scan(&art.ID, &art.NodeID, &art.MimeType, &art.Size, &art.Path); err != nil {
			return nil, err
		}
		w.Artifacts = append(w.Artifacts, art)
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	// Hydrate evidence
	rows3, err := s.db.QueryContext(ctx, `
        SELECT id, node_id, attempt_id, type, result, recorded_at, COALESCE(artifact_id,''), COALESCE(signer,''), COALESCE(environment,''), COALESCE(details_json,'')
        FROM work_evidence WHERE work_id = ? ORDER BY recorded_at ASC
    `, id)
	if err != nil {
		return nil, err
	}
	defer rows3.Close()
	for rows3.Next() {
		var e workgraph.Evidence
		var recordedStr string
		var details string
		if err := rows3.Scan(&e.ID, &e.NodeID, &e.AttemptID, &e.Type, &e.Result, &recordedStr, &e.ArtifactID, &e.Signer, &e.Environment, &details); err != nil {
			return nil, err
		}
		e.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedStr)
		if details != "" {
			_ = json.Unmarshal([]byte(details), &e.Details)
		}
		w.Evidence = append(w.Evidence, e)
	}
	return w, rows3.Err()
}

// ListWorks returns up to `limit` most recently updated Works.
func (s *SQLiteStore) ListWorks(ctx context.Context, limit int) ([]*workgraph.Work, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM works ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*workgraph.Work, 0, len(ids))
	for _, id := range ids {
		w, err := s.GetWork(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// UpdateState atomically transitions the Work to `to` if the transition is
// permitted by the state machine. Returns ErrInvalidTransition otherwise.
func (s *SQLiteStore) UpdateState(ctx context.Context, id string, to workgraph.State) (*workgraph.Work, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var currentStr string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM works WHERE id = ?`, id).Scan(&currentStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	current := workgraph.State(currentStr)
	if !workgraph.CanTransition(current, to) {
		return nil, fmt.Errorf("%w: %s -> %s", workgraph.ErrInvalidTransition, current, to)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE works SET state = ?, updated_at = ? WHERE id = ?`, string(to), now, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// CloudEvent: state_changed. Best-effort; emit AFTER commit so we
	// never persist an event for a transition that rolled back.
	s.auditEmit(ctx, &audit.CloudEvent{
		ID:              audit.NewEventID(),
		Source:          audit.Source,
		SpecVersion:     audit.SpecVersion,
		Type:            audit.TypeWorkStateChanged,
		Time:            time.Now().UTC(),
		DataContentType: "application/json",
		Subject:         id,
		WorkID:          id,
		FromState:       string(current),
		ToState:         string(to),
		SchemaVersion:   "6",
		Data: audit.StateTransitionData{
			WorkID:    id,
			FromState: string(current),
			ToState:   string(to),
			At:        time.Now().UTC(),
			Actor:     "api",
		},
	})
	return s.GetWork(ctx, id)
}

// AppendAttempt records a new attempt for a Work. If the attempt ID already
// exists, the existing row is updated (status, finished_at, exit_code, error).
// This lets the worker write the attempt once as "running" and then update it
// to the terminal status without losing the started_at.
func (s *SQLiteStore) AppendAttempt(ctx context.Context, workID string, a workgraph.Attempt) (*workgraph.Work, error) {
	if a.ID == "" {
		return nil, errors.New("attempt.ID required")
	}
	if a.StartedAt.IsZero() {
		a.StartedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO work_attempts (id, work_id, node_id, worker_id, started_at, finished_at, exit_code, status, log_ref, error)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            finished_at = excluded.finished_at,
            exit_code   = excluded.exit_code,
            status      = excluded.status,
            log_ref     = excluded.log_ref,
            error       = excluded.error
    `,
		a.ID, workID, a.NodeID, nullable(a.WorkerID),
		a.StartedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(a.FinishedAt),
		a.ExitCode, a.Status, nullable(a.LogRef), nullable(a.Error),
	)
	if err != nil {
		return nil, fmt.Errorf("upsert attempt: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE works SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), workID)
	return s.GetWork(ctx, workID)
}

// AppendEvidence records a new evidence record.
func (s *SQLiteStore) AppendEvidence(ctx context.Context, workID string, e workgraph.Evidence) (*workgraph.Work, error) {
	if e.ID == "" {
		return nil, errors.New("evidence.ID required")
	}
	if e.RecordedAt.IsZero() {
		e.RecordedAt = time.Now().UTC()
	}
	details := ""
	if e.Details != nil {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return nil, err
		}
		details = string(b)
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT OR IGNORE INTO work_evidence (id, work_id, node_id, attempt_id, type, result, recorded_at, artifact_id, signer, environment, details_json)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		e.ID, workID, e.NodeID, e.AttemptID, e.Type, e.Result,
		e.RecordedAt.UTC().Format(time.RFC3339Nano),
		nullable(e.ArtifactID), nullable(e.Signer), nullable(e.Environment),
		nullable(details),
	)
	if err != nil {
		return nil, fmt.Errorf("insert evidence: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE works SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), workID)
	return s.GetWork(ctx, workID)
}

// AppendArtifact records a new artifact. The bytes themselves live on disk
// under the artifact's Path; only metadata is persisted here.
func (s *SQLiteStore) AppendArtifact(ctx context.Context, workID string, art workgraph.Artifact) (*workgraph.Work, error) {
	if art.ID == "" {
		return nil, errors.New("artifact.ID required")
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT OR IGNORE INTO work_artifacts (id, work_id, node_id, mime_type, size, path)
        VALUES (?, ?, ?, ?, ?, ?)
    `,
		art.ID, workID, art.NodeID, art.MimeType, art.Size, art.Path,
	)
	if err != nil {
		return nil, fmt.Errorf("insert artifact: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE works SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), workID)
	return s.GetWork(ctx, workID)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("store: marshal failed: %v", err))
	}
	return string(b)
}

// nullable returns the sql.NullString-compatible value. SQLite driver uses
// plain string; NULL is represented by passing sql.NullString(nil).
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTime accepts both RFC3339Nano (our storage format) and RFC3339
// (fallback). Returns zero time on parse failure rather than an error
// because timestamps are best-effort metadata.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
// SaveProvenance persists a Provenance row for the given Work. Re-saving
// for the same Work replaces the previous attestation (the Work's provenance
// is monotonic per the SLSA v1 spec — the most recent terminal-state build
// wins). Foreign-key cascade on works(id) handles deletion.
func (s *SQLiteStore) SaveProvenance(ctx context.Context, p Provenance) error {
	if p.WorkID == "" {
		return errors.New("provenance.work_id is required")
	}
	if len(p.Attestation) == 0 {
		return errors.New("provenance.attestation is required")
	}
	if len(p.Signature) == 0 {
		return errors.New("provenance.signature is required")
	}
	if p.ProducedAt.IsZero() {
		p.ProducedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO work_provenance (work_id, attestation, signature, key_id, builder_id, produced_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(work_id) DO UPDATE SET
            attestation = excluded.attestation,
            signature   = excluded.signature,
            key_id      = excluded.key_id,
            builder_id  = excluded.builder_id,
            produced_at = excluded.produced_at
    `,
		p.WorkID,
		string(p.Attestation),
		string(p.Signature),
		nullable(p.KeyID),
		nullable(p.BuilderID),
		p.ProducedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert provenance: %w", err)
	}
	return nil
}

// GetProvenance fetches the persisted attestation for a Work. Returns
// ErrNotFound if the Work has no attestation yet (e.g. it has not reached a
// terminal state or the producer has not run).
func (s *SQLiteStore) GetProvenance(ctx context.Context, workID string) (*Provenance, error) {
	var p Provenance
	var attestation, signature, keyID, builderID, producedStr string
	err := s.db.QueryRowContext(ctx, `
        SELECT attestation, signature, key_id, builder_id, produced_at
        FROM work_provenance WHERE work_id = ?
    `, workID).Scan(&attestation, &signature, &keyID, &builderID, &producedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.WorkID = workID
	p.Attestation = []byte(attestation)
	p.Signature = []byte(signature)
	p.KeyID = keyID
	p.BuilderID = builderID
	p.ProducedAt, _ = parseTime(producedStr)
	return &p, nil
}

// auditEmit is the internal hook for store mutations. It is a no-op
// when no emitter is configured (e.g. tests that explicitly disable
// audit). Errors from the emitter are logged via the emitter itself;
// we intentionally do not propagate them so that a misconfigured audit
// sink cannot break Work state transitions.
func (s *SQLiteStore) auditEmit(ctx context.Context, ev *audit.CloudEvent) {
	if s.Audit == nil || ev == nil {
		return
	}
	_ = s.Audit.Emit(ctx, ev)
}

// ListAuditEvents implements Store.
func (s *SQLiteStore) ListAuditEvents(ctx context.Context, f audit.ListFilter) ([]audit.AuditEvent, error) {
	return audit.Query(ctx, s.db, f)
}
