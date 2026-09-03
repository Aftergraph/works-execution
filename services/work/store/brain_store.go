package store

// k-042 (ADR-0023, brain.ns/1.0): durable persistence for the Company Brain
// namespace.
//
// schema v11 adds two tables:
//   - brain_objects: revisioned brain objects at /org/<id>/... paths. The
//     append-only law is structural: the PRIMARY KEY is (path, revision) and
//     writes are INSERT-only — a revision is never overwritten, so
//     ErrBrainRevisionExists guards replays. Tombstones are new revisions
//     (tombstone=1), never DELETEs. Ephemeral objects carry expires_at; the
//     store reports the flag truthfully and the API decides visibility.
//   - brain_mounts: durable read-view grants {subject, path_prefix, scopes,
//     ttl, revoked}. Revoke is idempotent: unknown or already-revoked ids
//     are no-op successes.
//
// Fail-closed law (same as link_devices): a row whose JSON cannot be
// hydrated (content_json corrupt on write, scopes_json corrupt on read,
// unparsable timestamps) is refused, never partially trusted.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const brainSchema = `
CREATE TABLE IF NOT EXISTS brain_objects (
    path            TEXT NOT NULL,
    revision        INTEGER NOT NULL,
    class           TEXT NOT NULL CHECK (class IN ('immutable','mutable_with_revision','ephemeral')),
    content_json    TEXT NOT NULL,
    content_hash    TEXT NOT NULL,
    authoritative   INTEGER NOT NULL DEFAULT 0,
    promotion       TEXT NOT NULL DEFAULT 'none' CHECK (promotion IN ('none','human_stamped')),
    human_stamp     TEXT NOT NULL DEFAULT '',
    tombstone       INTEGER NOT NULL DEFAULT 0,
    evidence_ref    TEXT NOT NULL,
    expires_at      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    PRIMARY KEY (path, revision)
);
CREATE INDEX IF NOT EXISTS idx_brain_objects_path ON brain_objects(path, revision DESC);
CREATE TABLE IF NOT EXISTS brain_mounts (
    id          TEXT PRIMARY KEY,
    subject     TEXT NOT NULL,
    path_prefix TEXT NOT NULL,
    scopes_json TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    revoked     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_brain_mounts_subject ON brain_mounts(subject, revoked);
`

func (s *SQLiteStore) migrateBrain() error {
	if _, err := s.db.Exec(brainSchema); err != nil {
		return err
	}
	return nil
}

// brainTimeLayout is RFC3339Nano with the fractional seconds fixed at 9
// digits (zero-padded). brain_objects/brain_mounts ORDER BY their TEXT
// timestamps, and time.Format(time.RFC3339Nano) trims trailing zeros —
// "…:00.12Z" < "…:00.1Z" textually but not chronologically. The padded
// layout keeps text order == time order; time.Parse(time.RFC3339Nano, …)
// still reads it.
const brainTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func fmtBrainTime(t time.Time) string { return t.UTC().Format(brainTimeLayout) }

// Brain object classes (brain.ns/1.0).
const (
	BrainClassImmutable = "immutable"
	BrainClassMutable   = "mutable_with_revision"
	BrainClassEphemeral = "ephemeral"
)

// Brain promotion states.
const (
	PromotionNone         = "none"
	PromotionHumanStamped = "human_stamped"
)

// ErrBrainRevisionExists is returned by PutBrainObject when (path, revision)
// is already taken. The append-only law forbids in-place revision updates;
// the writer must bump the revision instead.
var ErrBrainRevisionExists = errors.New("brain revision already exists")

// BrainObject is one revisioned row of the brain namespace. Every write is
// an append: a new revision is a new row, and a tombstone is a new revision
// with Tombstone=true. ExpiresAt is set only for ephemeral objects; reads
// report it truthfully and the API decides visibility.
type BrainObject struct {
	Path          string
	Revision      int
	Class         string
	ContentJSON   string
	ContentHash   string
	Authoritative bool
	Promotion     string
	HumanStamp    string
	Tombstone     bool
	EvidenceRef   string
	ExpiresAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// BrainMount is a durable read-view grant: subject may see path_prefix with
// the listed scopes until ExpiresAt (or until revoked).
type BrainMount struct {
	ID         string
	Subject    string
	PathPrefix string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	Revoked    bool
}

const brainColumns = `path, revision, class, content_json, content_hash, authoritative,
	promotion, human_stamp, tombstone, evidence_ref, expires_at, created_at, updated_at`

// PutBrainObject appends one revision. It never overwrites: a (path,
// revision) collision returns ErrBrainRevisionExists. content_json must
// parse (corrupt fail closed), evidence_ref is mandatory (brain.ns/1.0
// law — a write without provenance is refused), and a human_stamped
// promotion must carry the stamp. Zero timestamps default to now (UTC).
func (s *SQLiteStore) PutBrainObject(ctx context.Context, o *BrainObject) error {
	if o == nil {
		return errors.New("brain store: nil object")
	}
	if o.Path == "" {
		return errors.New("brain store: object path is required")
	}
	if o.Revision < 1 {
		return fmt.Errorf("brain store: %s: revision must be >= 1, got %d", o.Path, o.Revision)
	}
	if !json.Valid([]byte(o.ContentJSON)) {
		return fmt.Errorf("brain store: %s r%d content_json corrupt (fail closed)", o.Path, o.Revision)
	}
	if o.EvidenceRef == "" {
		return fmt.Errorf("brain store: %s r%d evidence_ref is required", o.Path, o.Revision)
	}
	if o.Promotion == "" {
		o.Promotion = PromotionNone
	}
	if o.Promotion == PromotionHumanStamped && o.HumanStamp == "" {
		return fmt.Errorf("brain store: %s r%d promotion human_stamped requires human_stamp", o.Path, o.Revision)
	}
	now := time.Now().UTC()
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now
	}
	if o.UpdatedAt.IsZero() {
		o.UpdatedAt = now
	}
	expires := ""
	if o.ExpiresAt != nil && !o.ExpiresAt.IsZero() {
		expires = o.ExpiresAt.UTC().Format(brainTimeLayout)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO brain_objects
		   (path, revision, class, content_json, content_hash, authoritative,
		    promotion, human_stamp, tombstone, evidence_ref, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.Path, o.Revision, o.Class, o.ContentJSON, o.ContentHash, b2i(o.Authoritative),
		o.Promotion, o.HumanStamp, b2i(o.Tombstone), o.EvidenceRef, expires,
		fmtBrainTime(o.CreatedAt), fmtBrainTime(o.UpdatedAt))
	if err != nil {
		// Distinguish the append-only law from every other failure so the
		// API can map it to a conflict. Driver error strings vary; probe.
		var probe int
		if qErr := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM brain_objects WHERE path = ? AND revision = ?`,
			o.Path, o.Revision).Scan(&probe); qErr == nil {
			return ErrBrainRevisionExists
		}
		return fmt.Errorf("brain store: put object %s r%d: %w", o.Path, o.Revision, err)
	}
	return nil
}

