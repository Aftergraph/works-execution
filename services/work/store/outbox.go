package store

// Transactional outbox (ADR-0033).
//
// WHY THIS EXISTS. Before this slice, services/api/publisher_hook.go
// published a terminal Work's GitHub status from a background goroutine
// fired after the state mutation had already committed. That is a
// fire-and-forget side effect with no durable record: a crash between
// commit and the HTTP call loses the status update permanently, and
// nothing in the database knows it was ever owed.
//
// An outbox fixes the ordering by moving the DECISION to be durable and
// the DELIVERY to be retryable:
//
//	enqueue  — inside the SAME transaction as the state change
//	deliver  — afterwards, by a dispatcher, with retries and a claim
//
// This is the reason the enqueue lives in the store and not in the API.
// An outbox written outside the state mutation's transaction cannot
// guarantee the pairing in either direction: enqueue-then-crash publishes
// a state that never happened, and commit-then-crash drops a side effect
// that was owed. Only same-transaction makes "the Work is SUCCEEDED" and
//"a status update is owed" a single atomic fact.
//
// WHAT THIS DOES AND DOES NOT GUARANTEE. Delivery is AT-LEAST-ONCE, not
// exactly-once. The claim mechanism guarantees that two live dispatchers
// never hold the same row at the same time, so a healthy system delivers
// once. It cannot rule out a duplicate in the one window no database
// transaction can close: the external side effect succeeded, then the
// process died before MarkOutboxDelivered committed. That is inherent, not
// a defect of this implementation, and it is why every handler must be
// idempotent — services/publisher already requires exactly that ("MUST be
// idempotent on (Repository, SHA)").

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// OutboxStatus is the delivery lifecycle of an outbox row.
type OutboxStatus string

const (
	// OutboxPending is drainable: no dispatcher currently owns the row.
	OutboxPending OutboxStatus = "PENDING"
	// OutboxClaimed is owned by one dispatcher until claim_expires_at.
	OutboxClaimed OutboxStatus = "CLAIMED"
	// OutboxDelivered is terminal success.
	OutboxDelivered OutboxStatus = "DELIVERED"
	// OutboxDead is terminal failure: attempts were exhausted. The row is
	// kept, never deleted — an undelivered side effect must remain
	// auditable rather than vanishing.
	OutboxDead OutboxStatus = "DEAD"
)

// OutboxTopicWorkTerminal is emitted when a Work reaches a terminal state.
// It is a WORK-level fact, not a GitHub-specific one: WORKS owns the fact
// that the Work finished, and whatever consumer renders that fact (the
// GitHub publisher today) subscribes to the topic. Coupling the store to a
// publisher would invert that.
const OutboxTopicWorkTerminal = "work.terminal"

// TerminalWorkPayload is the body of an OutboxTopicWorkTerminal entry.
type TerminalWorkPayload struct {
	WorkID string `json:"work_id"`
	State  string `json:"state"`
	From   string `json:"from"`
}

// OutboxEntry is one durable side-effect obligation.
type OutboxEntry struct {
	ID             string
	Topic          string
	IdempotencyKey string
	WorkID         string
	PayloadJSON    string
	Status         OutboxStatus
	Attempts       int
	MaxAttempts    int
	CreatedAt      time.Time
	ClaimedBy      string
	LastError      string
}

// OutboxHandler delivers one entry. Returning nil marks it delivered;
// returning an error releases the claim for a later retry (or marks the
// row DEAD once attempts are exhausted). Handlers MUST be idempotent — see
// the at-least-once note above.
type OutboxHandler func(ctx context.Context, e OutboxEntry) error

// OutboxStore is the narrow surface the dispatcher needs. It is separate
// from Store so a dispatcher can be driven against any implementation, and
// so tests can exercise delivery without the full store surface.
type OutboxStore interface {
	ClaimOutbox(ctx context.Context, dispatcherID string, limit int, claimTTL time.Duration, now time.Time) ([]OutboxEntry, error)
	MarkOutboxDelivered(ctx context.Context, id, dispatcherID string, now time.Time) error
	FailOutboxAttempt(ctx context.Context, id, dispatcherID, reason string, now time.Time) error
}

// TerminalWorkIdempotencyKey is the deterministic key for the
// publish-on-terminal side effect of one Work reaching one state.
//
// Determinism is the whole point: it is a UNIQUE column, so a retried
// mutation, a replayed webhook, or two dispatchers racing all collapse to
// the same row instead of producing duplicate deliveries. It is namespaced
// by the target state because a Work legitimately reaches terminal at
// most once per state, but FAILED-then-SUSPENDED is two distinct facts.
func TerminalWorkIdempotencyKey(workID string, to workgraph.State) string {
	return "work.terminal:" + workID + ":" + string(to)
}

