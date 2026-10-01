package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

const GitHubWorkspaceProviderID = "github"

// IssuedCredential is a short-lived Git credential minted outside the provider.
// Plaintext must cross only into CredentialStore.Put and must never be persisted
// on a Workspace handle.
type IssuedCredential struct {
	ID        string
	Plaintext string
	ExpiresAt time.Time
}

// GitCredentialIssuer mints and revokes short-lived repo-scoped credentials.
// A GitHub App installation-token implementation is the intended production
// issuer; WORKS depends only on this boundary, never on app private keys.
type GitCredentialIssuer interface {
	Issue(ctx context.Context, repository, workID string, mode Mode, ttl time.Duration) (IssuedCredential, error)
	Revoke(ctx context.Context, id, plaintext string) error
}

type GitHubWorkspaceConfig struct {
	ControlTokenRef *secrets.Ref
	ControlScope    string
	TokenTTL        time.Duration
	APIBase         string
	WebBase         string
	BranchPrefix    string
}

type GitHubWorkspaceProvider struct {
	cfg         GitHubWorkspaceConfig
	resolver    secrets.Resolver
	issuer      GitCredentialIssuer
	credentials CredentialStore
	client      *http.Client
}

func NewGitHubWorkspaceProvider(cfg GitHubWorkspaceConfig, resolver secrets.Resolver, issuer GitCredentialIssuer, credentials CredentialStore, client *http.Client) (*GitHubWorkspaceProvider, error) {
	if cfg.ControlTokenRef == nil || resolver == nil || issuer == nil || credentials == nil {
		return nil, fmt.Errorf("%w: github control token ref, resolver, issuer and credential store are required", ErrMalformed)
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = time.Hour
	}
	if cfg.TokenTTL < time.Minute || cfg.TokenTTL > 24*time.Hour {
		return nil, fmt.Errorf("%w: github workspace token ttl must be between 1 minute and 24 hours", ErrMalformed)
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	if cfg.WebBase == "" {
		cfg.WebBase = "https://github.com"
	}
	if cfg.BranchPrefix == "" {
		cfg.BranchPrefix = "works"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &GitHubWorkspaceProvider{cfg:cfg,resolver:resolver,issuer:issuer,credentials:credentials,client:client}, nil
}

func (p *GitHubWorkspaceProvider) ID() string { return GitHubWorkspaceProviderID }

type githubRefObject struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

type githubRepo struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
}

func (p *GitHubWorkspaceProvider) Create(ctx context.Context, spec Spec) (Workspace, error) {
	if err := spec.Validate(); err != nil {
		return Workspace{}, err
	}
	if spec.Baseline.Provider != GitHubWorkspaceProviderID {
		return Workspace{}, fmt.Errorf("%w: github provider only accepts github baselines", ErrMalformed)
	}
	repository := strings.TrimSpace(spec.Baseline.Repository)
	if !validGitHubRepository(repository) {
		return Workspace{}, fmt.Errorf("%w: invalid github repository %q", ErrMalformed, repository)
	}
	control, err := p.controlToken(ctx)
	if err != nil {
		return Workspace{}, err
	}

	baselineSHA, err := p.resolveBaselineSHA(ctx, control, spec.Baseline)
	if err != nil {
		return Workspace{}, err
	}
	branch := p.branchName(spec)
	if err := p.ensureBranch(ctx, control, repository, branch, baselineSHA); err != nil {
		return Workspace{}, err
	}

	issued, err := p.issuer.Issue(ctx, repository, spec.WorkID, spec.Mode, p.cfg.TokenTTL)
	if err != nil {
		_ = p.deleteBranch(ctx, control, repository, branch)
		return Workspace{}, fmt.Errorf("workspace: issue github credential: %w", err)
	}
	if issued.ID == "" || issued.Plaintext == "" || issued.ExpiresAt.IsZero() {
		_ = p.deleteBranch(ctx, control, repository, branch)
		return Workspace{}, fmt.Errorf("%w: incomplete github credential", ErrMalformed)
	}
	ref, err := p.credentials.Put(ctx, spec.WorkID, p.ID(), issued.ID, issued.Plaintext, issued.ExpiresAt)
	if err != nil {
		_ = p.issuer.Revoke(ctx, issued.ID, issued.Plaintext)
		_ = p.deleteBranch(ctx, control, repository, branch)
		return Workspace{}, fmt.Errorf("workspace: persist github credential ref: %w", err)
	}

	repoMeta, err := p.getRepo(ctx, control, repository)
	if err != nil {
		_ = p.credentials.Delete(ctx, ref)
		_ = p.issuer.Revoke(ctx, issued.ID, issued.Plaintext)
		_ = p.deleteBranch(ctx, control, repository, branch)
		return Workspace{}, err
	}

	now := time.Now().UTC()
	ws := Workspace{
		ID:            repository + "#" + branch,
		ProviderID:    p.ID(),
		Org:           spec.Org,
		WorkID:        spec.WorkID,
		Name:          repository,
		RemoteURL:     repoMeta.CloneURL,
		DefaultBranch: branch,
		Baseline:      spec.Baseline,
		Mode:          spec.Mode,
		CredentialRef: ref,
		CredentialID:  issued.ID,
		CreatedAt:     now,
		ExpiresAt:     issued.ExpiresAt,
	}
	if err := ws.Validate(); err != nil {
		_ = p.credentials.Delete(ctx, ref)
		_ = p.issuer.Revoke(ctx, issued.ID, issued.Plaintext)
		_ = p.deleteBranch(ctx, control, repository, branch)
		return Workspace{}, err
	}
	return ws, nil
}

func (p *GitHubWorkspaceProvider) Get(ctx context.Context, handle Workspace) (Workspace, error) {
	if err := p.assertOwned(handle); err != nil {
		return Workspace{}, err
	}
	control, err := p.controlToken(ctx)
	if err != nil {
		return Workspace{}, err
	}
	ref, err := p.getRef(ctx, control, handle.Name, handle.DefaultBranch)
	if err != nil {
		return Workspace{}, err
	}
	if ref.Object.SHA == "" {
		return Workspace{}, fmt.Errorf("%w: github ref missing sha", ErrMalformed)
	}
	out := handle
	return out, nil
}

func (p *GitHubWorkspaceProvider) Candidate(ctx context.Context, ws Workspace) (Candidate, error) {
	if err := p.assertOwned(ws); err != nil {
		return Candidate{}, err
	}
	control, err := p.controlToken(ctx)
	if err != nil {
		return Candidate{}, err
	}
	ref, err := p.getRef(ctx, control, ws.Name, ws.DefaultBranch)
	if err != nil {
		return Candidate{}, err
	}
	if ref.Object.SHA == "" {
		return Candidate{}, fmt.Errorf("%w: github ref missing sha", ErrMalformed)
	}
	return Candidate{
		WorkspaceID: ws.ID,
		Repository:  ws.Name,
		Ref:         "refs/heads/" + ws.DefaultBranch,
		SHA:         ref.Object.SHA,
		ProducedAt:  time.Now().UTC(),
	}, nil
}

func (p *GitHubWorkspaceProvider) RevokeCredential(ctx context.Context, ws Workspace) error {
	if err := p.assertOwned(ws); err != nil {
		return err
	}
	if ws.CredentialID == "" {
		return fmt.Errorf("%w: missing github credential id", ErrMalformed)
	}
	if ws.CredentialRef == nil {
		return ErrCredentialRefRequired
	}
	plaintext, err := p.credentials.Resolve(ctx, ws.CredentialRef, ws.WorkID)
	if err != nil {
		return fmt.Errorf("workspace: resolve github credential for revoke: %w", err)
	}
	if err := p.issuer.Revoke(ctx, ws.CredentialID, plaintext); err != nil {
		return fmt.Errorf("workspace: revoke github credential: %w", err)
	}
	if err := p.credentials.Delete(ctx, ws.CredentialRef); err != nil {
		return fmt.Errorf("workspace: delete credential ref: %w", err)
	}
	return nil
}

func (p *GitHubWorkspaceProvider) Destroy(ctx context.Context, ws Workspace) error {
	if err := p.assertOwned(ws); err != nil {
		return err
	}
	control, err := p.controlToken(ctx)
	if err != nil {
		return err
	}
	if ws.CredentialID != "" && ws.CredentialRef != nil {
		if plaintext, resolveErr := p.credentials.Resolve(ctx, ws.CredentialRef, ws.WorkID); resolveErr == nil {
			_ = p.issuer.Revoke(ctx, ws.CredentialID, plaintext)
		}
	}
	if err := p.deleteBranch(ctx, control, ws.Name, ws.DefaultBranch); err != nil {
		return err
	}
	if ws.CredentialRef != nil {
		_ = p.credentials.Delete(ctx, ws.CredentialRef)
	}
	return nil
}

func (p *GitHubWorkspaceProvider) assertOwned(ws Workspace) error {
	if ws.ProviderID != p.ID() {
		return ErrForeignWorkspace
	}
	if ws.ID == "" || ws.Org == "" || ws.WorkID == "" || !validGitHubRepository(ws.Name) || ws.DefaultBranch == "" {
		return fmt.Errorf("%w: incomplete github workspace handle", ErrMalformed)
	}
	return nil
}

func (p *GitHubWorkspaceProvider) resolveBaselineSHA(ctx context.Context, token string, src SourceRef) (string, error) {
	if src.SHA != "" {
		path := "/repos/" + src.Repository + "/git/commits/" + url.PathEscape(src.SHA)
		var out map[string]any
		if err := p.doJSON(ctx, token, http.MethodGet, path, nil, &out); err != nil {
			return "", fmt.Errorf("workspace: verify github baseline sha: %w", err)
		}
		sha, _ := out["sha"].(string)
		if sha == "" || sha != src.SHA {
			return "", fmt.Errorf("%w: github baseline sha mismatch", ErrMalformed)
		}
		return sha, nil
	}
	refName := strings.TrimPrefix(src.Ref, "refs/")
	var out githubRefObject
	if err := p.doJSON(ctx, token, http.MethodGet, "/repos/"+src.Repository+"/git/ref/"+gitRefPath(refName), nil, &out); err != nil {
		return "", err
	}
	if out.Object.SHA == "" {
		return "", fmt.Errorf("%w: github baseline ref missing sha", ErrMalformed)
	}
	return out.Object.SHA, nil
}

func (p *GitHubWorkspaceProvider) ensureBranch(ctx context.Context, token, repository, branch, sha string) error {
	body := map[string]any{"ref":"refs/heads/"+branch,"sha":sha}
	var out githubRefObject
	err := p.doJSON(ctx, token, http.MethodPost, "/repos/"+repository+"/git/refs", body, &out)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrProviderUnavailable) {
		return err
	}
	// GitHub returns 422 when the deterministic idempotent ref already exists.
	// Verify it resolves to the exact requested baseline; otherwise fail closed.
	existing, getErr := p.getRef(ctx, token, repository, branch)
	if getErr != nil {
		return err
	}
	if existing.Object.SHA != sha {
		return fmt.Errorf("%w: existing workspace branch points at different baseline", ErrMalformed)
	}
	return nil
}

