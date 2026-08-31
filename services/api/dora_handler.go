package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/JonasAbde/works-execution/services/audit"
	"github.com/JonasAbde/works-execution/services/deploy"
)

// doraHandler implements GET /v1/dora.
//
// Query parameters:
//
//	since    RFC3339 timestamp (inclusive) — defaults to 30 days ago
//	until    RFC3339 timestamp (inclusive) — defaults to now
//	max      int — max Works to load for the computation (default 1000,
//	          clamped to [1, 5000])
//
// Response: deploy.Report (see services/deploy/dora.go).
//
// Errors:
//	400 — invalid query parameter
//	500 — store failure
func (s *Server) doraHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	q := r.URL.Query()

	now := time.Now().UTC()
	from := now.Add(-30 * 24 * time.Hour)
	to := now

	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_since", "since must be RFC3339")
			return
		}
		from = t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_until", "until must be RFC3339")
			return
		}
		to = t
	}
	if !from.Before(to) && !from.Equal(to) {
		writeError(w, http.StatusBadRequest, "invalid_window", "since must be <= until")
		return
	}

	max := 1000
	if v := q.Get("max"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_max", "max must be a positive integer")
			return
		}
		if n > 5000 {
			n = 5000
		}
		max = n
	}

	works, err := s.Store.ListWorks(r.Context(), max)
	if err != nil {
		s.logf("dora list works: %v", err)
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}

	// Pull the matching audit slice for MTTR computation. We use a
	// generous limit because MTTR only needs the FAILED -> SUCCEEDED
	// pairs in the window.
	events, err := s.Store.ListAuditEvents(r.Context(), audit.ListFilter{
		Since: from,
		Until: to,
		Limit: 2000,
	})
	if err != nil {
		s.logf("dora list audit: %v", err)
		writeError(w, http.StatusInternalServerError, "audit_failed", err.Error())
		return
	}

	report := deploy.Compute(works, events, deploy.Window{From: from, To: to})
	writeJSON(w, http.StatusOK, report)
}
