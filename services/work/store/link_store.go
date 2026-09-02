package store

// k-link-01 (ADR-0026/0027, link.wire/1.0 + pairing/1.0): durable device
// registry + consent mounts for the WORKS-Link surface.
//
// schema v10 adds two tables:
//   - link_devices: the pairing/1.0 record (PAIRED | REVOKED). Transient
//     handshake states live in the offer, never here — a row means a human
//     confirmed a SAS code.
//   - link_mounts: content-addressed mount records, idempotent on the
//     deterministic id (sha256 over device_id|payload_hash) so a replay of
//     an accepted mount is a no-op INSERT OR IGNORE (sync.rules/1.0).
//
// Fail-closed law: reads hydrate fully; a row whose scopes JSON cannot
// parse is refused (corrupt == unusable), never partially trusted.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/link"
)

const linkSchema = `
CREATE TABLE IF NOT EXISTS link_devices (
    device_id   TEXT PRIMARY KEY,
    scopes_json TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('PAIRED','REVOKED')),
    paired_at   TEXT NOT NULL,
    revoked_at  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS link_mounts (
    id              TEXT PRIMARY KEY,
    device_id       TEXT NOT NULL,
    work_id         TEXT NOT NULL,
    payload_hash    TEXT NOT NULL,
    scope           TEXT NOT NULL,
    purpose_binding TEXT NOT NULL,
    created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_link_mounts_work ON link_mounts(work_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_link_mounts_device ON link_mounts(device_id);
`

func (s *SQLiteStore) migrateLink() error {
	if _, err := s.db.Exec(linkSchema); err != nil {
		return err
	}
	return nil
}

// LinkDevices returns the store-backed link.DeviceStore implementation. The
// returned value also carries the kernel's revoke-cascade sink (k-035):
// linkStore embeds the owning *SQLiteStore, so a type assertion to
// interface{ SuspendMissionsForDevice(ctx, deviceID) ([]string, error) }
// succeeds and the link Service can cascade mission suspensions through the
// same store that owns the works.
func (s *SQLiteStore) LinkDevices() link.DeviceStore { return &linkStore{db: s.db, sqlite: s} }

type linkStore struct {
	db     *sql.DB
	sqlite *SQLiteStore // k-035: back-reference for the revoke-cascade sink
}

func (l *linkStore) GetDevice(ctx context.Context, deviceID string) (*link.Device, error) {
	var (
		d          = &link.Device{DeviceID: deviceID}
		scopesJSON string
		pairedRaw  string
		revokedRaw string
	)
	err := l.db.QueryRowContext(ctx,
		`SELECT scopes_json, state, paired_at, revoked_at FROM link_devices WHERE device_id = ?`,
		deviceID).Scan(&scopesJSON, &d.State, &pairedRaw, &revokedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, link.ErrUnknownDevice
	}
	if err != nil {
		return nil, fmt.Errorf("link store: get device: %w", err)
	}
	if err := json.Unmarshal([]byte(scopesJSON), &d.Scopes); err != nil || len(d.Scopes) == 0 {
		return nil, fmt.Errorf("link store: device %s scopes corrupt (fail closed)", deviceID)
	}
	if t, err := time.Parse(time.RFC3339Nano, pairedRaw); err == nil {
		d.PairedAt = t
	} else {
		return nil, fmt.Errorf("link store: device %s paired_at corrupt (fail closed)", deviceID)
	}
	if revokedRaw != "" {
		if t, err := time.Parse(time.RFC3339Nano, revokedRaw); err == nil {
			d.RevokedAt = t
		}
	}
	return d, nil
}

func (l *linkStore) PutDevice(ctx context.Context, d *link.Device) error {
	scopes, err := json.Marshal(d.Scopes)
	if err != nil {
		return fmt.Errorf("link store: marshal scopes: %w", err)
	}
	revoked := ""
	if !d.RevokedAt.IsZero() {
		revoked = d.RevokedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = l.db.ExecContext(ctx,
		`INSERT INTO link_devices (device_id, scopes_json, state, paired_at, revoked_at)
         VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(device_id) DO UPDATE SET
           scopes_json = excluded.scopes_json,
           state       = excluded.state,
           paired_at   = excluded.paired_at,
           revoked_at  = excluded.revoked_at`,
		d.DeviceID, string(scopes), d.State, d.PairedAt.UTC().Format(time.RFC3339Nano), revoked)
	if err != nil {
		return fmt.Errorf("link store: put device: %w", err)
	}
	return nil
}

func (l *linkStore) InsertMount(ctx context.Context, m *link.MountRecord) (bool, error) {
	res, err := l.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO link_mounts
           (id, device_id, work_id, payload_hash, scope, purpose_binding, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.DeviceID, m.WorkID, m.PayloadHash, m.Scope, m.PurposeBinding,
		m.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("link store: insert mount: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("link store: mount rows affected: %w", err)
	}
	return n == 1, nil
}

func (l *linkStore) GetMount(ctx context.Context, id string) (*link.MountRecord, error) {
	var (
		m         = &link.MountRecord{ID: id}
		createdAt string
	)
	err := l.db.QueryRowContext(ctx,
		`SELECT device_id, work_id, payload_hash, scope, purpose_binding, created_at
         FROM link_mounts WHERE id = ?`, id).
		Scan(&m.DeviceID, &m.WorkID, &m.PayloadHash, &m.Scope, &m.PurposeBinding, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("link store: mount %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("link store: get mount: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		m.CreatedAt = t
	}
	return m, nil
}

// ListMountWorkIDs (k-035) returns the DISTINCT Work IDs the device has
// mounted, oldest mount first. The revoke cascade's read side: which missions
// were attached to a device that just died. DISTINCT because two different
// mounts of the same Work (different payloads) are legitimate rows.
func (l *linkStore) ListMountWorkIDs(ctx context.Context, deviceID string) ([]string, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT DISTINCT work_id FROM link_mounts WHERE device_id = ? ORDER BY created_at`,
		deviceID)
	if err != nil {
		return nil, fmt.Errorf("link store: list mount work ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("link store: scan mount work id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("link store: list mount work ids: %w", err)
	}
	return out, nil
}

// SuspendMissionsForDevice (k-035) is the revoke-cascade sink the link
// Service calls after a durable revoke: suspend every active mission the
// device mounted, with a durable ADR-0010 handoff each. linkStore implements
// it on behalf of the owning SQLiteStore so the api package can wire the
// cascade by type-asserting the DeviceStore it already holds.
func (l *linkStore) SuspendMissionsForDevice(ctx context.Context, deviceID string) ([]string, error) {
	return l.sqlite.SuspendMissionsForDevice(ctx, deviceID)
}
