package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// GetWorkByIdempotencyKey resolves the canonical Work accepted for an
// idempotency key. It is a reconciliation primitive for controllers that lost
// the original POST response: callers recover the authoritative work_id from
// durable state instead of blindly resubmitting work.
//
// ErrNotFound means the key has never been accepted.
func (s *SQLiteStore) GetWorkByIdempotencyKey(ctx context.Context, key string) (*workgraph.Work, error) {
	if key == "" {
		return nil, ErrNotFound
	}

	var id string
	var intentHash sql.NullString
	var queueRequested sql.NullInt64
	err := s.readQueryRow(ctx,
		`SELECT id, creation_intent_hash, queue_requested FROM works WHERE idempotency_key = ?`, key,
	).Scan(&id, &intentHash, &queueRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	w, err := s.GetWork(ctx, id)
	if err != nil {
		return nil, err
	}
	if intentHash.Valid {
		w.CreationIntentHash = intentHash.String
	}
	if queueRequested.Valid {
		v := queueRequested.Int64 != 0
		w.QueueRequested = &v
	}
	return w, nil
}