func (p *GitHubWorkspaceProvider) getRef(ctx context.Context, token, repository, branch string) (githubRefObject, error) {
	var out githubRefObject
	path := "/repos/" + repository + "/git/ref/heads/" + gitRefPath(branch)
	if err := p.doJSON(ctx, token, http.MethodGet, path, nil, &out); err != nil {
		return githubRefObject{}, err
	}
	return out, nil
}

func (p *GitHubWorkspaceProvider) getRepo(ctx context.Context, token, repository string) (githubRepo, error) {
	var out githubRepo
	if err := p.doJSON(ctx, token, http.MethodGet, "/repos/"+repository, nil, &out); err != nil {
		return githubRepo{}, err
	}
	if out.CloneURL == "" {
		out.CloneURL = strings.TrimRight(p.cfg.WebBase, "/") + "/" + repository + ".git"
	}
	return out, nil
}

func (p *GitHubWorkspaceProvider) deleteBranch(ctx context.Context, token, repository, branch string) error {
	return p.doJSON(ctx, token, http.MethodDelete, "/repos/"+repository+"/git/refs/heads/"+url.PathEscape(branch), nil, nil)
}

func (p *GitHubWorkspaceProvider) controlToken(ctx context.Context) (string, error) {
	v, err := p.resolver.Resolve(ctx, p.cfg.ControlTokenRef, p.cfg.ControlScope)
	if err != nil {
		return "", fmt.Errorf("%w: resolve github control token: %v", ErrProviderUnavailable, err)
	}
	if v == "" {
		return "", fmt.Errorf("%w: empty github control token", ErrProviderUnavailable)
	}
	return v, nil
}