// StampBrainPromotion is the ONE narrow in-place exception to the
// append-only law: the k-041 immutable-object human stamp (immutable
// objects have exactly one revision; promotion rides revision 1 without a
// new revision, so nothing else can ever use this path). Guarded: mutable
// rows, revision>1 rows, already-authoritative rows, and tombstones are all
// untouched by the UPDATE. ErrNotFound when the revision-1 row is missing.
func (s *SQLiteStore) StampBrainPromotion(ctx context.Context, path, humanStamp string) error {
	if humanStamp == "" {
		return errors.New("brain store: promotion stamp requires human_stamp")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE brain_objects
		    SET authoritative = 1, promotion = 'human_stamped', human_stamp = ?, updated_at = ?
		  WHERE path = ? AND revision = 1
		    AND class = 'immutable'
		    AND authoritative = 0
		    AND tombstone = 0`,
		humanStamp, fmtBrainTime(time.Now().UTC()), path)
	if err != nil {
		return fmt.Errorf("brain store: stamp promotion %s: %w", path, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("brain store: stamp rows: %w", err)
	}
	if n == 0 {
		// Distinguish missing from refused (both fail closed, different code).
		var probe int
		if qErr := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM brain_objects WHERE path = ? AND revision = 1`, path).Scan(&probe); qErr != nil {
			return ErrNotFound
		}
		return fmt.Errorf("brain store: %s r1 not stampable (not immutable, already authoritative, or tombstoned)", path)
	}
	return nil
}

