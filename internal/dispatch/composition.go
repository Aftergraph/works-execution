package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrAuthorityRevalidationFailed = errors.New("dispatch: external authority revalidation failed")
	ErrAuthorityBindingMismatch     = errors.New("dispatch: authority binding mismatch")
	ErrAuthorityEvidenceMissing     = errors.New("dispatch: authority revalidation evidence reference required")
)

var authorityBindingDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DispatchAuthorityRequest is the composition request that sits in front of the
// frozen dispatch.acceptance/1.0 record. ActionID is the exact AIE-admitted
// action identity. BindingDigest is the canonical TG/AIE admission binding
// digest. Neither field is minted or inferred by WORKS.
type DispatchAuthorityRequest struct {
	Dispatch      Dispatch
	ActionID      string
	BindingDigest string
}

// AuthorityProof is returned by the externally-owned authority plane.
// EvidenceRef must identify the concrete revalidation evidence; a bare boolean
// is insufficient for a durable consequential acceptance.
type AuthorityProof struct {
	EvidenceRef    string
	DispatchDigest string
}

type AuthorityRevalidationRequest struct {
	ActionID       string
	BindingDigest  string
	Dispatch       Dispatch
	DispatchDigest string
}

// AuthorityRevalidator is implemented by the TG/AIE authority adapter.
// WORKS depends on this narrow port and never owns lease/revocation semantics.
type AuthorityRevalidator interface {
	Revalidate(ctx context.Context, req AuthorityRevalidationRequest) (AuthorityProof, error)
}

// AuthorityBinding is persisted atomically with the winning acceptance while
// remaining outside dispatch.acceptance/1.0's frozen JSON shape.
type AuthorityBinding struct {
	ActionID       string
	BindingDigest  string
	DispatchDigest string
	EvidenceRef    string
}

// GovernedStore extends the legacy acceptance store with the atomic binding
// operation required by production composition.
type GovernedStore interface {
	Store
	LoadAuthorityBinding(idempotencyKey string) (*AuthorityBinding, error)
	AcceptRevalidatedIfAbsent(candidate *Acceptance, binding AuthorityBinding) (*Acceptance, *AuthorityBinding, error)
}

// AcceptanceService is the production-safe Runtime -> WORKS composition seam.
// Runtime supplies identity; TG/AIE supplies current authority; WORKS only
// persists the independently revalidated result.
type AcceptanceService struct {
	acceptor    *Acceptor
	store       GovernedStore
	revalidator AuthorityRevalidator
}

func NewAcceptanceService(store GovernedStore, revalidator AuthorityRevalidator, clock Clock) (*AcceptanceService, error) {
	if store == nil {
		return nil, errors.New("dispatch: governed store is required")
	}
	if revalidator == nil {
		return nil, errors.New("dispatch: authority revalidator is required")
	}
	return &AcceptanceService{
		acceptor:    NewAcceptor(store, clock),
		store:       store,
		revalidator: revalidator,
	}, nil
}

func validateAuthorityRequest(req DispatchAuthorityRequest) error {
	if strings.TrimSpace(req.ActionID) == "" {
		return fmt.Errorf("%w: missing action_id", ErrAuthorityBindingMismatch)
	}
	if !authorityBindingDigestPattern.MatchString(req.BindingDigest) {
		return fmt.Errorf("%w: binding digest must be lowercase sha256", ErrAuthorityBindingMismatch)
	}
	if req.Dispatch.MissionID == "" || req.Dispatch.AuthorityRef == "" ||
		req.Dispatch.RuntimeDispatchID == "" || req.Dispatch.IdempotencyKey == "" {
		return ErrMissingBinding
	}
	return nil
}

func dispatchDigest(d Dispatch) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("dispatch: encode envelope digest: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func bindingMatches(got *AuthorityBinding, req DispatchAuthorityRequest, digest string) bool {
	return got != nil &&
		got.ActionID == req.ActionID &&
		got.BindingDigest == req.BindingDigest &&
		got.DispatchDigest == digest &&
		strings.TrimSpace(got.EvidenceRef) != ""
}

func (s *AcceptanceService) loadDurableWinner(req DispatchAuthorityRequest, digest string) (*Acceptance, bool, error) {
	existing, err := s.store.LoadByIdempotency(req.Dispatch.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		return nil, false, nil
	}
	binding, err := s.store.LoadAuthorityBinding(req.Dispatch.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	if !bindingMatches(binding, req, digest) {
		return nil, false, fmt.Errorf("%w: key %q", ErrAuthorityBindingMismatch, req.Dispatch.IdempotencyKey)
	}
	if err := validateAcceptedIdentity(existing, req.Dispatch); err != nil {
		return nil, false, err
	}
	return existing, true, nil
}

// Accept revalidates only before the first durable acceptance. A retry of an
// already accepted identical request returns the same acceptance and binding;
// it never asks Runtime to manufacture freshness and never authorizes a
// different action under the same idempotency key.
func (s *AcceptanceService) Accept(ctx context.Context, req DispatchAuthorityRequest) (*Acceptance, error) {
	if err := validateAuthorityRequest(req); err != nil {
		return nil, err
	}
	digest, err := dispatchDigest(req.Dispatch)
	if err != nil {
		return nil, err
	}
	if existing, ok, err := s.loadDurableWinner(req, digest); err != nil || ok {
		return existing, err
	}

	proof, err := s.revalidator.Revalidate(ctx, AuthorityRevalidationRequest{
		ActionID:       req.ActionID,
		BindingDigest:  req.BindingDigest,
		Dispatch:       req.Dispatch,
		DispatchDigest: digest,
	})
	if err != nil {
		// Resolve the load-before-revalidate race: another identical caller may
		// have committed the durable winner while this authority call failed.
		if existing, ok, reloadErr := s.loadDurableWinner(req, digest); reloadErr != nil {
			return nil, reloadErr
		} else if ok {
			return existing, nil
		}
		return nil, fmt.Errorf("%w: %v", ErrAuthorityRevalidationFailed, err)
	}
	if strings.TrimSpace(proof.EvidenceRef) == "" {
		return nil, ErrAuthorityEvidenceMissing
	}
	if proof.DispatchDigest != digest {
		return nil, fmt.Errorf("%w: authority proof did not bind accepted dispatch", ErrAuthorityBindingMismatch)
	}

	candidate, err := s.acceptor.buildAcceptance(req.Dispatch)
	if err != nil {
		return nil, err
	}
	binding := AuthorityBinding{
		ActionID:       req.ActionID,
		BindingDigest:  req.BindingDigest,
		DispatchDigest: digest,
		EvidenceRef:    proof.EvidenceRef,
	}
	accepted, persistedBinding, err := s.store.AcceptRevalidatedIfAbsent(candidate, binding)
	if err != nil {
		return nil, err
	}
	if !bindingMatches(persistedBinding, req, digest) {
		return nil, fmt.Errorf("%w: key %q", ErrAuthorityBindingMismatch, req.Dispatch.IdempotencyKey)
	}
	if err := validateAcceptedIdentity(accepted, req.Dispatch); err != nil {
		return nil, err
	}
	return accepted, nil
}
