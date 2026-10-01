package workspace

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

var immutableGitSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ReferenceProvider is an in-memory conformance implementation. It exercises
// lifecycle and boundary laws without coupling tests to GitHub, Cloudflare,
// local Git, or any other repository backend.
type ReferenceProvider struct {
	mu         sync.Mutex
	id         string
	seq        uint64
	byID       map[string]Workspace
	byIdem     map[string]string
	byIdemSpec map[string]string
}

func NewReferenceProvider(id string) *ReferenceProvider {
	return &ReferenceProvider{
		id:         id,
		byID:       make(map[string]Workspace),
		byIdem:     make(map[string]string),
		byIdemSpec: make(map[string]string),
	}
}

func (p *ReferenceProvider) ID() string { return p.id }

func (p *ReferenceProvider) Create(_ context.Context, spec Spec) (Workspace, error) {
	if err := spec.Validate(); err != nil {
		return Workspace{}, err
	}
	_, specHash, err := workspaceIdentity(spec)
	if err != nil {
		return Workspace{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if id, ok := p.byIdem[spec.IdempotencyKey]; ok {
		if p.byIdemSpec[spec.IdempotencyKey] != specHash {
			return Workspace{}, ErrIdempotencyConflict
		}
		return p.byID[id], nil
	}

	p.seq++
	id := fmt.Sprintf("wsp_ref_%08d", p.seq)
	ref, _ := secrets.ParseRef(fmt.Sprintf("secret://workspace/%s", id))
	now := time.Now().UTC()
	ws := Workspace{
		ID:            id,
		ProviderID:    p.id,
		Org:           spec.Org,
		WorkID:        spec.WorkID,
		Name:          spec.Name,
		RemoteURL:     "https://workspace.invalid/" + id + ".git",
		DefaultBranch: "main",
		Baseline:      spec.Baseline,
		Mode:          spec.Mode,
		CredentialRef: ref,
		CreatedAt:     now,
	}
	if spec.TTL > 0 {
		ws.ExpiresAt = now.Add(spec.TTL)
	}
	p.byID[id] = ws
	p.byIdem[spec.IdempotencyKey] = id
	p.byIdemSpec[spec.IdempotencyKey] = specHash
	return ws, nil
}

func (p *ReferenceProvider) Get(_ context.Context, handle Workspace) (Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ws, ok := p.byID[handle.ID]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	if handle.Org != "" && ws.Org != handle.Org {
		return Workspace{}, ErrForeignWorkspace
	}
	if handle.WorkID != "" && ws.WorkID != handle.WorkID {
		return Workspace{}, ErrForeignWorkspace
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
	sha := got.Baseline.SHA
	if !immutableGitSHA.MatchString(sha) {
		sum := sha1.Sum([]byte(got.Baseline.Provider + "|" + got.Baseline.Repository + "|" + got.Baseline.Ref))
		sha = hex.EncodeToString(sum[:])
	}
	return Candidate{
		WorkspaceID: got.ID,
		Repository:  got.Name,
		Ref:         "refs/heads/" + got.DefaultBranch,
		SHA:         sha,
		ProducedAt:  time.Now().UTC(),
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
	// Revocation invalidates credential material in a real CredentialStore.
	// The inert secret:// ref remains part of the durable workspace handle.
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
