package promotionservice

import (
	"context"
	"fmt"

	domain "github.com/JonasAbde/works-execution/packages/promotion"
	"github.com/JonasAbde/works-execution/services/evidence"
)

type BundleLoader interface {
	LoadEvidenceBundle(ctx context.Context, bundleID string) (*evidence.Bundle, error)
}

type EvidenceKeyResolver interface {
	ResolveEvidenceVerificationKey(ctx context.Context, bundleID string) (keyID string, key []byte, err error)
}

type verifyBundleFunc func(*evidence.Bundle, string, []byte) (*evidence.BundleVerificationResult, error)

type EvidenceVerifier struct {
	loader BundleLoader
	keys   EvidenceKeyResolver
	verify verifyBundleFunc
}

func NewEvidenceVerifier(loader BundleLoader, keys EvidenceKeyResolver) (*EvidenceVerifier, error) {
	if loader == nil || keys == nil {
		return nil, fmt.Errorf("%w: evidence loader and key resolver are required", domain.ErrMalformed)
	}
	return &EvidenceVerifier{loader: loader, keys: keys, verify: evidence.VerifyBundle}, nil
}

func newEvidenceVerifierWithVerify(loader BundleLoader, keys EvidenceKeyResolver, verify verifyBundleFunc) *EvidenceVerifier {
	if verify == nil {
		verify = evidence.VerifyBundle
	}
	return &EvidenceVerifier{loader: loader, keys: keys, verify: verify}
}

func (v *EvidenceVerifier) Verify(ctx context.Context, bundleID, workID string) error {
	if v == nil || v.loader == nil || v.keys == nil || v.verify == nil {
		return fmt.Errorf("%w: verifier unavailable", domain.ErrEvidenceNotVerified)
	}
	bundle, err := v.loader.LoadEvidenceBundle(ctx, bundleID)
	if err != nil || bundle == nil {
		return fmt.Errorf("%w: bundle unavailable", domain.ErrEvidenceNotVerified)
	}
	if bundle.BundleID != bundleID || bundle.WorkID != workID {
		return fmt.Errorf("%w: bundle identity mismatch", domain.ErrEvidenceNotVerified)
	}
	keyID, key, err := v.keys.ResolveEvidenceVerificationKey(ctx, bundleID)
	if err != nil || keyID == "" || len(key) == 0 {
		return fmt.Errorf("%w: verification key unavailable", domain.ErrEvidenceNotVerified)
	}
	result, err := v.verify(bundle, keyID, key)
	if err != nil || result == nil || !result.Valid {
		return fmt.Errorf("%w: bundle verification failed", domain.ErrEvidenceNotVerified)
	}
	return nil
}