// enqueueOutboxTx inserts a delivery obligation using the caller's
// transaction. It MUST be called before that transaction commits: the
// enqueue and the state change it describes become durable together, or
// not at all.
//
// The insert is INSERT OR IGNORE on the UNIQUE idempotency_key, so
// replaying the mutation is a no-op rather than a duplicate.
func enqueueOutboxTx(ctx context.Context, tx *sql.Tx, e OutboxEntry) error {
	var workID any
	if e.WorkID != "" {
		workID = e.WorkID
	}
	var maxAttempts int
	if e.MaxAttempts > 0 {
		maxAttempts = e.MaxAttempts
	} else {
		maxAttempts = defaultOutboxMaxAttempts
	}
	_, err := tx.ExecContext(ctx, `
        INSERT OR IGNORE INTO outbox
            (id, topic, idempotency_key, work_id, payload_json, created_at,
             status, attempts, max_attempts)
        VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)
    `, e.ID, e.Topic, e.IdempotencyKey, workID, e.PayloadJSON,
		e.CreatedAt.UTC().Format(time.RFC3339Nano), string(OutboxPending), maxAttempts)
	if err != nil {
		return fmt.Errorf("outbox enqueue %s: %w", e.IdempotencyKey, err)
	}
	return nil
}

// enqueueWorkTerminal records that a Work reached a terminal state, in the
// same transaction as the state change.
func enqueueWorkTerminal(ctx context.Context, tx *sql.Tx, workID string, from, to workgraph.State, now time.Time) error {
	payload := mustJSON(TerminalWorkPayload{
		WorkID: workID,
		State:  string(to),
		From:   string(from),
	})
	return enqueueOutboxTx(ctx, tx, OutboxEntry{
		ID:             workgraph.NewID("obx"),
		Topic:          OutboxTopicWorkTerminal,
		IdempotencyKey: TerminalWorkIdempotencyKey(workID, to),
		WorkID:         workID,
		PayloadJSON:    payload,
		CreatedAt:      now,
		Status:         OutboxPending,
	})
}

// ErrOutboxNotClaimed is returned when a dispatcher tries to settle a row
// it does not own — either it never claimed it, or its claim expired and
// another dispatcher took over. This is what stops a slow dispatcher from
// marking another dispatcher's in-flight delivery as delivered.
var ErrOutboxNotClaimed = errors.New("outbox entry not claimed by this dispatcher")

