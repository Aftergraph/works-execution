// Package audit emits CloudEvents v1.0 events for Work state transitions
// and persists them to a durable audit log.
//
// CloudEvents v1.0 is a vendor-neutral spec (https://cloudevents.io/) for
// describing event data. The mandatory context attributes we set on every
// event are:
//
//   id          — unique per event (UUIDv4)
//   source      — identifies the producer ("works-execution/control-plane")
//   specversion — "1.0"
//   type        — reverse-DNS, e.g. "com.works-execution.work.state_changed"
//   time        — RFC3339 timestamp when the event was produced
//   datacontenttype — "application/json"
//   subject     — work ID (the resource the event is about)
//
// The "data" payload is JSON describing the transition (from_state,
// to_state, attempt_id, correlation_id, ...).
//
// See services/work/store for the persistence backing (`work_audit_events`
// table) and services/api/audit_handler.go for the HTTP read API.
package audit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers itself
)

// SpecVersion is the CloudEvents spec version this package emits.
const SpecVersion = "1.0"

// Source is the producer identifier attached to every event as the
// "source" CloudEvents attribute.
const Source = "works-execution/control-plane"

// CloudEvent is the in-memory representation of a CloudEvents v1.0
// "structured-mode" event (JSON envelope).
//
// JSON tags follow the CloudEvents v1.0 spec attribute names exactly
// (lowercase, no transforms). The `Data` field is the event payload and
// is encoded as-is; it MUST itself be JSON-marshalable.
type CloudEvent struct {
	// Required context attributes.
	ID              string    `json:"id"`
	Source          string    `json:"source"`
	SpecVersion     string    `json:"specversion"`
	Type            string    `json:"type"`
	Time            time.Time `json:"time"`
	DataContentType string    `json:"datacontenttype,omitempty"`
	Subject         string    `json:"subject,omitempty"`

	// Optional extension attributes we attach on every event.
	WorkID         string `json:"workid,omitempty"`
	FromState      string `json:"fromstate,omitempty"`
	ToState        string `json:"tostate,omitempty"`
	CorrelationID  string `json:"correlationid,omitempty"`
	AttemptID      string `json:"attemptid,omitempty"`
	SchemaVersion  string `json:"schema_version,omitempty"`

	// Data is the event payload. Marshalled via the same encoder as the
	// envelope so the wire shape is well-formed JSON.
	Data any `json:"data,omitempty"`
}

// Event types we emit. Reverse-DNS per the CloudEvents convention.
const (
	TypeWorkCreated       = "com.works-execution.work.created"
	TypeWorkStateChanged  = "com.works-execution.work.state_changed"
	TypeWorkAttemptEnded  = "com.works-execution.work.attempt_ended"
)

