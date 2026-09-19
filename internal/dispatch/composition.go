package dispatch

import (
	"context"
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
	EvidenceRef string
}

// AuthorityRevalidator is implemented by the TG/AIE authority adapter.
// WORKS depends on this narrow port and never owns lease/revocation semantics.
type AuthorityRevalidator interface {
	Revalidate(ctx context.Context, actionID, bindingDigest string) (AuthorityProof, error)
}

// AuthorityBinding is persisted atomically with the winning acceptance while
// remaining outside dispatch.acceptance/1.0's frozen JSON shape.
type AuthorityBinding struct {
	ActionID      string
	BindingDigest string
	EvidenceRef   string
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

func bindingMatches(got *AuthorityBinding, req DispatchAuthorityRequest) bool {
	return got != nil &&
		got.ActionID == req.ActionID &&
		got.BindingDigest == req.BindingDigest &&
		strings.TrimSpace(got.EvidenceRef) != ""
}

// Accept revalidates only before the first durable acceptance. A retry of an
// already accepted identical request returns the same acceptance and binding;
// it never asks Runtime to manufacture freshness and never authorizes a
// different action under the same idempotency key.
func (s *AcceptanceService) Accept(ctx context.Context, req DispatchAuthorityRequest) (*Acceptance, error) {
	if err := validateAuthorityRequest(req); err != nil {
		return nil, err
	}

	existing, err := s.store.LoadByIdempotency(req.Dispatch.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		binding, err := s.store.LoadAuthorityBinding(req.Dispatch.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if !bindingMatches(binding, req) {
			return nil, fmt.Errorf("%w: key %q", ErrAuthorityBindingMismatch, req.Dispatch.IdempotencyKey)
		}
		if err := validateAcceptedIdentity(existing, req.Dispatch); err != nil {
			return nil, err
		}
		return existing, nil
	}

	proof, err := s.revalidator.Revalidate(ctx, req.ActionID, req.BindingDigest)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthorityRevalidationFailed, err)
	}
	if strings.TrimSpace(proof.EvidenceRef) == "" {
		return nil, ErrAuthorityEvidenceMissing
	}

	candidate, err := s.acceptor.buildAcceptance(req.Dispatch)
	if err != nil {
		return nil, err
	}
	binding := AuthorityBinding{
		ActionID:      req.ActionID,
		BindingDigest: req.BindingDigest,
		EvidenceRef:   proof.EvidenceRef,
	}
	accepted, persistedBinding, err := s.store.AcceptRevalidatedIfAbsent(candidate, binding)
	if err != nil {
		return nil, err
	}
	if !bindingMatches(persistedBinding, req) {
		return nil, fmt.Errorf("%w: key %q", ErrAuthorityBindingMismatch, req.Dispatch.IdempotencyKey)
	}
	if err := validateAcceptedIdentity(accepted, req.Dispatch); err != nil {
		return nil, err
	}
	return accepted, nil
}
