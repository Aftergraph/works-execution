package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

const CloudflareArtifactsProviderID = "cloudflare-artifacts"

// CredentialStore is the handoff boundary for provider-issued Git credentials.
// The raw token may exist only long enough to cross this call. Implementations
// must persist it outside WORKS state and return an inert secret:// ref.
type CredentialStore interface {
	Put(ctx context.Context, workID, provider, name, value string, expiresAt time.Time) (*secrets.Ref, error)
	Delete(ctx context.Context, ref *secrets.Ref) error
}

// CloudflareArtifactsConfig contains only non-secret provider coordinates plus
// a secret ref for the Cloudflare control-plane token.
type CloudflareArtifactsConfig struct {
	AccountID       string
	Namespace       string
	ControlTokenRef *secrets.Ref
	ControlScope    string
	TokenTTL        time.Duration
	ReadyTimeout    time.Duration
	BaseURL         string // test override; defaults to api.cloudflare.com/client/v4
}

type CloudflareArtifactsProvider struct {
	cfg         CloudflareArtifactsConfig
	resolver    secrets.Resolver
	credentials CredentialStore
	client      *http.Client
}

func NewCloudflareArtifactsProvider(cfg CloudflareArtifactsConfig, resolver secrets.Resolver, credentials CredentialStore, client *http.Client) (*CloudflareArtifactsProvider, error) {
	if strings.TrimSpace(cfg.AccountID) == "" || strings.TrimSpace(cfg.Namespace) == "" {
		return nil, fmt.Errorf("%w: cloudflare account_id and namespace are required", ErrMalformed)
	}
	if cfg.ControlTokenRef == nil || resolver == nil || credentials == nil {
		return nil, fmt.Errorf("%w: control token ref, resolver and credential store are required", ErrMalformed)
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = time.Hour
	}
	if cfg.TokenTTL < time.Minute || cfg.TokenTTL > 365*24*time.Hour {
		return nil, fmt.Errorf("%w: token ttl must be between 1 minute and 1 year", ErrMalformed)
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = 15 * time.Second
	}
	if cfg.ReadyTimeout < 0 {
		return nil, fmt.Errorf("%w: ready timeout must be non-negative", ErrMalformed)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.cloudflare.com/client/v4"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &CloudflareArtifactsProvider{cfg: cfg, resolver: resolver, credentials: credentials, client: client}, nil
}

func (p *CloudflareArtifactsProvider) ID() string { return CloudflareArtifactsProviderID }

type cfEnvelope[T any] struct {
	Result   T         `json:"result"`
	Success  bool      `json:"success"`
	Errors   []cfError `json:"errors"`
	Messages []cfError `json:"messages"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfRepoResult struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Remote        string `json:"remote"`
	Token         string `json:"token"`
}

type cfRepoInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Remote        string `json:"remote"`
}

type cfTokenResult struct {
	ID        string `json:"id"`
	Plaintext string `json:"plaintext"`
	Scope     string `json:"scope"`
	ExpiresAt string `json:"expires_at"`
}

func (p *CloudflareArtifactsProvider) Create(ctx context.Context, spec Spec) (Workspace, error) {
	if err := spec.Validate(); err != nil {
		return Workspace{}, err
	}
	token, err := p.controlToken(ctx)
	if err != nil {
		return Workspace{}, err
	}

	var created cfRepoResult
	switch spec.Baseline.Provider {
	case CloudflareArtifactsProviderID:
		created, err = p.fork(ctx, token, spec)
	default:
		created, err = p.importRepo(ctx, token, spec)
	}
	if err != nil {
		return Workspace{}, err
	}

	// The initial token returned by create/import/fork is intentionally never
	// persisted. Mint a separate short-lived token so WORKS receives a token ID
	// that can later be revoked deterministically through the REST API.
	minted, err := p.mintToken(ctx, token, created.Name, spec.Mode, p.cfg.TokenTTL)
	if err != nil {
		_ = p.deleteRepo(ctx, token, created.Name)
		return Workspace{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339, minted.ExpiresAt)
	if err != nil {
		_ = p.revokeToken(ctx, token, minted.ID)
		_ = p.deleteRepo(ctx, token, created.Name)
		return Workspace{}, fmt.Errorf("workspace: parse cloudflare token expiry: %w", err)
	}
	credRef, err := p.credentials.Put(ctx, spec.WorkID, p.ID(), minted.ID, minted.Plaintext, expiresAt)
	if err != nil {
		_ = p.revokeToken(ctx, token, minted.ID)
		_ = p.deleteRepo(ctx, token, created.Name)
		return Workspace{}, fmt.Errorf("workspace: persist cloudflare credential ref: %w", err)
	}

	now := time.Now().UTC()
	ws := Workspace{
		ID:             created.ID,
		ProviderID:     p.ID(),
		Org:            spec.Org,
		WorkID:         spec.WorkID,
		Name:           created.Name,
		RemoteURL:      created.Remote,
		DefaultBranch:  created.DefaultBranch,
		Baseline:       spec.Baseline,
		Mode:           spec.Mode,
		CredentialRef:  credRef,
		CredentialID:   minted.ID,
		CreatedAt:      now,
		ExpiresAt:      expiresAt,
	}
	if err := ws.Validate(); err != nil {
		_ = p.credentials.Delete(ctx, credRef)
		_ = p.revokeToken(ctx, token, minted.ID)
		_ = p.deleteRepo(ctx, token, created.Name)
		return Workspace{}, err
	}
	return ws, nil
}

func (p *CloudflareArtifactsProvider) Get(ctx context.Context, handle Workspace) (Workspace, error) {
	if err := p.assertOwned(handle); err != nil {
		return Workspace{}, err
	}
	token, err := p.controlToken(ctx)
	if err != nil {
		return Workspace{}, err
	}
	var env cfEnvelope[cfRepoInfo]
	path := p.repoBase() + "/repos/" + url.PathEscape(handle.Name)
	if err := p.doJSON(ctx, token, http.MethodGet, path, nil, &env); err != nil {
		return Workspace{}, err
	}
	out := handle
	out.ID = env.Result.ID
	out.Name = env.Result.Name
	out.RemoteURL = env.Result.Remote
	out.DefaultBranch = env.Result.DefaultBranch
	return out, nil
}

func (p *CloudflareArtifactsProvider) Candidate(ctx context.Context, ws Workspace) (Candidate, error) {
	if err := p.assertOwned(ws); err != nil {
		return Candidate{}, err
	}
	token, err := p.controlToken(ctx)
	if err != nil {
		return Candidate{}, err
	}
	ref := ws.DefaultBranch
	if ref == "" {
		ref = "main"
	}

	var raw map[string]any
	path := p.repoBase() + "/repos/" + url.PathEscape(ws.Name) + "/log?ref=" + url.QueryEscape(ref) + "&limit=1"
	if err := p.doJSON(ctx, token, http.MethodGet, path, nil, &raw); err != nil {
		return Candidate{}, err
	}
	sha := firstCommitHash(raw)
	if sha == "" {
		return Candidate{}, fmt.Errorf("%w: Cloudflare log response did not contain a commit hash", ErrMalformed)
	}
	return Candidate{
		WorkspaceID: ws.ID,
		Repository:  ws.Name,
		Ref:         "refs/heads/" + ref,
		SHA:         sha,
		ProducedAt:  time.Now().UTC(),
	}, nil
}

func (p *CloudflareArtifactsProvider) RevokeCredential(ctx context.Context, ws Workspace) error {
	if err := p.assertOwned(ws); err != nil {
		return err
	}
	if ws.CredentialID == "" {
		return fmt.Errorf("%w: missing credential id", ErrMalformed)
	}
	token, err := p.controlToken(ctx)
	if err != nil {
		return err
	}
	if err := p.revokeToken(ctx, token, ws.CredentialID); err != nil {
		return err
	}
	if ws.CredentialRef != nil {
		if err := p.credentials.Delete(ctx, ws.CredentialRef); err != nil {
			return fmt.Errorf("workspace: delete credential ref: %w", err)
		}
	}
	return nil
}

func (p *CloudflareArtifactsProvider) Destroy(ctx context.Context, ws Workspace) error {
	if err := p.assertOwned(ws); err != nil {
		return err
	}
	token, err := p.controlToken(ctx)
	if err != nil {
		return err
	}
	// Revocation is best-effort before repo deletion. Deleting the repository is
	// the terminal authority boundary: repo-scoped credentials cannot mutate a
	// repository that no longer exists.
	if ws.CredentialID != "" {
		_ = p.revokeToken(ctx, token, ws.CredentialID)
	}
	if err := p.deleteRepo(ctx, token, ws.Name); err != nil {
		return err
	}
	if ws.CredentialRef != nil {
		_ = p.credentials.Delete(ctx, ws.CredentialRef)
	}
	return nil
}

func (p *CloudflareArtifactsProvider) assertOwned(ws Workspace) error {
	if ws.ProviderID != p.ID() {
		return ErrForeignWorkspace
	}
	if ws.Org == "" || ws.WorkID == "" || ws.Name == "" || ws.ID == "" {
		return fmt.Errorf("%w: incomplete cloudflare workspace", ErrMalformed)
	}
	return nil
}

func (p *CloudflareArtifactsProvider) fork(ctx context.Context, token string, spec Spec) (cfRepoResult, error) {
	if strings.TrimSpace(spec.Baseline.Repository) == "" {
		return cfRepoResult{}, fmt.Errorf("%w: cloudflare baseline repository is required", ErrMalformed)
	}
	body := map[string]any{
		"name":                spec.Name,
		"read_only":           spec.Mode == ModeRead,
		"default_branch_only": false,
		"description":         "WORKS isolated workspace for " + spec.WorkID,
	}
	var env cfEnvelope[cfRepoResult]
	path := p.repoBase() + "/repos/" + url.PathEscape(spec.Baseline.Repository) + "/fork"
	if err := p.doJSON(ctx, token, http.MethodPost, path, body, &env); err != nil {
		return cfRepoResult{}, err
	}
	return env.Result, nil
}

func (p *CloudflareArtifactsProvider) importRepo(ctx context.Context, token string, spec Spec) (cfRepoResult, error) {
	remote := strings.TrimSpace(spec.Baseline.RemoteURL)
	if remote == "" && spec.Baseline.Provider == "github" {
		remote = "https://github.com/" + strings.TrimSuffix(spec.Baseline.Repository, ".git") + ".git"
	}
	if !strings.HasPrefix(remote, "https://") {
		return cfRepoResult{}, fmt.Errorf("%w: external baseline requires public https remote_url", ErrMalformed)
	}
	branch := strings.TrimPrefix(spec.Baseline.Ref, "refs/heads/")
	body := map[string]any{
		"url":       remote,
		"read_only": spec.Mode == ModeRead,
	}
	if branch != "" {
		body["branch"] = branch
	}
	var env cfEnvelope[cfRepoResult]
	path := p.repoBase() + "/repos/" + url.PathEscape(spec.Name) + "/import"
	if err := p.doJSON(ctx, token, http.MethodPost, path, body, &env); err != nil {
		return cfRepoResult{}, err
	}

	// If the caller supplied an immutable baseline SHA, prove the imported
	// repository contains it before handing the workspace to an agent.
	if spec.Baseline.SHA != "" {
		if err := p.waitForCommit(ctx, token, spec.Name, spec.Baseline.SHA); err != nil {
			_ = p.deleteRepo(ctx, token, spec.Name)
			return cfRepoResult{}, fmt.Errorf("workspace: imported baseline SHA unavailable: %w", err)
		}
	}
	return env.Result, nil
}

func (p *CloudflareArtifactsProvider) waitForCommit(ctx context.Context, controlToken, repo, sha string) error {
	deadline := time.Now().Add(p.cfg.ReadyTimeout)
	path := p.repoBase() + "/repos/" + url.PathEscape(repo) + "/commit/" + url.PathEscape(sha)
	for {
		var verify map[string]any
		err := p.doJSON(ctx, controlToken, http.MethodGet, path, nil, &verify)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrProviderUnavailable) || time.Now().After(deadline) {
			return err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *CloudflareArtifactsProvider) mintToken(ctx context.Context, controlToken, repo string, mode Mode, ttl time.Duration) (cfTokenResult, error) {
	scope := "read"
	if mode == ModeWrite {
		scope = "write"
	}
	body := map[string]any{
		"repo":  repo,
		"scope": scope,
		"ttl":   int(ttl.Seconds()),
	}
	var env cfEnvelope[cfTokenResult]
	if err := p.doJSON(ctx, controlToken, http.MethodPost, p.repoBase()+"/tokens", body, &env); err != nil {
		return cfTokenResult{}, err
	}
	if env.Result.ID == "" || env.Result.Plaintext == "" || env.Result.ExpiresAt == "" {
		return cfTokenResult{}, fmt.Errorf("%w: incomplete Cloudflare token response", ErrMalformed)
	}
	return env.Result, nil
}

func (p *CloudflareArtifactsProvider) revokeToken(ctx context.Context, controlToken, id string) error {
	var env cfEnvelope[map[string]any]
	return p.doJSON(ctx, controlToken, http.MethodDelete, p.repoBase()+"/tokens/"+url.PathEscape(id), nil, &env)
}

func (p *CloudflareArtifactsProvider) deleteRepo(ctx context.Context, controlToken, name string) error {
	var env cfEnvelope[map[string]any]
	return p.doJSON(ctx, controlToken, http.MethodDelete, p.repoBase()+"/repos/"+url.PathEscape(name), nil, &env)
}

func (p *CloudflareArtifactsProvider) controlToken(ctx context.Context) (string, error) {
	v, err := p.resolver.Resolve(ctx, p.cfg.ControlTokenRef, p.cfg.ControlScope)
	if err != nil {
		return "", fmt.Errorf("%w: resolve Cloudflare control token: %v", ErrProviderUnavailable, err)
	}
	if v == "" {
		return "", fmt.Errorf("%w: empty Cloudflare control token", ErrProviderUnavailable)
	}
	return v, nil
}

func (p *CloudflareArtifactsProvider) repoBase() string {
	return strings.TrimRight(p.cfg.BaseURL, "/") +
		"/accounts/" + url.PathEscape(p.cfg.AccountID) +
		"/artifacts/namespaces/" + url.PathEscape(p.cfg.Namespace)
}

func (p *CloudflareArtifactsProvider) doJSON(ctx context.Context, bearer, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return fmt.Errorf("%w: cloudflare status %d", ErrProviderUnavailable, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: cloudflare status %d", ErrMalformed, resp.StatusCode)
	}
	if len(data) == 0 || out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: decode cloudflare response: %v", ErrMalformed, err)
	}
	if env, ok := out.(*cfEnvelope[map[string]any]); ok && !env.Success {
		return cfAPIError(env.Errors)
	}
	// Generic envelopes cannot be type-asserted across T, so inspect success
	// from raw JSON before returning.
	var meta struct {
		Success bool      `json:"success"`
		Errors  []cfError `json:"errors"`
	}
	if err := json.Unmarshal(data, &meta); err == nil && !meta.Success {
		return cfAPIError(meta.Errors)
	}
	return nil
}

func cfAPIError(errs []cfError) error {
	if len(errs) == 0 {
		return ErrProviderUnavailable
	}
	return fmt.Errorf("%w: cloudflare code=%d message=%s", ErrProviderUnavailable, errs[0].Code, errs[0].Message)
}

// firstCommitHash tolerates minor REST/Workers-shape differences without
// guessing authority. It accepts only fields conventionally used for immutable
// Git commit identifiers and only returns a 40-char hexadecimal SHA-1.
func firstCommitHash(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"hash", "sha", "id"} {
			if s, ok := x[key].(string); ok && isSHA1(s) {
				return s
			}
		}
		for _, key := range []string{"result", "commits", "history", "items"} {
			if h := firstCommitHash(x[key]); h != "" {
				return h
			}
		}
	case []any:
		for _, item := range x {
			if h := firstCommitHash(item); h != "" {
				return h
			}
		}
	}
	return ""
}

func isSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := strconv.ParseUint(s[:16], 16, 64)
	if err != nil {
		return false
	}
	_, err = strconv.ParseUint(s[16:32], 16, 64)
	if err != nil {
		return false
	}
	_, err = strconv.ParseUint(s[32:], 16, 64)
	return err == nil
}

var _ Provider = (*CloudflareArtifactsProvider)(nil)