// GetBrainObject reads one revision; revision 0 means the latest. A missing
// path (or a missing revision on an existing path) returns ErrNotFound.
// Rows that cannot hydrate (corrupt timestamps) fail closed.
func (s *SQLiteStore) GetBrainObject(ctx context.Context, path string, revision int) (*BrainObject, error) {
	var where string
	var args []any
	if revision == 0 {
		where = `path = ? ORDER BY revision DESC LIMIT 1`
		args = []any{path}
	} else {
		where = `path = ? AND revision = ?`
		args = []any{path, revision}
	}
	q := fmt.Sprintf(`SELECT %s FROM brain_objects WHERE %s`, brainColumns, where)
	return scanBrainObject(s.db.QueryRowContext(ctx, q, args...))
}

func scanBrainObject(row *sql.Row) (*BrainObject, error) {
	var (
		o                              = &BrainObject{}
		authoritative, tombstone       int
		expiresRaw, createdRaw, updRaw string
	)
	err := row.Scan(&o.Path, &o.Revision, &o.Class, &o.ContentJSON, &o.ContentHash,
		&authoritative, &o.Promotion, &o.HumanStamp, &tombstone, &o.EvidenceRef,
		&expiresRaw, &createdRaw, &updRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("brain store: get object: %w", err)
	}
	o.Authoritative = authoritative != 0
	o.Tombstone = tombstone != 0
	if expiresRaw != "" {
		t, perr := time.Parse(time.RFC3339Nano, expiresRaw)
		if perr != nil {
			return nil, fmt.Errorf("brain store: object %s r%d expires_at corrupt (fail closed)", o.Path, o.Revision)
		}
		o.ExpiresAt = &t
	}
	if o.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw); err != nil {
		return nil, fmt.Errorf("brain store: object %s r%d created_at corrupt (fail closed)", o.Path, o.Revision)
	}
	if o.UpdatedAt, err = time.Parse(time.RFC3339Nano, updRaw); err != nil {
		return nil, fmt.Errorf("brain store: object %s r%d updated_at corrupt (fail closed)", o.Path, o.Revision)
	}
	return o, nil
}

// ListBrainPathsWithPrefix returns the DISTINCT brain paths under prefix,
// ordered by each path's latest-revision updated_at (newest first).
// limit <= 0 defaults to 50 and is capped at 500.
func (s *SQLiteStore) ListBrainPathsWithPrefix(ctx context.Context, prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT b.path FROM brain_objects b
		 JOIN (SELECT path, MAX(revision) AS rev FROM brain_objects
		       WHERE substr(path, 1, length(?)) = ?
		       GROUP BY path) t ON b.path = t.path AND b.revision = t.rev
		 ORDER BY b.updated_at DESC, b.path
		 LIMIT ?`,
		prefix, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("brain store: list paths: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("brain store: scan path: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("brain store: list paths: %w", err)
	}
	return out, nil
}

// LatestRevision returns the highest stored revision for path, or 0 when the
// path has never been written (the caller's next append is revision 1).
func (s *SQLiteStore) LatestRevision(ctx context.Context, path string) (int, error) {
	var rev int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision), 0) FROM brain_objects WHERE path = ?`, path).Scan(&rev)
	if err != nil {
		return 0, fmt.Errorf("brain store: latest revision %s: %w", path, err)
	}
	return rev, nil
}

