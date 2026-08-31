package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/JonasAbde/works-execution/services/audit"
)

// auditEventsHandler implements GET /v1/audit-events.
//
// Query parameters:
//
//	since    RFC3339 timestamp (inclusive) — lower bound on event time
//	until    RFC3339 timestamp (inclusive) — upper bound on event time
//	work_id  filter to one work
//	type     filter to one CloudEvent type
//	limit    max events to return (clamped to [1, 1000], default 200)
//
// Response: {"events":[<CloudEvent>, ...], "count": N}
//
// Each event is the CloudEvents v1.0 structured-mode envelope with
// extension attributes denormalized from the row.
//
// Errors:
//	400 — invalid query parameter
//	500 — store failure
func (s *Server) auditEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	q := r.URL.Query()

	filter := audit.ListFilter{
		WorkID: q.Get("work_id"),
		Type:   q.Get("type"),
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_since", "since must be RFC3339")
			return
		}
		filter.Since = t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_until", "until must be RFC3339")
			return
		}
		filter.Until = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
		filter.Limit = n
	}

	events, err := s.Store.ListAuditEvents(r.Context(), filter)
	if err != nil {
		s.logf("audit list: %v", err)
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"count":  len(events),
	})
}