func (p *GitHubWorkspaceProvider) branchName(spec Spec) string {
	h := sha256.Sum256([]byte(spec.IdempotencyKey))
	suffix := hex.EncodeToString(h[:6])
	slug := sanitizeBranchSegment(spec.Name)
	if slug == "" {
		slug = "workspace"
	}
	return strings.Trim(p.cfg.BranchPrefix, "/") + "/" + slug + "-" + suffix
}

var branchSegmentRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
func sanitizeBranchSegment(s string) string {
	s = branchSegmentRE.ReplaceAllString(strings.TrimSpace(s), "-")
	s = strings.Trim(s, ".-")
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

func gitRefPath(ref string) string {
	parts := strings.Split(ref, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

var githubRepoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
func validGitHubRepository(s string) bool { return githubRepoRE.MatchString(s) }

func (p *GitHubWorkspaceProvider) doJSON(ctx context.Context, bearer, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil { return err }
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.cfg.APIBase, "/")+path, reader)
	if err != nil { return err }
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil { req.Header.Set("Content-Type", "application/json") }

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil { return err }

	switch resp.StatusCode {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnprocessableEntity, http.StatusConflict, http.StatusTooManyRequests:
		return fmt.Errorf("%w: github status %d", ErrProviderUnavailable, resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: github status %d", ErrProviderUnavailable, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: github status %d", ErrMalformed, resp.StatusCode)
	}
	if out == nil || len(data) == 0 { return nil }
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: decode github response: %v", ErrMalformed, err)
	}
	return nil
}

var _ Provider = (*GitHubWorkspaceProvider)(nil)
