// Package api exposes the public HTTP API for works-execution.
//
// The API is the control plane boundary. It validates input, delegates
// persistence to the store, and enforces the state machine. It does not
// execute work — that is the worker's job.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// Server is the HTTP API server. It holds a reference to the store.
type Server struct {
	Store  store.Store
	Logger *log.Logger
	// ArtifactsDir is the directory where workers write artifact/log files.
	// Required for the GET /v1/works/{id}/nodes/{n}/logs endpoint. Optional
	// in V1; when nil, the log endpoint returns 503.
	ArtifactsDir string
}

// Routes returns an http.Handler with the public API mounted under /v1.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/works", s.worksHandler)               // POST = create, GET = list
	mux.HandleFunc("/v1/works/", s.workPathHandler)           // GET, POST .../cancel|queue, GET .../nodes/{n}/logs
	mux.HandleFunc("/v1/workers/ready", s.readyNodesHandler) // scheduler poll
	mux.HandleFunc("/v1/leases", s.leasesHandler)            // POST = grant
	mux.HandleFunc("/v1/leases/", s.leaseItemHandler)         // POST .../{action}
	mux.HandleFunc("/healthz", s.healthz)
	return s.recoverer(mux)
}

// workPathHandler routes all paths under /v1/works/. It splits out:
//   /v1/works/{id}                       -> GET workItemHandler
//   /v1/works/{id}/cancel|queue          -> POST workItemHandler
//   /v1/works/{id}/nodes/{n}/logs        -> GET workLogsHandler
func (s *Server) workPathHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	// parts: [id] OR [id, action] OR [id, "nodes", nodeID, "logs"]
	if len(parts) >= 2 && parts[1] == "nodes" {
		s.workLogsHandler(w, r)
		return
	}
	s.workItemHandler(w, r)
}

// recoverer wraps a handler with a panic guard.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logf("panic: %v", rec)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errBody{Error: code, Message: msg})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// worksHandler handles POST /v1/works (create) and GET /v1/works (list).
func (s *Server) worksHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createWork(w, r)
	case http.MethodGet:
		s.listWorks(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
	}
}

// createWork handles POST /v1/works.
//
// If the request body includes `{"queue": true}`, the work is immediately
// transitioned CREATED -> QUEUED so workers can pick it up. Otherwise it
// stays in CREATED until an explicit /queue call.
func (s *Server) createWork(w http.ResponseWriter, r *http.Request) {
	type createBody struct {
		workgraph.Work
		Queue bool `json:"queue,omitempty"`
	}
	var body createBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	w_in := body.Work
	if w_in.ID == "" {
		w_in.ID = workgraph.NewID("wrk")
	}
	if w_in.State == "" {
		w_in.State = workgraph.StateCreated
	}
	if err := w_in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	if err := s.Store.CreateWork(r.Context(), &w_in); err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
			return
		}
		s.logf("create work failed: %v", err)
		writeError(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	if body.Queue {
		if _, err := s.Store.UpdateState(r.Context(), w_in.ID, workgraph.StateQueued); err != nil {
			s.logf("auto-queue failed: %v", err)
		} else {
			w_in.State = workgraph.StateQueued
		}
	}
	writeJSON(w, http.StatusCreated, &w_in)
}

// listWorks handles GET /v1/works?limit=N.
func (s *Server) listWorks(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		var n int
		if _, err := fmt.Sscanf(l, "%d", &n); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	list, err := s.Store.ListWorks(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"works": list, "count": len(list)})
}

// workItemHandler routes /v1/works/{id}, /v1/works/{id}/cancel, /v1/works/{id}/queue.
func (s *Server) workItemHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		s.getWork(w, r, id)
	case r.Method == http.MethodPost && action == "cancel":
		s.cancelWork(w, r, id)
	case r.Method == http.MethodPost && action == "queue":
		s.queueWork(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
	}
}

func (s *Server) getWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.GetWork(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", id)
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

func (s *Server) cancelWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.UpdateState(r.Context(), id, workgraph.StateCancelled)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", id)
			return
		}
		writeError(w, http.StatusConflict, "cancel_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// queueWork transitions CREATED -> QUEUED. Returns 409 if the work is in a
// state that cannot transition to QUEUED.
func (s *Server) queueWork(w http.ResponseWriter, r *http.Request, id string) {
	wk, err := s.Store.UpdateState(r.Context(), id, workgraph.StateQueued)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", id)
			return
		}
		writeError(w, http.StatusConflict, "queue_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// readyNodesHandler is the scheduler poll endpoint.
//
// GET /v1/workers/ready?limit=N
//
// Returns a list of {work_id, node_id, run, env, timeout_s} entries that
// calling workers can pick up. Minimal capability filtering for V1; the full
// scheduler comes in slice 2.
func (s *Server) readyNodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	limit := 25
	if l := r.URL.Query().Get("limit"); l != "" {
		var n int
		if _, err := fmt.Sscanf(l, "%d", &n); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	list, err := s.Store.ListWorks(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	type readyItem struct {
		WorkID   string            `json:"work_id"`
		NodeID   string            `json:"node_id"`
		Run      string            `json:"run"`
		Env      map[string]string `json:"env,omitempty"`
		TimeoutS int               `json:"timeout_s,omitempty"`
	}
	var items []readyItem
	for _, work := range list {
		if work.State != workgraph.StateQueued && work.State != workgraph.StateRunning {
			continue
		}
		// Honor active leases: don't return a node another worker is leasing.
		active, err := s.Store.ActiveLeasesByWorkID(r.Context(), work.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "leases_failed", err.Error())
			return
		}
		for _, nid := range work.ReadyNodes(active) {
			n := work.Graph.Nodes[nid]
			items = append(items, readyItem{
				WorkID:   work.ID,
				NodeID:   nid,
				Run:      n.Run,
				Env:      n.Env,
				TimeoutS: n.TimeoutS,
			})
			if len(items) >= limit {
				break
			}
		}
		if len(items) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// workLogsHandler streams the artifact log for a (work_id, node_id) pair.
// Slice 2: serves the on-disk artifact file written by the worker. Slice 3
// will switch to true streaming-from-worker.
//
// Path: /v1/works/{id}/nodes/{n}/logs
func (s *Server) workLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	if s.ArtifactsDir == "" {
		writeError(w, http.StatusServiceUnavailable, "logs_unavailable", "ArtifactsDir not configured")
		return
	}
	// Path: /v1/works/{id}/nodes/{n}/logs
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[1] != "nodes" || parts[3] != "logs" {
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
		return
	}
	workID, nodeID := parts[0], parts[2]

	// Verify the work exists.
	if _, err := s.Store.GetWork(r.Context(), workID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "work_not_found", workID)
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	logPath := filepath.Join(s.ArtifactsDir, workID, nodeID+".log")
	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "logs_not_found", logPath)
			return
		}
		writeError(w, http.StatusInternalServerError, "stat_failed", err.Error())
		return
	}
	http.ServeFile(w, r, logPath)
}