// ClaimOutbox atomically claims up to `limit` drainable entries for
// dispatcherID and returns only those this caller won.
//
// Two mechanisms, both required:
//
//  1. Candidates are selected then claimed by a conditional UPDATE whose
//     WHERE re-asserts the claimable predicate. RowsAffected==1 is the
//     only proof of ownership. A dispatcher that merely SELECTed rows and
//     then delivered them would double-deliver under concurrency.
//  2. A CLAIMED row whose claim_expires_at has passed is reclaimable, so a
//     dispatcher that crashes mid-delivery does not strand its rows
//     forever. The expired-claim window is the at-least-once cost: a
//     dispatcher that stalls past its TTL may have its row redelivered.
//     TTL must exceed the slowest expected delivery.
//
// `now` is a parameter rather than a call to time.Now() so delivery tests
// are deterministic rather than sleep-driven.
func (s *SQLiteStore) ClaimOutbox(ctx context.Context, dispatcherID string, limit int, claimTTL time.Duration, now time.Time) ([]OutboxEntry, error) {
	if dispatcherID == "" {
		return nil, errors.New("outbox: dispatcher id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	if claimTTL <= 0 {
		claimTTL = 30 * time.Second
	}
	now = now.UTC()

	rows, err := s.readQuery(ctx, `
        SELECT id, topic, idempotency_key, COALESCE(work_id,''), payload_json, attempts, max_attempts
        FROM outbox
        WHERE status = ?
           OR (status = ? AND claim_expires_at IS NOT NULL AND claim_expires_at < ?)
        ORDER BY created_at ASC
        LIMIT ?
    `, string(OutboxPending), string(OutboxClaimed),
		now.Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id, topic, key, workID, payload string
		attempts, maxAttempts           int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.topic, &c.key, &c.workID, &c.payload, &c.attempts, &c.maxAttempts); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// The read pool is a different connection; close it before taking the
	// single writer connection or the claim can deadlock against it.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	claimedAt := now.Format(time.RFC3339Nano)
	claimExpires := now.Add(claimTTL).Format(time.RFC3339Nano)
	var out []OutboxEntry
	for _, c := range candidates {
		res, err := s.db.ExecContext(ctx, `
            UPDATE outbox
            SET status = ?, claimed_by = ?, claimed_at = ?, claim_expires_at = ?, attempts = attempts + 1
            WHERE id = ?
              AND (status = ?
                   OR (status = ? AND claim_expires_at IS NOT NULL AND claim_expires_at < ?))
        `, string(OutboxClaimed), dispatcherID, claimedAt, claimExpires, c.id,
			string(OutboxPending), string(OutboxClaimed), claimedAt)
		if err != nil {
			return out, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return out, err
		}
		if affected != 1 {
			// Another dispatcher won this row. Skip it; it is not ours to
			// deliver and claiming it would be the double-delivery bug.
			continue
		}
		out = append(out, OutboxEntry{
			ID:             c.id,
			Topic:          c.topic,
			IdempotencyKey: c.key,
			WorkID:         c.workID,
			PayloadJSON:    c.payload,
			Status:         OutboxClaimed,
			Attempts:       c.attempts + 1,
			MaxAttempts:    c.maxAttempts,
			CreatedAt:      now,
			ClaimedBy:      dispatcherID,
		})
	}
	return out, nil
}

// MarkOutboxDelivered settles a row this dispatcher holds. The claim
// predicate is re-asserted so a dispatcher whose claim expired cannot
// overwrite a DELIVERED written by whoever redelivered the row.
func (s *SQLiteStore) MarkOutboxDelivered(ctx context.Context, id, dispatcherID string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
        UPDATE outbox SET status = ?, delivered_at = ?, last_error = NULL
        WHERE id = ? AND status = ? AND claimed_by = ?
    `, string(OutboxDelivered), now.UTC().Format(time.RFC3339Nano),
		id, string(OutboxClaimed), dispatcherID)
	if err != nil {
		return err
	}
	return outboxSettleRows(res, id, dispatcherID)
}

// FailOutboxAttempt records a delivery failure. If the entry has used up
// its attempts it becomes DEAD (terminal, retained for audit); otherwise
// it returns to PENDING for a later drain. Either way the row is released
// rather than left CLAIMED, so a failing handler cannot wedge the outbox.
func (s *SQLiteStore) FailOutboxAttempt(ctx context.Context, id, dispatcherID, reason string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
        UPDATE outbox
        SET status = CASE WHEN attempts >= max_attempts THEN ? ELSE ? END,
            claimed_by = NULL,
            claimed_at = NULL,
            claim_expires_at = NULL,
            last_error = ?
        WHERE id = ? AND status = ? AND claimed_by = ?
    `, string(OutboxDead), string(OutboxPending), reason,
		id, string(OutboxClaimed), dispatcherID)
	if err != nil {
		return err
	}
	return outboxSettleRows(res, id, dispatcherID)
}

func outboxSettleRows(res sql.Result, id, dispatcherID string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s (dispatcher %s)", ErrOutboxNotClaimed, id, dispatcherID)
	}
	return nil
}

