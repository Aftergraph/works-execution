// Package evidence provides evidence bundle verification for independent
// runtimes. This complements the bundle production in bundle.go.
package evidence

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// BundleVerificationResult holds the outcome of VerifyBundle.
type BundleVerificationResult struct {
	Valid            bool
	SignatureValid   bool
	ContentHashValid bool
	CorrelationComplete bool
	Errors           []string
}

// ErrBundleVerification combines multiple verification errors.
var ErrBundleVerification = errors.New("bundle verification failed")

// VerifyBundle validates an evidence bundle for an independent runtime.
// It checks:
//   1. HMAC-SHA256 signature validity
//   2. Content-addressed bundle_id hash
//   3. Correlation-ID completeness (work_id/node_id/attempt_id/lease_id/
//      evidence_id/bundle_id)
func VerifyBundle(b *Bundle, keyID string, hmacKey []byte) (*BundleVerificationResult, error) {
	result := &BundleVerificationResult{
		Valid:           false,
		SignatureValid:  false,
		ContentHashValid: false,
		CorrelationComplete: false,
		Errors:          []string{},
	}

	if b == nil {
		result.Errors = append(result.Errors, "bundle is nil")
		return result, nil
	}

	// Check signature validity
	if err := verifySignature(b, keyID, hmacKey); err != nil {
		result.Errors = append(result.Errors, "signature invalid: "+err.Error())
	} else {
		result.SignatureValid = true
	}

	// Check content-addressed bundle_id
	if err := verifyBundleID(b); err != nil {
		result.Errors = append(result.Errors, "bundle_id invalid: "+err.Error())
	} else {
		result.ContentHashValid = true
	}

	// Check correlation-ID completeness
	if err := verifyCorrelationIDs(b); err != nil {
		result.Errors = append(result.Errors, "correlation incomplete: "+err.Error())
	} else {
		result.CorrelationComplete = true
	}

	// Overall validity: all checks must pass
	result.Valid = result.SignatureValid && result.ContentHashValid && result.CorrelationComplete

	return result, nil
}

// verifySignature checks the HMAC-SHA256 signature.
func verifySignature(b *Bundle, keyID string, hmacKey []byte) error {
	if len(b.Signatures) == 0 {
		return errors.New("no signatures")
	}

	for _, s := range b.Signatures {
		if s.KeyID != keyID {
			continue
		}

		// Re-canonicalize with bundle_id replaced by placeholder and signatures stripped
		clone := *b
		clone.BundleID = placeholderBundleID
		clone.Signatures = nil

		canonical, err := canonicalize(&clone)
		if err != nil {
			return err
		}

		// Decode stored signature
		storedSig, err := base64.StdEncoding.DecodeString(s.Value)
		if err != nil {
			return err
		}

		// Compute expected signature
		expectedSig := hmacSum(canonical, hmacKey)

		// Compare in constant time
		if !hmac.Equal(storedSig, expectedSig) {
			return errors.New("HMAC mismatch")
		}

		return nil // Found and verified matching signature
	}

	return errors.New("no matching signature for key_id")
}

// verifyBundleID checks the content-addressed bundle_id.
func verifyBundleID(b *Bundle) error {
	if b.BundleID == "" {
		return errors.New("bundle_id is empty")
	}

	// Compute expected bundle_id using the same method as Produce
	preCanonical := *b
	preCanonical.BundleID = placeholderBundleID
	preCanonical.Signatures = nil

	canonical, err := canonicalize(&preCanonical)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(canonical)
	expectedBundleID := "evb_" + hex.EncodeToString(sum[:])[:32]

	if b.BundleID != expectedBundleID {
		return errors.New("bundle_id hash mismatch")
	}

	return nil
}

// verifyCorrelationIDs checks that all required correlation fields are present.
func verifyCorrelationIDs(b *Bundle) error {
	var missing []string

	// Bundle-level required fields
	if b.BundleID == "" {
		missing = append(missing, "bundle_id")
	}
	if b.WorkID == "" {
		missing = append(missing, "work_id")
	}

	// Check components for correlation completeness
	for i, attempt := range b.Components.Attempts {
		if attempt.ID == "" {
			missing = append(missing, fmt.Sprintf("attempts[%d].id", i))
		}
		if attempt.NodeID == "" {
			missing = append(missing, fmt.Sprintf("attempts[%d].node_id", i))
		}
		if attempt.LeaseID == "" {
			missing = append(missing, fmt.Sprintf("attempts[%d].lease_id", i))
		}
	}

	for i, evidence := range b.Components.Evidence {
		if evidence.ID == "" {
			missing = append(missing, fmt.Sprintf("evidence[%d].id", i))
		}
		if evidence.NodeID == "" {
			missing = append(missing, fmt.Sprintf("evidence[%d].node_id", i))
		}
		if evidence.AttemptID == "" {
			missing = append(missing, fmt.Sprintf("evidence[%d].attempt_id", i))
		}
	}

	if len(missing) > 0 {
		return errors.New("missing fields: " + strings.Join(missing, ", "))
	}

	return nil
}

// VerifyBundleSimple is a convenience function that returns true/false only.
func VerifyBundleSimple(b *Bundle, keyID string, hmacKey []byte) bool {
	result, err := VerifyBundle(b, keyID, hmacKey)
	if err != nil || result == nil {
		return false
	}
	return result.Valid
}
