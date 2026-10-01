// Package workspace defines the provider-neutral source workspace boundary used
// by WORKS to isolate concurrent agent work without making any Git host the
// execution authority.
//
// A workspace is an execution-scoped fork of a baseline source revision.
// Providers own repository mechanics; WORKS owns intent, authority, lifecycle,
// evidence, and promotion.
//
// Credentials NEVER appear as raw values in this package. Only secret:// refs
// cross the boundary; resolution remains a kernel/worker concern (ADR-0022).
package workspace

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

const ABI = "workspace/1.0"

// CredentialStore is the shared handoff boundary for provider-issued Git
// credentials. Plaintext may exist only inside Put/Resolve call scope; durable
// WORKS state carries only secret:// refs and non-secret correlation ids.
type CredentialStore interface {
	Put(ctx context.Context, workID, provider, name, value string, expiresAt time.Time) (*secrets.Ref, error)
	Resolve(ctx context.Context, ref *secrets.Ref, workID string) (string, error)
	Delete(ctx context.Context, ref *secrets.Ref) error
}


type Mode string

const (
	ModeRead  Mode = "read"
	ModeWrite Mode = "write"
)

var (
	ErrMalformed             = errors.New("workspace: malformed request")
	ErrProviderUnavailable   = errors.New("workspace: provider unavailable")
	ErrNotFound              = errors.New("workspace: not found")
	ErrForeignWorkspace      = errors.New("workspace: foreign workspace")
	ErrIdempotencyConflict    = errors.New("workspace: idempotency key conflicts with original creation intent")
	ErrCredentialRefRequired = errors.New("workspace: credential must be a secret ref")
	ErrPromotionUnsupported  = errors.New("workspace: provider does not implement promotion")
)

// SourceRef identifies the immutable or branch-addressed baseline from which an
// execution workspace is derived. Provider is descriptive routing metadata, not
// an authority decision.
type SourceRef struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Ref        string `json:"ref,omitempty"`
	SHA        string `json:"sha,omitempty"`
	RemoteURL  string `json:"remote_url,omitempty"`
}

func (s SourceRef) Validate() error {
	if strings.TrimSpace(s.Provider) == "" || strings.TrimSpace(s.Repository) == "" {
		return fmt.Errorf("%w: provider and repository are required", ErrMalformed)
	}
	if strings.TrimSpace(s.Ref) == "" && strings.TrimSpace(s.SHA) == "" {
		return fmt.Errorf("%w: ref or sha is required", ErrMalformed)
	}
	return nil
}

// Spec is a request to create one isolated unit of agent work. WorkID and Org
// are mandatory provenance/tenant bindings and MUST be preserved by providers.
type Spec struct {
	IdempotencyKey string            `json:"idempotency_key"`
	WorkID         string            `json:"work_id"`
	Org            string            `json:"org"`
	Name           string            `json:"name"`
	Baseline       SourceRef         `json:"baseline"`
	Mode           Mode              `json:"mode"`
	Labels         map[string]string `json:"labels,omitempty"`
	TTL            time.Duration     `json:"ttl,omitempty"`
}

func (s Spec) Validate() error {
	if strings.TrimSpace(s.IdempotencyKey) == "" || strings.TrimSpace(s.WorkID) == "" ||
		strings.TrimSpace(s.Org) == "" || strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%w: idempotency_key, work_id, org and name are required", ErrMalformed)
	}
	if s.Mode != ModeRead && s.Mode != ModeWrite {
		return fmt.Errorf("%w: unsupported mode %q", ErrMalformed, s.Mode)
	}
	return s.Baseline.Validate()
}

// Workspace is the durable provider-neutral handle WORKS may persist.
// CredentialRef is safe inert data. Raw provider tokens MUST NOT be stored here.
type Workspace struct {
	ID            string      `json:"id"`
	ProviderID    string      `json:"provider_id"`
	Org           string      `json:"org"`
	WorkID        string      `json:"work_id"`
	Name          string      `json:"name"`
	RemoteURL     string      `json:"remote_url"`
	DefaultBranch string      `json:"default_branch,omitempty"`
	Baseline      SourceRef   `json:"baseline"`
	Mode          Mode        `json:"mode"`
	CredentialRef *secrets.Ref `json:"credential_ref"`
	CredentialID  string       `json:"credential_id,omitempty"` // provider token id; non-secret, used for deterministic revocation
	CreatedAt     time.Time   `json:"created_at"`
	ExpiresAt     time.Time   `json:"expires_at,omitempty"`
}

func (w Workspace) Validate() error {
	if strings.TrimSpace(w.ID) == "" || strings.TrimSpace(w.ProviderID) == "" ||
		strings.TrimSpace(w.Org) == "" || strings.TrimSpace(w.WorkID) == "" ||
		strings.TrimSpace(w.Name) == "" || strings.TrimSpace(w.RemoteURL) == "" {
		return fmt.Errorf("%w: incomplete workspace handle", ErrMalformed)
	}
	if w.Mode != ModeRead && w.Mode != ModeWrite {
		return fmt.Errorf("%w: unsupported mode %q", ErrMalformed, w.Mode)
	}
	if err := w.Baseline.Validate(); err != nil {
		return err
	}
	if w.CredentialRef == nil {
		return ErrCredentialRefRequired
	}
	if _, err := secrets.ParseRef(w.CredentialRef.String()); err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialRefRequired, err)
	}
	return nil
}


// MarshalJSON pins workspace/1.0 credential_ref to the inert secret:// string
// form instead of leaking the Go implementation shape of secrets.Ref.
func (w Workspace) MarshalJSON() ([]byte, error) {
	type alias Workspace
	return json.Marshal(&struct {
		CredentialRef string `json:"credential_ref"`
		*alias
	}{
		CredentialRef: w.CredentialRef.String(),
		alias:         (*alias)(&w),
	})
}

// UnmarshalJSON reconstructs the typed secret ref while keeping the wire form
// provider-neutral and value-free.
func (w *Workspace) UnmarshalJSON(data []byte) error {
	type alias Workspace
	aux := &struct {
		CredentialRef string `json:"credential_ref"`
		*alias
	}{alias: (*alias)(w)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if aux.CredentialRef == "" {
		w.CredentialRef = nil
		return nil
	}
	ref, err := secrets.ParseRef(aux.CredentialRef)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialRefRequired, err)
	}
	w.CredentialRef = ref
	return nil
}

// Candidate is the immutable result handed from execution to evaluation. The
// provider only reports source coordinates; WORKS decides whether anything is
// promoted to canonical source.
type Candidate struct {
	WorkspaceID string    `json:"workspace_id"`
	Repository  string    `json:"repository"`
	Ref         string    `json:"ref"`
	SHA         string    `json:"sha"`
	ProducedAt  time.Time `json:"produced_at"`
}

// Provider is the source-workspace boundary. It deliberately excludes merge,
// review, scoring, and policy decisions: those remain WORKS/eval/governance
// responsibilities.
type Provider interface {
	ID() string
	Create(ctx context.Context, spec Spec) (Workspace, error)
	Get(ctx context.Context, handle Workspace) (Workspace, error)
	Candidate(ctx context.Context, ws Workspace) (Candidate, error)
	RevokeCredential(ctx context.Context, ws Workspace) error
	Destroy(ctx context.Context, ws Workspace) error
}
