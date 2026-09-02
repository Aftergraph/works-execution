// Package api — resumable WORKS SSE (Task 2, Conversation V1).
//
// GET /v1/works/{id}/events streams the durable per-Work event journal as
// Server-Sent Events with a monotonic cursor, so a browser refresh or
// reconnect can resume exactly where it left off (plan: "SSE is resumable
// with monotonic sequence IDs; reset requires canonical snapshot refetch").
//
// This endpoint intentionally does NOT reuse /v1/ui/events: that feed is an
// HTML-view implementation detail with no durable cursor.
//
// Wire format per journal row:
//
//	id: <sequence>\nevent: <type>\ndata: <json>\n\n
//
// When no rows are available the stream emits a comment heartbeat:
//
//	: keepalive\n\n
//
// When the client's Last-Event-ID cursor is older than the oldest retained
// journal row (retention/compaction dropped the gap), the stream emits:
//
//	event: reset
//	data: {"reason":"cursor_expired","work_id":"..."}
//
// and closes; the consumer must refetch a canonical snapshot and reconnect
// with the fresh cursor.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/services/work/store"
)

// RouteRegistrar is the minimal registration surface WireWorkEventRoutes
// and WireResumeRoutes need. *http.ServeMux satisfies it (what api.go's
// Routes() uses today); chi.Router also satisfies it via its Handle
// method, so the integrator can mount these routes on either router.
type RouteRegistrar interface {
	Handle(pattern string, handler http.Handler)
}

// WorkEventLister is the minimal journal surface the SSE handler depends
// on. *store.SQLiteStore (Task 1's journal) satisfies it directly, keeping
// the handler compilable independently of the journal implementation.
type WorkEventLister interface {
	ListWorkEventsAfter(ctx context.Context, workID string, after int64, limit int) ([]store.WorkEvent, error)
	OldestWorkEventSequence(ctx context.Context, workID string) (int64, error)
}

// SSE polling cadence and batch size (plan-specified constants).
const (
	ssePollInterval = 750 * time.Millisecond
	ssePollLimit    = 200
)

// workEventsHandler serves GET /v1/works/{id}/events as SSE.
type workEventsHandler struct {
	lister WorkEventLister
}

// WireWorkEventRoutes mounts GET /v1/works/{id}/events on the given
// registrar, wrapped in the server's requireBearer middleware (same style
// as api.go's registrations).
//
// Wiring (integrator, in api.go Routes() after the existing registrations):
//
//	api.WireWorkEventRoutes(mux, s)
//
// s is the *Server; its Store (a *store.SQLiteStore) is adapted to
// WorkEventLister behind the scenes. Bearer-auth enforcement follows
// Server.AuthEnabled exactly like every other authenticated surface.
func WireWorkEventRoutes(reg RouteRegistrar, s *Server) {
	reg.Handle("GET /v1/works/{id}/events", s.requireBearer(&workEventsHandler{lister: storeLister{s.Store}}))
}

// storeLister adapts the concrete store to WorkEventLister. It tolerates a
// store that does not (yet) implement the journal methods by failing
// closed, and lazily ensures the work_events table exists via the bridge
// file's exported helper so the endpoint works before Task 1's shared
// migration lands.
type storeLister struct{ st store.Store }

func (l storeLister) ensureTable(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	type ensurer interface{ EnsureWorkEventsTable() error }
	if e, ok := l.st.(ensurer); ok {
		_ = e.EnsureWorkEventsTable()
	}
}

func (l storeLister) ListWorkEventsAfter(ctx context.Context, workID string, after int64, limit int) ([]store.WorkEvent, error) {
	l.ensureTable(ctx)
	type lister interface {
		ListWorkEventsAfter(ctx context.Context, workID string, after int64, limit int) ([]store.WorkEvent, error)
	}
	lst, ok := l.st.(lister)
	if !ok {
		return nil, errors.New("events: store does not implement ListWorkEventsAfter")
	}
	return lst.ListWorkEventsAfter(ctx, workID, after, limit)
}

func (l storeLister) OldestWorkEventSequence(ctx context.Context, workID string) (int64, error) {
	l.ensureTable(ctx)
	type oldest interface {
		OldestWorkEventSequence(ctx context.Context, workID string) (int64, error)
	}
	o, ok := l.st.(oldest)
	if !ok {
		return 0, errors.New("events: store does not implement OldestWorkEventSequence")
	}
	return o.OldestWorkEventSequence(ctx, workID)
}

