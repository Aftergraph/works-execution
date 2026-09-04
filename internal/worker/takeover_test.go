// Package worker implements the WORKS execution worker.
// takeover_test.go tests HC8 takeover continuity.
package worker

import (
	"context"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func TestTakeoverHandler_PermissionsSubset(t *testing.T) {
	handler := NewTakeoverHandler(func(ctx context.Context, leaseID, workerID string) error {
		return nil // Pass validation
	})

	// Record original permissions
	originalPerms := []string{"read", "write", "secrets"}
	handler.RecordPermissions("worker-1", originalPerms)

	// Test: valid subset should succeed
	req := TakeoverRequest{
		WorkID:         "work-123",
		NodeID:         "node-1",
		WorkerID:       "worker-1",
		NewWorkerID:    "worker-2",
		OriginalLeaseID: "lease-123",
		EventTime:      time.Now(),
		Permissions:    []string{"read", "write"}, // Subset
	}

	result := handler.HandleTakeover(context.Background(), req)
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Reason)
	}
	if result.EvidenceEntry == nil {
		t.Error("expected evidence entry")
	}
	if result.EvidenceEntry.Type != "takeover_event" {
		t.Errorf("expected type 'takeover_event', got: %s", result.EvidenceEntry.Type)
	}
}

func TestTakeoverHandler_PermissionsEscalation(t *testing.T) {
	handler := NewTakeoverHandler(func(ctx context.Context, leaseID, workerID string) error {
		return nil // Pass validation
	})

	// Record original permissions
	handler.RecordPermissions("worker-1", []string{"read"})

	// Test: escalation should fail
	req := TakeoverRequest{
		WorkID:         "work-123",
		NodeID:         "node-1",
		WorkerID:       "worker-1",
		NewWorkerID:    "worker-2",
		OriginalLeaseID: "lease-123",
		EventTime:      time.Now(),
		Permissions:    []string{"read", "write", "secrets"}, // Escalation!
	}

	result := handler.HandleTakeover(context.Background(), req)
	if result.Success {
		t.Error("expected failure due to escalation")
	}
	if result.Reason != "permissions escalation detected" {
		t.Errorf("expected 'permissions escalation detected', got: %s", result.Reason)
	}
}

func TestTakeoverHandler_AIERevalidationFail(t *testing.T) {
	failed := false
	handler := NewTakeoverHandler(func(ctx context.Context, leaseID, workerID string) error {
		failed = true
		return ErrTakeoverFailure
	})

	req := TakeoverRequest{
		WorkID:         "work-123",
		NodeID:         "node-1",
		WorkerID:       "worker-1",
		NewWorkerID:    "worker-2",
		OriginalLeaseID: "lease-123",
		EventTime:      time.Now(),
		Permissions:    []string{"read"},
	}

	result := handler.HandleTakeover(context.Background(), req)
	if result.Success {
		t.Error("expected failure due to AIE revalidation")
	}
	if !failed {
		t.Error("revalidate hook should have been called")
	}
}

func TestTakeoverHandler_EvidenceEntry(t *testing.T) {
	handler := NewTakeoverHandler(func(ctx context.Context, leaseID, workerID string) error {
		return nil
	})

	handler.RecordPermissions("worker-1", []string{"read"})

	req := TakeoverRequest{
		WorkID:         "work-123",
		NodeID:         "node-1",
		WorkerID:       "worker-1",
		NewWorkerID:    "worker-2",
		OriginalLeaseID: "lease-123",
		EventTime:      time.Now(),
		Permissions:    []string{"read"},
	}

	result := handler.HandleTakeover(context.Background(), req)
	if result.EvidenceEntry == nil {
		t.Fatal("expected evidence entry")
	}

	if result.EvidenceEntry.Type != "takeover_event" {
		t.Errorf("expected type 'takeover_event', got: %s", result.EvidenceEntry.Type)
	}

	if result.EvidenceEntry.Result != "pass" {
		t.Errorf("expected result 'pass', got: %s", result.EvidenceEntry.Result)
	}

	details := result.EvidenceEntry.Details
	if details == nil {
		t.Fatal("expected details")
	}

	if details["event_type"] != "human_takeover" {
		t.Errorf("expected event_type 'human_takeover', got: %v", details["event_type"])
	}
}

func TestTakeoverContinuity_Validation(t *testing.T) {
	continuity := &TakeoverContinuity{
		OriginalWorker:     "worker-1",
		ContinuityWorker:   "worker-2",
		ContinuityID:       "cont-123",
		PreviousAttempts:   []string{"attempt-1"},
		AdmittedAt:         time.Now(),
		AuthorityPreserved: true,
	}

	// Valid continuity
	err := ValidateContinuity(continuity, "worker-1", "worker-2")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Mismatched worker
	continuity.ContinuityWorker = "worker-3"
	err = ValidateContinuity(continuity, "worker-1", "worker-2")
	if err == nil {
		t.Error("expected error for mismatched worker")
	}
}

func TestIsPermissionsSubset(t *testing.T) {
	tests := []struct {
		name     string
		requested []string
		original  []string
		expected  bool
	}{
		{
			name:     "subset",
			requested: []string{"read"},
			original:  []string{"read", "write"},
			expected:  true,
		},
		{
			name:     "equal",
			requested: []string{"read", "write"},
			original:  []string{"read", "write"},
			expected:  true,
		},
		{
			name:     "superset",
			requested: []string{"read", "write", "execute"},
			original:  []string{"read", "write"},
			expected:  false,
		},
		{
			name:     "empty_requested",
			requested: []string{},
			original:  []string{"read"},
			expected:  true,
		},
		{
			name:     "both_empty",
			requested: []string{},
			original:  []string{},
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPermissionsSubset(tt.requested, tt.original)
			if result != tt.expected {
				t.Errorf("isPermissionsSubset(%v, %v) = %v, want %v",
					tt.requested, tt.original, result, tt.expected)
			}
		})
	}
}
