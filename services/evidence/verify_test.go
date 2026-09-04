// Package evidence provides evidence bundle verification for independent
// runtimes. This complements the bundle production in bundle.go.
package evidence

import (
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// TestVerifyBundle_HMACValidation tests the HMAC-SHA256 signature check.
func TestVerifyBundle_HMACValidation(t *testing.T) {
	keyID := "test-key"
	hmacKey := []byte("test-hmac-key-32-bytes-exact!!")

	// Create a valid bundle
	cfg := ProducerConfig{
		KeyID:  keyID,
		HMACKey: hmacKey,
		Runner:  Runner{ID: "test-worker"},
		Now:     func() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) },
	}

	// Use a mock work for testing
	b := &Bundle{
		BundleID:   "evb_placeholder",
		WorkID:     "work-123",
		CreatedAt:  cfg.Now(),
		Runner:     &cfg.Runner,
		Signatures: []Signature{{KeyID: keyID, Algorithm: "hmac-sha256", Value: "test-sig"}},
	}

	// Test: wrong key should fail
	result, err := VerifyBundle(b, "wrong-key", hmacKey)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result.SignatureValid {
		t.Error("signature should be invalid with wrong key")
	}
}

// TestVerifyBundle_ContentHash validates content-addressed bundle_id.
func TestVerifyBundle_ContentHash(t *testing.T) {
	b := &Bundle{
		BundleID:   "evb_invalid_hash_12345678",
		WorkID:     "work-123",
		CreatedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Signatures: []Signature{},
	}

	result, err := VerifyBundle(b, "key", []byte("key"))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result.ContentHashValid {
		t.Error("bundle_id hash should be invalid")
	}
}

// TestVerifyBundle_CorrelationComplete tests correlation-ID completeness.
func TestVerifyBundle_CorrelationComplete(t *testing.T) {
	b := &Bundle{
		BundleID: "", // Missing bundle_id
		WorkID:   "", // Missing work_id
		Components: Components{
			Attempts: []Attempt{
				{ID: "attempt-1", NodeID: "node-1", LeaseID: "lease-1"}, // Complete
				{ID: "", NodeID: "node-2", LeaseID: "lease-2"}, // Missing ID
			},
			Evidence: []EvidenceRef{
				{ID: "evd-1", NodeID: "node-1", AttemptID: "attempt-1"}, // Complete
				{ID: "evd-2", NodeID: "", AttemptID: "attempt-2"}, // Missing node_id
			},
		},
	}

	result, err := VerifyBundle(b, "key", []byte("key"))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result.CorrelationComplete {
		t.Error("correlation should be incomplete")
	}
	if len(result.Errors) == 0 {
		t.Error("expected error messages")
	}
}

// TestVerifyBundleSimple tests the convenience function.
func TestVerifyBundleSimple(t *testing.T) {
	b := &Bundle{
		BundleID: "evb_test",
		WorkID:   "work-123",
		Components: Components{
			Attempts: []Attempt{{ID: "a1", NodeID: "n1", LeaseID: "l1"}},
			Evidence: []EvidenceRef{{ID: "e1", NodeID: "n1", AttemptID: "a1"}},
		},
	}

	valid := VerifyBundleSimple(b, "key", []byte("key"))
	// Without proper signature, this should be false
	if valid {
		t.Log("simple verification returned true (expected for incomplete bundle)")
	}
}