// serveJournalList is the REST cursor listing the AVC conversation worker
// consumes (works-client listWorkEvents): GET /v1/works/{id}/events
// ?after=<seq>&limit=<n> returns the durable journal rows after the cursor
// as a JSON envelope array in the cross-repo wire shape
// (apps/conversation-worker WorksEventEnvelope: camelCase workId/
// observedAt, RFC3339Nano timestamps, opaque data passthrough). The
// response is the conversation mirror's only view of the journal; there is
// no pagination token beyond the monotonic sequence itself.
func (h *workEventsHandler) serveJournalList(w http.ResponseWriter, r *http.Request, workID string) {
	q := r.URL.Query()
	after := int64(0)
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "after must be a non-negative integer")
			return
		}
		after = n
	}
	limit := 200
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
		if n > 1000 {
			n = 1000
		}
		limit = n
	}

	rows, err := h.lister.ListWorkEventsAfter(r.Context(), workID, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "journal_unavailable", "journal unavailable")
		return
	}

	// Cross-repo envelope (AVC contracts WorksEventEnvelope). Data is
	// passed through untouched when valid; a corrupt row degrades to the
	// same "{}" the SSE stream uses.
	type envelope struct {
		ID         string          `json:"id"`
		WorkID     string          `json:"workId"`
		Type       string          `json:"type"`
		ObservedAt string          `json:"observedAt"`
		Sequence   int64           `json:"sequence"`
		Data       json.RawMessage `json:"data"`
	}
	events := make([]envelope, 0, len(rows))
	for _, ev := range rows {
		data := ev.Data
		if !json.Valid(data) {
			data = json.RawMessage("{}")
		}
		events = append(events, envelope{
			ID:         ev.ID,
			WorkID:     ev.WorkID,
			Type:       ev.Type,
			ObservedAt: ev.ObservedAt.UTC().Format(time.RFC3339Nano),
			Sequence:   ev.Sequence,
			Data:       data,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"work_id": workID, "events": events})
}

// ServeHTTP implements the SSE stream. See package comment for the wire
// contract.
func (h *workEventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if workID == "" {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}

	// Existence check WITHOUT leaking details: nonexistent works and any
	// other lookup failure collapse to the same generic 404 (plan: no
	// existence leak). The journal boundary helpers return
	// store.ErrNotFound exactly when the Work row does not exist.
	exists, err := h.workExists(r.Context(), workID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}

	// Task 8 (cross-repo mirror loop): the AVC conversation worker polls
	// the journal as REST JSON using query cursors
	// (works-client listWorkEvents: ?after=&limit=). SSE subscriptions
	// never send query cursors — Last-Event-ID is their resume mechanism —
	// so the two wire formats disambiguate cleanly on one route.
	if r.URL.Query().Has("after") || r.URL.Query().Has("limit") {
		h.serveJournalList(w, r, workID)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// A request WITHOUT Last-Event-ID is a fresh snapshot subscription:
	// cursor starts at 0 and every retained event replays. Reset only
	// applies to a client-SUPPLIED cursor that is older than the oldest
	// retained event (the gap can never be replayed). Header.Get
	// canonicalizes the lookup (the raw map key is "Last-Event-Id").
	rawCursor := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	cursor := int64(0)
	if rawCursor != "" {
		v, err := strconv.ParseInt(rawCursor, 10, 64)
		if err != nil || v < 0 {
			// A malformed cursor cannot be trusted to resume: force the
			// client to resnapshot.
			emitReset(w, flusher, workID)
			return
		}
		cursor = v
		oldest, err := h.lister.OldestWorkEventSequence(r.Context(), workID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
			return
		}
		if cursor < oldest {
			emitReset(w, flusher, workID)
			return
		}
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		rows, err := h.lister.ListWorkEventsAfter(ctx, workID, cursor, ssePollLimit)
		if err != nil {
			writeSSEErrorAfterHeaders(w, flusher, "journal unavailable")
			return
		}
		if len(rows) == 0 {
			// Comment heartbeat: keeps proxies/clients alive without
			// advancing the cursor.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-ctx.Done():
				return
			case <-time.After(ssePollInterval):
			}
			continue
		}

		for _, ev := range rows {
			data := string(ev.Data)
			if !json.Valid([]byte(data)) {
				data = "{}"
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Sequence, ev.Type, data); err != nil {
				return
			}
			cursor = ev.Sequence
		}
		flusher.Flush()
	}
}

// emitReset writes the reset event and closes the stream. The consumer
// must refetch the canonical snapshot and reconnect.
func emitReset(w http.ResponseWriter, flusher http.Flusher, workID string) {
	payload, _ := json.Marshal(map[string]string{
		"reason":  "cursor_expired",
		"work_id": workID,
	})
	fmt.Fprintf(w, "event: reset\ndata: %s\n\n", payload)
	flusher.Flush()
}

func writeSSEErrorAfterHeaders(w http.ResponseWriter, flusher http.Flusher, msg string) {
	// Headers are already sent; surface the failure in-band so the client
	// reconnects rather than hanging on a dead stream.
	fmt.Fprintf(w, "event: error\ndata: %q\n\n", msg)
	flusher.Flush()
}

// workExists reports whether the Work is readable via the store. Any
// store-level "not found" collapses to (false, nil); other errors are
// propagated.
func (h *workEventsHandler) workExists(ctx context.Context, workID string) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	_, err := h.lister.OldestWorkEventSequence(ctx, workID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return false, err
}
