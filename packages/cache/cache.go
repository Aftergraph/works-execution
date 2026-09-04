// Package cache implements content-addressed work caching (RFC-0005).
//
// Principle (from the starter pack's CACHE_AND_CAS.md): cache
// computation only when equivalence can be proven. A fingerprint binds
// together everything that could change a node's outcome:
//
//   - the node's command (exact bytes)
//   - the work's source identity (repository + ref + SHA)
//   - the node's declared environment variables
//   - the work's declared requirements (os/arch)
//   - the cache namespace (scope: worker-local | organization)
//
// The fingerprint is a SHA-256 over a canonical JSON document. Cache
// entries are stored as immutable objects keyed by fingerprint; a hit
// means "a node with byte-identical inputs already succeeded", and the
// stored result (exit code 0 + log tail) can be replayed without
// executing anything.
//
// Correctness over hit rate: a false hit is a product-integrity
// failure, so the fingerprint errs on the side of missing inputs —
// anything not captured changes the key, never gets merged in.
package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// SchemaVersion-compatible table creation. The store owns migrations;
// this file only defines the data access.
const schemaCacheTable = `
CREATE TABLE IF NOT EXISTS work_cache (
    fingerprint  TEXT PRIMARY KEY,
    scope        TEXT NOT NULL,            -- worker | organization
    work_id      TEXT NOT NULL,            -- first creator (provenance)
    node_id      TEXT NOT NULL,
    exit_code    INTEGER NOT NULL,
    log_tail     TEXT NOT NULL,            -- truncated combined output
    created_at   TEXT NOT NULL
);`

// EnsureSchema creates the cache table if absent. Called by the store
// at Open; safe to call repeatedly.
func EnsureSchema(db *sql.DB) error {
	_, err := db.Exec(schemaCacheTable)
	return err
}

// Key is the canonical fingerprint input. Every field participates in
// the hash; nothing outside it does.
type Key struct {
	Run        string            `json:"run"`
	Repository string            `json:"repository,omitempty"`
	Ref        string            `json:"ref,omitempty"`
	SHA        string            `json:"sha,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	OS         string            `json:"os,omitempty"`
	Arch       string            `json:"arch,omitempty"`
	Scope      string            `json:"scope"` // worker | organization
}

// Fingerprint returns the hex-encoded SHA-256 of the canonical JSON
// encoding of the Key. Map iteration order would poison the hash, so
// Env is canonicalized by json.Marshal (Go sorts map keys).
func (k Key) Fingerprint() (string, error) {
	b, err := json.Marshal(k)
	if err != nil {
		return "", fmt.Errorf("cache: canonical key: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// KeyFromNode builds the fingerprint key for one (work, node) pair.
// Scope uplevels to the caller: worker-local keys include the worker
// id, organization keys do not. When the node declares CacheSpec
// KeyInputs, only the named fields participate in the fingerprint —
// e.g. key_inputs: [run, repository] makes push-CI cache hits
// possible across different SHAs (the default includes SHA, which
// makes every push a fresh key by construction).
func KeyFromNode(work *workgraph.Work, node *workgraph.Node, scope string) Key {
	k := Key{
		Run:        node.Run,
		Repository: work.Source.Repository,
		Ref:        work.Source.Ref,
		SHA:        work.Source.SHA,
		Env:        node.Env,
		OS:         work.Requirements.OS,
		Arch:       work.Requirements.Arch,
		Scope:      scope,
	}
	if node.CacheSpec == nil || len(node.CacheSpec.KeyInputs) == 0 {
		return k
	}
	allowed := map[string]bool{}
	for _, f := range node.CacheSpec.KeyInputs {
		allowed[f] = true
	}
	if !allowed["run"] {
		k.Run = ""
	}
	if !allowed["repository"] {
		k.Repository = ""
	}
	if !allowed["ref"] {
		k.Ref = ""
	}
	if !allowed["sha"] {
		k.SHA = ""
	}
	if !allowed["env"] {
		k.Env = nil
	}
	if !allowed["os"] {
		k.OS = ""
	}
	if !allowed["arch"] {
		k.Arch = ""
	}
	return k
}

// Entry is a stored cache hit: what happened last time identical
// inputs ran.
type Entry struct {
	Fingerprint string
	WorkID      string
	NodeID      string
	ExitCode    int
	LogTail     string
	CreatedAt   time.Time
}

// Store is the content-addressed cache backing store. V1 backs it
// with the same SQLite database as the works table (single-writer is
// fine: cache reads happen once per node execution).
type Store struct {
	db *sql.DB
}

// New wraps an open *sql.DB.
func New(db *sql.DB) (*Store, error) {
	if err := EnsureSchema(db); err != nil {
		return nil, fmt.Errorf("cache: ensure schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Lookup returns the cached result for a fingerprint, or
// ErrNotFound.
var ErrNotFound = fmt.Errorf("cache: miss")

// Lookup returns the entry for fingerprint, or ErrNotFound.
func (s *Store) Lookup(ctx context.Context, fingerprint string) (*Entry, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT work_id, node_id, exit_code, log_tail, created_at FROM work_cache WHERE fingerprint = ?`,
		fingerprint,
	)
	var e Entry
	var created string
	e.Fingerprint = fingerprint
	if err := row.Scan(&e.WorkID, &e.NodeID, &e.ExitCode, &e.LogTail, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return &e, nil
}

// Put stores a successful node result under its fingerprint. Only
// successful executions are cached — failures are re-run by design
// (flaky infra should not be memorialized).
func (s *Store) Put(ctx context.Context, e Entry) error {
	if e.ExitCode != 0 {
		return fmt.Errorf("cache: refusing to cache failed node (exit=%d)", e.ExitCode)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO work_cache (fingerprint, scope, work_id, node_id, exit_code, log_tail, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(fingerprint) DO NOTHING
    `,
		e.Fingerprint, "organization", e.WorkID, e.NodeID, e.ExitCode, e.LogTail,
		e.CreatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// LogTailMax bounds the stored output. Full logs live in the artifact
// store; the cache only needs enough to explain a hit in the UI.
const LogTailMax = 4096

// TruncateLogTail clamps a combined log to LogTailMax bytes.
func TruncateLogTail(b []byte) string {
	if len(b) <= LogTailMax {
		return string(b)
	}
	return string(b[len(b)-LogTailMax:]) // keep the tail: errors surface at the end
}
