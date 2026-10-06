package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

type workerLeaseBindingV2Response struct {
	Schema        string `json:"schema"`
	WorkID        string `json:"work_id"`
	NodeID        string `json:"node_id"`
	WorkerID      string `json:"worker_id"`
	WorkerLeaseID string `json:"worker_lease_id"`
	AttemptID     string `json:"attempt_id"`
	GrantedAt     string `json:"granted_at"`
	ExpiresAt     string `json:"expires_at"`
	Status        string `json:"status"`
}

// readWorkerLeaseBindingV2 is a read-only platform bridge seam.
//
// Workers remain the only actors that claim leases through /v1/leases/grant.
// Runtime may call this endpoint only after a selected worker has self-claimed
// a node, so it can bind dispatch.acceptance/2.0 to the exact canonical lse_*
// and attempt ids without minting or mutating WORKS execution truth.
func (s *Server) readWorkerLeaseBindingV2(w http.ResponseWriter, r *http.Request) {
	workID := strings.TrimSpace(r.PathValue("id"))
	if workID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	if !s.authorizeDispatchV2Platform(w, r) {
		return
	}

	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	workerID := strings.TrimSpace(r.URL.Query().Get("worker_id"))
	if nodeID == "" || workerID == "" {
		writeError(w, http.StatusBadRequest, "lease_binding_query_invalid", "node_id and worker_id are required")
		return
	}
	if !validWorkerID(workerID) {
		writeError(w, http.StatusBadRequest, "lease_binding_query_invalid", "worker_id is invalid")
		return
	}

	leases, err := s.Store.LeasesByWorkID(r.Context(), workID)
	if err != nil {
		s.logf("lease binding read failed for work=%s: %v", workID, err)
		writeError(w, http.StatusInternalServerError, "lease_binding_read_failed", "failed to read WORKS lease binding")
		return
	}

	var matches []*workgraph.Lease
	for _, lease := range leases {
		if lease == nil ||
			lease.NodeID != nodeID ||
			lease.WorkerID != workerID ||
			lease.Status != workgraph.LeaseActive {
			continue
		}
		matches = append(matches, lease)
	}

	if len(matches) == 0 {
		writeError(w, http.StatusNotFound, "worker_lease_not_found", "no active WorkerLease matches the requested work/node/worker")
		return
	}
	if len(matches) != 1 {
		writeError(w, http.StatusConflict, "worker_lease_ambiguous", "multiple active WorkerLeases match the requested work/node/worker")
		return
	}

	lease := matches[0]
	now := time.Now().UTC()
	if !lease.ExpiresAt.After(now) {
		writeError(w, http.StatusConflict, "worker_lease_stale", "matching WorkerLease is expired")
		return
	}

	writeJSON(w, http.StatusOK, workerLeaseBindingV2Response{
		Schema:        "works.worker-lease-binding/1.0",
		WorkID:        lease.WorkID,
		NodeID:        lease.NodeID,
		WorkerID:      lease.WorkerID,
		WorkerLeaseID: lease.ID,
		AttemptID:     lease.AttemptID,
		GrantedAt:     lease.GrantedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:     lease.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Status:        string(lease.Status),
	})
}
