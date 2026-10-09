package promotion

import (
	"context"
	"fmt"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workspace"
)

// MaterializedCandidate is the mechanically imported immutable Git object
// presented to the canonical-source backend. It deliberately contains no
// credential material and no authority state.
type MaterializedCandidate struct {
	Repository        string
	Ref               string
	SHA               string
	SourceRepository  string
	SourceRef         string
	SourceWorkspaceID string
}

func (m MaterializedCandidate) Validate() error {
	if strings.TrimSpace(m.Repository) == "" ||
		strings.TrimSpace(m.Ref) == "" ||
		strings.TrimSpace(m.SourceRepository) == "" ||
		strings.TrimSpace(m.SourceRef) == "" ||
		strings.TrimSpace(m.SourceWorkspaceID) == "" {
		return fmt.Errorf("%w: incomplete materialized candidate", ErrMaterializationMismatch)
	}
	if !immutableGitSHA.MatchString(m.SHA) {
		return fmt.Errorf("%w: materialized SHA is not immutable", ErrMaterializationMismatch)
	}
	return nil
}

// Materializer owns Git mechanics only. It receives an AuthorizedRequest, so
// evidence + governance decision gates have already passed. It cannot create a
// Proposal and therefore cannot acquire merge/release authority.
type Materializer interface {
	Materialize(context.Context, AuthorizedRequest) (MaterializedCandidate, error)
}

// MaterializingBackend adapts external Git candidates into the existing
// canonical-source Backend while preserving the original promotion identity.
type MaterializingBackend struct {
	inner       Backend
	materializer Materializer
}

func NewMaterializingBackend(inner Backend, materializer Materializer) (*MaterializingBackend, error) {
	if inner == nil || materializer == nil {
		return nil, fmt.Errorf("%w: canonical backend and materializer are required", ErrMalformed)
	}
	return &MaterializingBackend{inner: inner, materializer: materializer}, nil
}

func (b *MaterializingBackend) ID() string { return b.inner.ID() }

func (b *MaterializingBackend) Get(ctx context.Context, proposal Proposal) (Proposal, error) {
	return b.inner.Get(ctx, proposal)
}

func (b *MaterializingBackend) Propose(ctx context.Context, auth AuthorizedRequest) (Proposal, error) {
	req := auth.Request()
	if req.Candidate.Repository == req.Target.Repository {
		return b.inner.Propose(ctx, auth)
	}

	m, err := b.materializer.Materialize(ctx, auth)
	if err != nil {
		return Proposal{}, fmt.Errorf("%w: %v", ErrMaterializationFailed, err)
	}
	if err := m.Validate(); err != nil {
		return Proposal{}, err
	}
	if m.Repository != req.Target.Repository ||
		m.SHA != req.Candidate.SHA ||
		m.SourceRepository != req.Candidate.Repository ||
		m.SourceRef != req.Candidate.Ref ||
		m.SourceWorkspaceID != req.Workspace.ID {
		return Proposal{}, ErrMaterializationMismatch
	}

	next := req
	next.Candidate = workspace.Candidate{
		WorkspaceID: req.Candidate.WorkspaceID,
		Repository:  m.Repository,
		Ref:         m.Ref,
		SHA:         m.SHA,
		ProducedAt:  req.Candidate.ProducedAt,
	}

	// Preserve keyHash/fingerprint from the original external request. The
	// mechanical import must never change the authority/idempotency identity.
	return b.inner.Propose(ctx, AuthorizedRequest{
		request: next,
		keyHash: auth.keyHash,
		fp:      auth.fp,
	})
}

var _ Backend = (*MaterializingBackend)(nil)
