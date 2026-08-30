package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// grantLeaseBody is the POST /v1/leases/grant request payload.
type grantLeaseBody struct {
	WorkID     string `json:"work_id"`
	NodeID     string `json:"node_id"`
	WorkerID   string `json:"worker_id"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"` // default 25
}

// leasesHandler routes /v1/leases (POST = grant) and /v1/leases/{id}/action.
func (s *Server) leasesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.grantLease(w, r)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
}

// leaseItemHandler routes /v1/leases/{id}/{action} where action is one of
// heartbeat, complete, release, revoke.
func (s *Server) leaseItemHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/leases/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "lease id required")
		return
	}
	leaseID := parts[0]
	action := parts[1]

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	switch action {
	case "heartbeat":
		s.heartbeatLease(w, r, leaseID)
	case "complete":
		s.completeLease(w, r, leaseID)
	case "release":
		s.releaseLease(w, r, leaseID)
	case "revoke":
		s.revokeLease(w, r, leaseID)
	default:
		writeError(w, http.StatusNotFound, "not_found", action)
	}
}

// grantLease is POST /v1/leases/grant.
func (s *Server) grantLease(w http.ResponseWriter, r *http.Request) {
	var body grantLeaseBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if body.WorkID == "" || body.NodeID == "" || body.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "work_id, node_id, worker_id are required")
		return
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second
	lease, attempt, err := s.Store.GrantLease(r.Context(), body.WorkID, body.NodeID, body.WorkerID, ttl)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "work_not_found", body.WorkID)
		case errors.Is(err, store.ErrLeaseConflict):
			writeError(w, http.StatusConflict, "lease_conflict", "node already leased")
		default:
			s.logf("grant lease: %v", err)
			writeError(w, http.StatusInternalServerError, "grant_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"lease":   lease,
		"attempt": attempt,
	})
}

// heartbeatLeaseBody is POST /v1/leases/{id}/heartbeat.
type heartbeatLeaseBody struct {
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

func (s *Server) heartbeatLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body heartbeatLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body) // body optional
	ttl := time.Duration(body.TTLSeconds) * time.Second
	lease, err := s.Store.RenewLease(r.Context(), leaseID, ttl)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		case errors.Is(err, store.ErrLeaseNotActive):
			writeError(w, http.StatusConflict, "lease_not_active", leaseID)
		default:
			writeError(w, http.StatusInternalServerError, "renew_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

// completeLeaseBody is POST /v1/leases/{id}/complete.
type completeLeaseBody struct {
	ExitCode int                `json:"exit_code"`
	Artifact *workgraph.Artifact `json:"artifact,omitempty"`
	Evidence []workgraph.Evidence `json:"evidence,omitempty"`
}

func (s *Server) completeLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body completeLeaseBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	wk, err := s.Store.CompleteLease(r.Context(), leaseID, body.ExitCode, body.Artifact, body.Evidence)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		case errors.Is(err, store.ErrLeaseNotActive):
			writeError(w, http.StatusConflict, "lease_not_active", leaseID)
		default:
			writeError(w, http.StatusInternalServerError, "complete_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// releaseLeaseBody is POST /v1/leases/{id}/release.
type releaseLeaseBody struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) releaseLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body releaseLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.Store.ReleaseLease(r.Context(), leaseID, body.Reason); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		default:
			writeError(w, http.StatusConflict, "release_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": leaseID})
}

// revokeLeaseBody is POST /v1/leases/{id}/revoke.
type revokeLeaseBody struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) revokeLease(w http.ResponseWriter, r *http.Request, leaseID string) {
	var body revokeLeaseBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.Store.RevokeLease(r.Context(), leaseID, body.Reason); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "lease_not_found", leaseID)
		default:
			writeError(w, http.StatusConflict, "revoke_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": leaseID})
}

// ReaperConfig controls the lease-reaper goroutine.
type ReaperConfig struct {
	// Interval is how often the reaper scans for expired leases.
	// Default: 5s. Lower means faster SLO compliance at higher DB cost.
	Interval time.Duration
	// BatchLimit caps the number of leases expired per tick to bound work.
	// Default: 100.
	BatchLimit int
}

// RunLeaseReaper blocks until ctx is done, periodically expiring stale
// leases. The reaper is idempotent: it only marks ACTIVE leases as EXPIRED
// and only cancels attempts whose status is still 'running'. A future slice
// can run multiple reapers safely.
//
// This function is intended to be launched as `go api.RunLeaseReaper(...)`
// from cmd/works-api.
func RunLeaseReaper(ctx context.Context, s store.Store, cfg ReaperConfig) error {
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.BatchLimit == 0 {
		cfg.BatchLimit = 100
	}
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := reapOnce(ctx, s, cfg.BatchLimit); err != nil {
				// best-effort: log and continue
				fmt.Printf("reaper: %v\n", err)
			} else if n > 0 {
				fmt.Printf("reaper: expired %d lease(s)\n", n)
			}
		}
	}
}

// reapOnce performs a single reaper pass. Returns the number of leases expired.
func reapOnce(ctx context.Context, s store.Store, limit int) (int, error) {
	expired, err := s.ListExpiredLeases(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("list expired: %w", err)
	}
	n := 0
	for _, l := range expired {
		// Mark lease EXPIRED, cancel attempt. Both must be idempotent.
		if err := s.RevokeLease(ctx, l.ID, "lease expired"); err != nil {
			// Skip — probably already revoked by a concurrent reaper or worker.
			continue
		}
		_ = s.MarkAttemptCancelled(ctx, l.AttemptID, "lease expired")
		n++
	}
	return n, nil
}