// GetOutboxEntry returns one row by id, for tests and operator tooling.
func (s *SQLiteStore) GetOutboxEntry(ctx context.Context, id string) (*OutboxEntry, error) {
	var e OutboxEntry
	var statusStr, createdStr, claimedBy string
	var lastError string
	err := s.readQueryRow(ctx, `
        SELECT id, topic, idempotency_key, COALESCE(work_id,''), payload_json,
               status, attempts, max_attempts, created_at, COALESCE(claimed_by,''), COALESCE(last_error,'')
        FROM outbox WHERE id = ?
    `, id).Scan(&e.ID, &e.Topic, &e.IdempotencyKey, &e.WorkID, &e.PayloadJSON,
		&statusStr, &e.Attempts, &e.MaxAttempts, &createdStr, &claimedBy, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.Status = OutboxStatus(statusStr)
	e.CreatedAt, _ = parseTime(createdStr)
	e.ClaimedBy = claimedBy
	e.LastError = lastError
	return &e, nil
}

// OutboxEntryForKey returns the row for an idempotency key, so a test or
// operator can confirm a replayed mutation produced no second obligation.
func (s *SQLiteStore) OutboxEntryForKey(ctx context.Context, key string) (*OutboxEntry, error) {
	var id string
	err := s.readQueryRow(ctx, `SELECT id FROM outbox WHERE idempotency_key = ?`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.GetOutboxEntry(ctx, id)
}

// CountOutboxByStatus counts rows in a status. Used by tests and metrics.
func (s *SQLiteStore) CountOutboxByStatus(ctx context.Context, st OutboxStatus) (int, error) {
	var n int
	err := s.readQueryRow(ctx, `SELECT COUNT(*) FROM outbox WHERE status = ?`, string(st)).Scan(&n)
	return n, err
}

// ListOutboxByWorkID returns every obligation recorded for one Work, in
// creation order.
func (s *SQLiteStore) ListOutboxByWorkID(ctx context.Context, workID string) ([]OutboxEntry, error) {
	rows, err := s.readQuery(ctx, `
        SELECT id, topic, idempotency_key, payload_json, status, attempts, max_attempts, created_at
        FROM outbox WHERE work_id = ? ORDER BY created_at ASC
    `, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		var statusStr, createdStr string
		if err := rows.Scan(&e.ID, &e.Topic, &e.IdempotencyKey, &e.PayloadJSON,
			&statusStr, &e.Attempts, &e.MaxAttempts, &createdStr); err != nil {
			return nil, err
		}
		e.WorkID = workID
		e.Status = OutboxStatus(statusStr)
		e.CreatedAt, _ = parseTime(createdStr)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DecodeTerminalWorkPayload parses a work.terminal payload for a
// dispatcher handler. A payload that does not parse, or that carries no
// work id, is an error: the handler must not treat a corrupt entry as
// delivered, because that would retire an obligation nobody ever carried
// out.
func DecodeTerminalWorkPayload(payload string) (TerminalWorkPayload, error) {
	var p TerminalWorkPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return TerminalWorkPayload{}, fmt.Errorf("outbox: malformed terminal payload: %w", err)
	}
	if p.WorkID == "" {
		return TerminalWorkPayload{}, errors.New("outbox: terminal payload missing work_id")
	}
	return p, nil
}

// defaultOutboxMaxAttempts bounds retries for an obligation whose producer
// did not choose a limit. Three is enough to ride out a transient GitHub 5xx
// without letting a permanently broken entry spin.
const defaultOutboxMaxAttempts = 3

// OutboxConfig configures RunOutboxDispatcher.
type OutboxConfig struct {
	// DispatcherID identifies this dispatcher in claimed_by. It must be
	// unique per process; generate it from the hostname and pid.
	DispatcherID string
	// Interval is the drain period. Default 1s.
	Interval time.Duration
	// BatchLimit caps entries claimed per tick. Default 50.
	BatchLimit int
	// ClaimTTL is how long a claim survives without being settled, after
	// which another dispatcher may take the row. Default 30s. Must exceed
	// the slowest expected single delivery.
	ClaimTTL time.Duration
	// Now injects the clock. Nil means time.Now. Tests inject a fixed
	// clock so claim expiry is exercised without sleeping.
	Now func() time.Time
}

// DrainOutbox claims and delivers one batch, returning how many entries it
// settled. Exported separately from the loop so a test (or an operator
// running a one-shot drain) can drive delivery deterministically without
// starting a ticker.
func DrainOutbox(ctx context.Context, st OutboxStore, h OutboxHandler, cfg OutboxConfig) (int, error) {
	if cfg.DispatcherID == "" {
		return 0, errors.New("outbox: dispatcher id is required")
	}
	now := time.Now().UTC()
	if cfg.Now != nil {
		now = cfg.Now().UTC()
	}
	entries, err := st.ClaimOutbox(ctx, cfg.DispatcherID, cfg.BatchLimit, cfg.ClaimTTL, now)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, e := range entries {
		if err := h(ctx, e); err != nil {
			// A failed delivery is not a failed drain: the row goes back to
			// PENDING (or DEAD) and the remaining entries still get their
			// turn. Only a settle error is propagated, because that means
			// this dispatcher no longer owns the row.
			if serr := st.FailOutboxAttempt(ctx, e.ID, cfg.DispatcherID, err.Error(), now); serr != nil {
				return delivered, serr
			}
			continue
		}
		if err := st.MarkOutboxDelivered(ctx, e.ID, cfg.DispatcherID, now); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

// RunOutboxDispatcher drains the outbox until ctx is cancelled.
//
// It is safe to run several concurrently against the same database, and
// safe to run alongside request handling: it only writes outbox rows it
// has claimed, and the claim CAS is what prevents overlap. Every error is
// logged-and-continue rather than fatal — a dispatcher that exits on a
// transient failure silently stops delivering, which is the failure mode
// the outbox exists to prevent.
//
// Intended to be launched as `go api.RunOutboxDispatcher(...)` from
// cmd/works-api.
func RunOutboxDispatcher(ctx context.Context, st OutboxStore, h OutboxHandler, cfg OutboxConfig) error {
	if cfg.Interval == 0 {
		cfg.Interval = time.Second
	}
	if cfg.BatchLimit == 0 {
		cfg.BatchLimit = 50
	}
	if cfg.ClaimTTL == 0 {
		cfg.ClaimTTL = 30 * time.Second
	}
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := DrainOutbox(ctx, st, h, cfg); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Printf("outbox: drain: %v\n", err)
			}
		}
	}
}