// Tombstoned reports whether the LATEST revision of path carries the
// tombstone flag. An absent path is not tombstoned (false, nil): there is
// nothing to hide.
func (s *SQLiteStore) Tombstoned(ctx context.Context, path string) (bool, error) {
	var tomb int
	err := s.db.QueryRowContext(ctx,
		`SELECT tombstone FROM brain_objects WHERE path = ? ORDER BY revision DESC LIMIT 1`,
		path).Scan(&tomb)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("brain store: tombstone %s: %w", path, err)
	}
	return tomb != 0, nil
}

// CreateBrainMount records a durable read-view grant. Scopes are mandatory
// (a mount that grants nothing is refused on write, mirroring the read-side
// fail-closed law) and id collisions are rejected — mounts have no
// revision story; the API derives a fresh id per grant.
func (s *SQLiteStore) CreateBrainMount(ctx context.Context, m *BrainMount) error {
	if m == nil {
		return errors.New("brain store: nil mount")
	}
	if m.ID == "" || m.Subject == "" || m.PathPrefix == "" {
		return errors.New("brain store: mount id, subject and path_prefix are required")
	}
	if len(m.Scopes) == 0 {
		return fmt.Errorf("brain store: mount %s has no scopes (fail closed)", m.ID)
	}
	scopes, err := json.Marshal(m.Scopes)
	if err != nil {
		return fmt.Errorf("brain store: marshal mount scopes: %w", err)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.ExpiresAt.IsZero() {
		return fmt.Errorf("brain store: mount %s expires_at is required (TTL is explicit)", m.ID)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO brain_mounts (id, subject, path_prefix, scopes_json, created_at, expires_at, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Subject, m.PathPrefix, string(scopes),
		fmtBrainTime(m.CreatedAt),
		fmtBrainTime(m.ExpiresAt), b2i(m.Revoked)); err != nil {
		return fmt.Errorf("brain store: create mount %s: %w", m.ID, err)
	}
	return nil
}

// RevokeBrainMount sets revoked=1. Idempotent by contract: an already
// revoked or unknown id is a success (no-op) — revoke is a law, not a
// question.
func (s *SQLiteStore) RevokeBrainMount(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE brain_mounts SET revoked = 1 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("brain store: revoke mount %s: %w", id, err)
	}
	return nil
}

// ListBrainMounts returns the mounts granted to subject, oldest first.
// Revoked mounts are hidden unless includeRevoked. A row whose scopes_json
// cannot parse fails the whole read closed (corrupt == unusable), exactly
// like link_devices.
func (s *SQLiteStore) ListBrainMounts(ctx context.Context, subject string, includeRevoked bool) ([]*BrainMount, error) {
	q := `SELECT id, subject, path_prefix, scopes_json, created_at, expires_at, revoked
	      FROM brain_mounts WHERE subject = ?`
	if !includeRevoked {
		q += ` AND revoked = 0`
	}
	q += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, q, subject)
	if err != nil {
		return nil, fmt.Errorf("brain store: list mounts: %w", err)
	}
	defer rows.Close()
	var out []*BrainMount
	for rows.Next() {
		var (
			m          = &BrainMount{}
			scopesJSON string
			createdRaw string
			expiresRaw string
			revokedRaw int
		)
		if err := rows.Scan(&m.ID, &m.Subject, &m.PathPrefix, &scopesJSON,
			&createdRaw, &expiresRaw, &revokedRaw); err != nil {
			return nil, fmt.Errorf("brain store: scan mount: %w", err)
		}
		if err := json.Unmarshal([]byte(scopesJSON), &m.Scopes); err != nil || len(m.Scopes) == 0 {
			return nil, fmt.Errorf("brain store: mount %s scopes corrupt (fail closed)", m.ID)
		}
		if m.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw); err != nil {
			return nil, fmt.Errorf("brain store: mount %s created_at corrupt (fail closed)", m.ID)
		}
		if m.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresRaw); err != nil {
			return nil, fmt.Errorf("brain store: mount %s expires_at corrupt (fail closed)", m.ID)
		}
		m.Revoked = revokedRaw != 0
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("brain store: list mounts: %w", err)
	}
	return out, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
