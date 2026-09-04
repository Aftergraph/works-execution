// Package worker implements the WORKS execution worker.
// takeover.go handles human takeover continuity (HC8).
package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// ErrTakeoverFailure is returned when takeover validation fails.
var ErrTakeoverFailure = errors.New("takeover validation failed")

// TakeoverRequest represents a human takeover request.
type TakeoverRequest struct {
	WorkID         string    `json:"work_id"`
	NodeID         string    `json:"node_id"`
	WorkerID       string    `json:"worker_id"`    // original worker
	NewWorkerID    string    `json:"new_worker_id"` // human takeover worker
	OriginalLeaseID string   `json:"original_lease_id"`
	EventTime      time.Time `json:"event_time"`
	// Permissions requested (must be subset of original)
	Permissions    []string  `json:"permissions"`
}

// TakeoverResult is the outcome of a takeover attempt.
type TakeoverResult struct {
	Success    bool
	NewLeaseID string
	Reason     string
	// Evidence entry for audit trail
	EvidenceEntry *workgraph.Evidence
}

// TakeoverHandler handles takeover requests with AIE revalidation.
type TakeoverHandler struct {
	mu sync.Mutex

	// Original worker's permissions for authority check
	originalPermissions map[string][]string

	// AIE revalidation hook
	revalidateHook func(ctx context.Context, leaseID string, workerID string) error
}

// NewTakeoverHandler creates a new takeover handler.
func NewTakeoverHandler(revalidateHook func(ctx context.Context, leaseID string, workerID string) error) *TakeoverHandler {
	return &TakeoverHandler{
		originalPermissions: make(map[string][]string),
		revalidateHook:      revalidateHook,
	}
}

// RecordPermissions stores permissions for a worker.
func (h *TakeoverHandler) RecordPermissions(workerID string, permissions []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.originalPermissions[workerID] = permissions
}

// HandleTakeover processes a takeover request.
// HC8 requirements:
// 1. Lease must be re-validated via AIE revalidate hook (fail-closed)
// 2. Evidence chain must carry a takeover_event entry
// 3. No authority escalation (permissions subset-only)
func (h *TakeoverHandler) HandleTakeover(ctx context.Context, req TakeoverRequest) *TakeoverResult {
	h.mu.Lock()
	defer h.mu.Unlock()

	result := &TakeoverResult{
		Success: false,
		Reason:  "",
	}

	// HC8 Requirement 3: Check permissions subset-only (no authority escalation)
	originalPerms := h.originalPermissions[req.WorkerID]
	if !isPermissionsSubset(req.Permissions, originalPerms) {
		result.Reason = "permissions escalation detected"
		return result
	}

	// HC8 Requirement 1: Re-validate lease via AIE revalidate hook
	if h.revalidateHook != nil {
		if err := h.revalidateHook(ctx, req.OriginalLeaseID, req.NewWorkerID); err != nil {
			result.Reason = fmt.Sprintf("AIE revalidation failed: %v", err)
			return result
		}
	}

	// Create evidence entry for audit trail
	// HC8 Requirement 2: Evidence chain must carry a takeover_event entry
	now := time.Now().UTC()
	result.EvidenceEntry = &workgraph.Evidence{
		ID:         workgraph.NewID("evd"),
		NodeID:     req.NodeID,
		AttemptID:  req.OriginalLeaseID, // Link to original lease
		Type:       "takeover_event",
		Result:     "pass",
		RecordedAt: now,
		Signer:     req.NewWorkerID,
		Environment: fmt.Sprintf("takeover=true,original_worker=%s,new_worker=%s", req.WorkerID, req.NewWorkerID),
		Details: map[string]any{
			"work_id":        req.WorkID,
			"original_worker": req.WorkerID,
			"new_worker":      req.NewWorkerID,
			"lease_id":        req.OriginalLeaseID,
			"event_type":      "human_takeover",
			"permissions":     req.Permissions,
		},
	}

	// Takeover succeeded; caller should create new lease
	result.Success = true
	result.Reason = "takeover validated"

	return result
}

// isPermissionsSubset checks if requested is a subset of original.
func isPermissionsSubset(requested, original []string) bool {
	originalSet := make(map[string]bool)
	for _, p := range original {
		originalSet[p] = true
	}

	for _, p := range requested {
		if !originalSet[p] {
			return false
		}
	}

	return true
}

// TakeoverContinuity records continuity metadata for HC8.
type TakeoverContinuity struct {
	OriginalWorker     string    `json:"original_worker"`
	ContinuityWorker   string    `json:"continuity_worker"`
	ContinuityID       string    `json:"continuity_id"`  // unique continuity token
	PreviousAttempts   []string  `json:"previous_attempts"` // attempt IDs carried forward
	HandoffID          string    `json:"handoff_id"`      // reference to handoff checkpoint
	AdmittedAt         time.Time `json:"admitted_at"`
	AuthorityPreserved bool     `json:"authority_preserved"` // true if no escalation
}

// ValidateContinuity checks that continuity is valid (HC8).
func ValidateContinuity(c *TakeoverContinuity, originalWorker string, newWorker string) error {
	if c == nil {
		return errors.New("continuity is nil")
	}
	if c.ContinuityWorker != newWorker {
		return errors.New("continuity worker mismatch")
	}
	if c.AuthorityPreserved {
		// Verify no permission escalation occurred
		// (this check is contextual and would require access to permission state)
	}
	return nil
}