// StateTransitionData is the `data` payload for state-change events.
type StateTransitionData struct {
	WorkID        string    `json:"work_id"`
	FromState     string    `json:"from_state"`
	ToState       string    `json:"to_state"`
	At            time.Time `json:"at"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Actor         string    `json:"actor,omitempty"`     // "api" | "worker" | "scheduler" | ...
	Reason        string    `json:"reason,omitempty"`
}

// Emitter is the audit interface used by the store. A nil Emitter is a
// no-op (audit disabled). The concrete *SQLiteEmitter implements it.
type Emitter interface {
	// Emit persists the event and returns its assigned ID. The event's
	// ID, Time, and Source fields MUST be filled in by the caller before
	// invocation; Emit is responsible for storage only.
	Emit(ctx context.Context, e *CloudEvent) error
}

// NewEvent builds a CloudEvent with the mandatory v1.0 context attributes
// populated. The caller still owns `Data` and extension attributes.
//
// `subject` is typically the work ID. `eventType` is one of the Type*
// constants in this package.
func NewEvent(eventType, subject string) *CloudEvent {
	return &CloudEvent{
		ID:              NewEventID(),
		Source:          Source,
		SpecVersion:     SpecVersion,
		Type:            eventType,
		Time:            time.Now().UTC(),
		DataContentType: "application/json",
		Subject:         subject,
		SchemaVersion:   "6",
	}
}

// NewEventID returns a 16-byte random hex ID prefixed with "evt_".
func NewEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand is the only failure mode and it is fatal in practice;
		// fall back to a time-based ID so callers can still make progress.
		ts := time.Now().UTC().UnixNano()
		return fmt.Sprintf("evt_%016x", ts)
	}
	return "evt_" + hex.EncodeToString(b[:])
}

// SQLiteEmitter persists CloudEvents to the `work_audit_events` table.
// It is safe for concurrent use only insofar as the underlying
// *sql.DB is — the existing store uses SetMaxOpenConns(1) so callers
// don't need to coordinate.
type SQLiteEmitter struct {
	DB     *sql.DB
	Logger *log.Logger
}

// NewSQLiteEmitter constructs an emitter. logger may be nil.
func NewSQLiteEmitter(db *sql.DB, logger *log.Logger) *SQLiteEmitter {
	return &SQLiteEmitter{DB: db, Logger: logger}
}

// Emit inserts the event row. Returns the assigned database row ID via
// the event's `id` field (already set by the caller via NewEvent).
func (e *SQLiteEmitter) Emit(ctx context.Context, ev *CloudEvent) error {
	if ev == nil {
		return errors.New("audit: nil event")
	}
	// ADR-0024 boundary law check (k-052). Placed immediately after the
	// existing nil guard and before any field defaults or mutation so
	// every event that reaches the audit table has passed the law. The
	// projection is constant (kind=event, signed=false), so this is a
	// no-op for all current callers -- zero behavior change; it fails
	// fast only if a future refactor drifts events toward being signed.
	if err := CheckEvent(ev); err != nil {
		return err
	}
	if ev.ID == "" {
		return errors.New("audit: event ID required")
	}
	if ev.SpecVersion == "" {
		ev.SpecVersion = SpecVersion
	}
	if ev.Source == "" {
		ev.Source = Source
	}
	if ev.Type == "" {
		return errors.New("audit: event type required")
	}

	payload, err := json.Marshal(ev.Data)
	if err != nil {
		return fmt.Errorf("audit: marshal data: %w", err)
	}

	_, err = e.DB.ExecContext(ctx, `
        INSERT INTO work_audit_events
            (id, occurred_at, source, type, subject, work_id,
             from_state, to_state, correlation_id, attempt_id,
             spec_version, data)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		ev.ID,
		ev.Time.UTC().Format(time.RFC3339Nano),
		ev.Source,
		ev.Type,
		nullable(ev.Subject),
		nullable(ev.WorkID),
		nullable(ev.FromState),
		nullable(ev.ToState),
		nullable(ev.CorrelationID),
		nullable(ev.AttemptID),
		ev.SpecVersion,
		string(payload),
	)
	if err != nil {
		if e.Logger != nil {
			e.Logger.Printf("audit emit failed: %v", err)
		}
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

// AuditEvent is a row read back from work_audit_events. `Data` is the
// raw JSON payload as stored (decoded by the caller).
type AuditEvent struct {
	ID            string    `json:"id"`
	OccurredAt    time.Time `json:"time"`
	Source        string    `json:"source"`
	Type          string    `json:"type"`
	Subject       string    `json:"subject,omitempty"`
	WorkID        string    `json:"work_id,omitempty"`
	FromState     string    `json:"from_state,omitempty"`
	ToState       string    `json:"to_state,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	AttemptID     string    `json:"attempt_id,omitempty"`
	SpecVersion   string    `json:"specversion"`
	Data          json.RawMessage `json:"data,omitempty"`
}

// ListFilter narrows the audit query. Empty fields are unbounded.
type ListFilter struct {
	Since time.Time
	Until time.Time
	WorkID string
	Type   string
	Limit  int
}

// Query reads audit events matching `f` from the store, ordered by
// occurred_at ASC then id ASC (stable, deterministic order).
func Query(ctx context.Context, db *sql.DB, f ListFilter) ([]AuditEvent, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	q := `SELECT id, occurred_at, source, type, subject, work_id,
                 from_state, to_state, correlation_id, attempt_id,
                 spec_version, data
          FROM work_audit_events WHERE 1=1`
	args := []any{}
	if !f.Since.IsZero() {
		q += " AND occurred_at >= ?"
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	if !f.Until.IsZero() {
		q += " AND occurred_at <= ?"
		args = append(args, f.Until.UTC().Format(time.RFC3339Nano))
	}
	if f.WorkID != "" {
		q += " AND work_id = ?"
		args = append(args, f.WorkID)
	}
	if f.Type != "" {
		q += " AND type = ?"
		args = append(args, f.Type)
	}
	q += " ORDER BY occurred_at ASC, id ASC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AuditEvent{}
	for rows.Next() {
		var ev AuditEvent
		var subject, workID, fromS, toS, corr, att sql.NullString
		var occurredStr string
		var data sql.NullString
		if err := rows.Scan(
			&ev.ID, &occurredStr, &ev.Source, &ev.Type, &subject, &workID,
			&fromS, &toS, &corr, &att, &ev.SpecVersion, &data,
		); err != nil {
			return nil, err
		}
		ev.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurredStr)
		if subject.Valid {
			ev.Subject = subject.String
		}
		if workID.Valid {
			ev.WorkID = workID.String
		}
		if fromS.Valid {
			ev.FromState = fromS.String
		}
		if toS.Valid {
			ev.ToState = toS.String
		}
		if corr.Valid {
			ev.CorrelationID = corr.String
		}
		if att.Valid {
			ev.AttemptID = att.String
		}
		if data.Valid && data.String != "" {
			ev.Data = json.RawMessage(data.String)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
