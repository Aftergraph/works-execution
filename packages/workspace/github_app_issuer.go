package workspace

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

// GitHubAppIssuerConfig configures a GitHub App installation-token issuer.
// The private key is referenced, never persisted in WORKS state.
type GitHubAppIssuerConfig struct {
	AppID             int64
	InstallationID    int64
	PrivateKeyRef     *secrets.Ref
	PrivateKeyScope   string
	APIBase           string
}

type GitHubAppIssuer struct {
	cfg      GitHubAppIssuerConfig
	resolver secrets.Resolver
	client   *http.Client
	now      func() time.Time
}

func NewGitHubAppIssuer(cfg GitHubAppIssuerConfig, resolver secrets.Resolver, client *http.Client) (*GitHubAppIssuer, error) {
	if cfg.AppID <= 0 || cfg.InstallationID <= 0 || cfg.PrivateKeyRef == nil || resolver == nil {
		return nil, fmt.Errorf("%w: app id, installation id, private key ref and resolver are required", ErrMalformed)
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &GitHubAppIssuer{
		cfg: cfg, resolver: resolver, client: client,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

type githubInstallationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (g *GitHubAppIssuer) Issue(ctx context.Context, repository, workID string, mode Mode, ttl time.Duration) (IssuedCredential, error) {
	if !validGitHubRepository(repository) {
		return IssuedCredential{}, fmt.Errorf("%w: invalid github repository", ErrMalformed)
	}
	if strings.TrimSpace(workID) == "" {
		return IssuedCredential{}, fmt.Errorf("%w: work id required", ErrMalformed)
	}
	if mode != ModeRead && mode != ModeWrite {
		return IssuedCredential{}, fmt.Errorf("%w: unsupported github credential mode", ErrMalformed)
	}

	privateKeyPEM, err := g.resolver.Resolve(ctx, g.cfg.PrivateKeyRef, g.cfg.PrivateKeyScope)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("%w: resolve github app private key: %v", ErrProviderUnavailable, err)
	}
	key, err := parseRSAPrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("%w: invalid github app private key", ErrMalformed)
	}
	jwt, err := g.signAppJWT(key)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("%w: sign github app jwt", ErrProviderUnavailable)
	}

	parts := strings.Split(repository, "/")
	repoName := parts[1]
	permission := "read"
	if mode == ModeWrite {
		permission = "write"
	}
	body := map[string]any{
		"repositories": []string{repoName},
		"permissions": map[string]string{
			"contents": permission,
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return IssuedCredential{}, err
	}
	endpoint := strings.TrimRight(g.cfg.APIBase, "/") +
		"/app/installations/" + strconv.FormatInt(g.cfg.InstallationID, 10) + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return IssuedCredential{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("%w: github app token request: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return IssuedCredential{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusNotFound {
			return IssuedCredential{}, ErrNotFound
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return IssuedCredential{}, fmt.Errorf("%w: github app token status %d", ErrProviderUnavailable, resp.StatusCode)
		}
		return IssuedCredential{}, fmt.Errorf("%w: github app token status %d", ErrMalformed, resp.StatusCode)
	}
	var out githubInstallationTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return IssuedCredential{}, fmt.Errorf("%w: decode github app token", ErrMalformed)
	}
	if out.Token == "" || out.ExpiresAt.IsZero() {
		return IssuedCredential{}, fmt.Errorf("%w: incomplete github app token response", ErrMalformed)
	}

	// GitHub controls installation-token expiry (normally one hour). The caller
	// can request a shorter logical TTL; WORKS never claims authority beyond it.
	effectiveExpiry := out.ExpiresAt
	if ttl > 0 {
		requested := g.now().Add(ttl)
		if requested.Before(effectiveExpiry) {
			effectiveExpiry = requested
		}
	}

	// GitHub's REST API does not expose a stable server token id for installation
	// access tokens. Use a one-way fingerprint as non-secret correlation id.
	sum := sha256.Sum256([]byte(out.Token))
	id := "ghinst_" + base64.RawURLEncoding.EncodeToString(sum[:12])
	return IssuedCredential{
		ID: id,
		Plaintext: out.Token,
		ExpiresAt: effectiveExpiry,
	}, nil
}

// Revoke invalidates one installation access token server-side. GitHub's
// endpoint authenticates with the token being revoked, so the caller resolves
// the secret ref only for this boundary call and never persists plaintext.
func (g *GitHubAppIssuer) Revoke(ctx context.Context, id, plaintext string) error {
	if !strings.HasPrefix(id, "ghinst_") || plaintext == "" {
		return fmt.Errorf("%w: invalid github installation credential", ErrMalformed)
	}
	sum := sha256.Sum256([]byte(plaintext))
	want := "ghinst_" + base64.RawURLEncoding.EncodeToString(sum[:12])
	if id != want {
		return fmt.Errorf("%w: github installation credential correlation mismatch", ErrMalformed)
	}
	endpoint := strings.TrimRight(g.cfg.APIBase, "/") + "/installation/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+plaintext)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: revoke github installation token: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		return nil // already expired/revoked is idempotent teardown
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return fmt.Errorf("%w: github revoke status %d", ErrProviderUnavailable, resp.StatusCode)
	}
	return fmt.Errorf("%w: github revoke status %d", ErrMalformed, resp.StatusCode)
}

func (g *GitHubAppIssuer) signAppJWT(key *rsa.PrivateKey) (string, error) {
	now := g.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claimsRaw, err := json.Marshal(map[string]any{
		"iat": now.Add(-30 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(g.cfg.AppID, 10),
	})
	if err != nil {
		return "", err
	}
	claims := base64.RawURLEncoding.EncodeToString(claimsRaw)
	input := header + "." + claims
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no pem block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not rsa private key")
	}
	return key, nil
}

var _ GitCredentialIssuer = (*GitHubAppIssuer)(nil)
