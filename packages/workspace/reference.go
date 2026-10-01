package workspace

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

// ReferenceProvider is an in-memory conformance implementation. It exercises
// lifecycle and boundary laws without coupling tests to GitHub, Cloudflare,
// local Git, or any other repository backend.
type ReferenceProvider struct {
	mu       sync.Mutex
	id       string
	seq      uint64
	byID     map[string]Workspace
	byIdem   map[string]string
}

func NewReferenceProvider(id string) *ReferenceProvider {
	return &ReferenceProvider{
		id: id,
		byID: make(map[string]Workspace),
		byIdem: make(map[string]string),
	}
}

func (p *ReferenceProvider) ID() string { return p.id }

func (p *ReferenceProvider) Create(_ context.Context, spec Spec) (Workspace, error) {
	if err := spec.Validate(); err != nil {
		return Workspace{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if id, ok := p.byIdem[spec.IdempotencyKey]; ok {
		return p.byID[id], nil
	}

	p.seq++
	id := fmt.Sprintf("wsp_ref_%08d", p.seq)
	ref, _ := secrets.ParseRef(fmt.Sprintf("secret://workspace/%s", id))
	now := time.Now().UTC()
	ws := Workspace{
		ID: id,
		ProviderID: p.id,
		Org: spec.Org,
		WorkID: spec.WorkID,
		Name: spec.Name,
		RemoteURL: "https://workspace.invalid/" + id + ".git",
		DefaultBranch: "main",
		Baseline: spec.Baseline,
		Mode: spec.Mode,
		CredentialRef: ref,
		CreatedAt: now,
	}
	if spec.TTL > 0 {
		ws.ExpiresAt = now.Add(spec.TTL)
	}
	p.byID[id] = ws
	p.byIdem[spec.IdempotencyKey] = id
	return ws, nil
}

func (p *ReferenceProvider) Get(_ context.Context, id string) (Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ws, ok := p.byID[id]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	return ws, nil
}

func (p *ReferenceProvider) Candidate(_ context.Context, ws Workspace) (Candidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	got, ok := p.byID[ws.ID]
	if !ok {
		return Candidate{}, ErrNotFound
	}
	if got.Org != ws.Org || got.WorkID != ws.WorkID {
		return Candidate{}, ErrForeignWorkspace
	}
	return Candidate{
		WorkspaceID: got.ID,
		Repository: got.Name,
		Ref: "refs/heads/" + got.DefaultBranch,
		SHA: "reference-unresolved",
		ProducedAt: time.Now().UTC(),
	}, nil
}

func (p *ReferenceProvider) RevokeCredential(_ context.Context, ws Workspace) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	got, ok := p.byID[ws.ID]
	if !ok {
		return ErrNotFound
	}
	if got.Org != ws.Org || got.WorkID != ws.WorkID {
		return ErrForeignWorkspace
	}
	got.CredentialRef = nil
	p.byID[ws.ID] = got
	return nil
}

func (p *ReferenceProvider) Destroy(_ context.Context, ws Workspace) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	got, ok := p.byID[ws.ID]
	if !ok {
		return ErrNotFound
	}
	if got.Org != ws.Org || got.WorkID != ws.WorkID {
		return ErrForeignWorkspace
	}
	delete(p.byID, ws.ID)
	return nil
}
