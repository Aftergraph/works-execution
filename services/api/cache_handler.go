// Package api — content-addressed cache endpoints (RFC-0005).
//
//   - GET  /v1/cache/{key}  → 200 + Entry on hit, 404 on miss
//   - PUT  /v1/cache/{key}  → 204; stores a successful node result
//
// The key is the fingerprint computed by the worker over the same
// canonical Key document the scheduler used when it offered the node
// (packages/cache). Both sides derive it independently from the work +
// node, so a claim proves byte-identical inputs without trusting the
// worker's claim about what it ran.
//
// Only exit-code-0 results are stored (cache.Put refuses failures):
// correctness over hit rate.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/cache"
)

// cacheEntryBody is the wire shape for both GET responses and PUT
// requests.
type cacheEntryBody struct {
	WorkID   string `json:"work_id"`
	NodeID   string `json:"node_id"`
	ExitCode int    `json:"exit_code"`
	LogTail  string `json:"log_tail,omitempty"`
}

// cacheHandler routes /v1/cache/{key}.
func (s *Server) cacheHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/v1/cache/")
	if key == "" || strings.Contains(key, "/") {
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.cacheGet(w, r, key)
	case http.MethodPut:
		s.cachePut(w, r, key)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
	}
}

// cacheGet returns the stored entry for the fingerprint.
func (s *Server) cacheGet(w http.ResponseWriter, r *http.Request, key string) {
	if s.CacheStore == nil {
		writeError(w, http.StatusServiceUnavailable, "cache_disabled", "cache store not configured")
		return
	}
	e, err := s.CacheStore.Lookup(r.Context(), key)
	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			writeError(w, http.StatusNotFound, "cache_miss", "no cached result for this fingerprint")
			return
		}
		writeError(w, http.StatusInternalServerError, "cache_lookup_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cacheEntryBody{
		WorkID:   e.WorkID,
		NodeID:   e.NodeID,
		ExitCode: e.ExitCode,
		LogTail:  e.LogTail,
	})
}

// cachePut stores a successful result. The body's exit_code must be 0;
// the store enforces this too (defense in depth).
func (s *Server) cachePut(w http.ResponseWriter, r *http.Request, key string) {
	if s.CacheStore == nil {
		writeError(w, http.StatusServiceUnavailable, "cache_disabled", "cache store not configured")
		return
	}
	var body cacheEntryBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if body.ExitCode != 0 {
		writeError(w, http.StatusBadRequest, "cache_refuses_failures",
			"only successful executions (exit_code=0) may be cached")
		return
	}
	if body.WorkID == "" || body.NodeID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "work_id and node_id are required")
		return
	}
	err := s.CacheStore.Put(r.Context(), cache.Entry{
		Fingerprint: key,
		WorkID:      body.WorkID,
		NodeID:      body.NodeID,
		ExitCode:    body.ExitCode,
		LogTail:     cache.TruncateLogTail([]byte(body.LogTail)),
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cache_put_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
