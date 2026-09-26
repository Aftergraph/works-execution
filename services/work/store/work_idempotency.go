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
	err := s.readQueryRow(ctx,
		`SELECT id FROM works WHERE idempotency_key = ?`, key,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.GetWork(ctx, id)
}
