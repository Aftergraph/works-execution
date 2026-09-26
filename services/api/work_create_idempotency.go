package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// idempotencyWorkGetter is intentionally narrower than store.Store so older
// test doubles do not need to grow a reconciliation method. SQLiteStore
// implements it through GetWorkByIdempotencyKey.
type idempotencyWorkGetter interface {
	GetWorkByIdempotencyKey(ctx context.Context, key string) (*workgraph.Work, error)
}

// lookupIdempotentWork returns the canonical Work already bound to the key.
// A nil result means no accepted Work is known. Stores without the optional
// lookup surface preserve the historical 409-on-conflict behavior.
func (s *Server) lookupIdempotentWork(ctx context.Context, key string) (*workgraph.Work, error) {
	if key == "" {
		return nil, nil
	}
	getter, ok := s.Store.(idempotencyWorkGetter)
	if !ok {
		return nil, nil
	}
	w, err := getter.GetWorkByIdempotencyKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return w, err
}

// sameWorkCreationIdentity compares only immutable creation intent.
//
// Work ID and correlation ID are controller-generated identities and are
// deliberately excluded: a replacement controller may mint fresh values after
// losing the original response. State/attempts/artifacts/evidence are also
// excluded because they evolve after durable acceptance.
//
// Queue is not a Work field; it is a convenience transition on POST. Replays
// never apply queue a second time. A caller that wants to queue an existing
// CREATED Work must use POST /v1/works/{id}/queue.
func sameWorkCreationIdentity(a, b *workgraph.Work) bool {
	if a == nil || b == nil {
		return false
	}
	return reflect.DeepEqual(a.Source, b.Source) &&
		reflect.DeepEqual(a.Objective, b.Objective) &&
		reflect.DeepEqual(a.Graph, b.Graph) &&
		reflect.DeepEqual(a.Requirements, b.Requirements) &&
		reflect.DeepEqual(a.Policy, b.Policy) &&
		reflect.DeepEqual(a.Mission, b.Mission)
}

// writeIdempotentReplay returns the authoritative existing Work without
// applying any new mutation. The header makes replay observable to clients
// without changing the frozen Work wire shape.
func writeIdempotentReplay(w http.ResponseWriter, existing *workgraph.Work) {
	w.Header().Set("X-Works-Idempotent-Replay", "true")
	writeJSON(w, http.StatusOK, existing)
}
