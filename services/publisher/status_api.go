package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// StatusAPIPublisher posts Results via the GitHub Statuses API:
//
//	POST /repos/{owner}/{repo}/statuses/{sha}
//
// Authentication: a Personal Access Token (classic `ghp_*` or fine-
// grained `github_pat_*`) sent as `Authorization: Bearer <token>`.
// Works today; no GitHub App required.
//
// Idempotency: GitHub treats (sha, context) as the dedup key. We
// always emit the same context ("works-execution") so a retry
// updates the existing status rather than creating a new one.
//
// Limitations vs Check Runs: no annotations, no rich output panel,
// no per-step breakdown. Operators who need that should switch to
// CheckRunPublisher once they have an App.
type StatusAPIPublisher struct {
	// BaseURL is the API base. Default: https://api.github.com.
	BaseURL string
	// Token is the PAT. Required.
	Token string
	// Context is the GitHub status "context" label. Default:
	// "works-execution". This is the user-visible label in PRs
	// ("All checks have passed — works-execution").
	Context string
	// HTTPClient overrides the default client (timeouts, etc).
	HTTPClient *http.Client
}

// NewStatusAPIPublisher returns a StatusAPIPublisher with sane
// defaults applied. Returns an error if token is empty.
func NewStatusAPIPublisher(token string) (*StatusAPIPublisher, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("publisher: StatusAPIPublisher requires a non-empty token")
	}
	return &StatusAPIPublisher{
		BaseURL: "https://api.github.com",
		Token:   token,
		Context: "works-execution",
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

// Kind implements Publisher.
func (s *StatusAPIPublisher) Kind() string { return "status" }

// statusBody is the GitHub Statuses API request body. GitHub's
// schema requires `state` to be one of success/failure/pending.
type statusBody struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

// Publish implements Publisher.
func (s *StatusAPIPublisher) Publish(ctx context.Context, r Result) error {
	if err := r.Validate(); err != nil {
		return err
	}
	body := statusBody{
		State:       string(r.Conclusion),
		Context:     s.Context,
		Description: r.Description,
		TargetURL:   r.DetailsURL,
	}
	// GitHub requires "failure" to be the literal lowercase string
	// for failure. Sanity check.
	switch body.State {
	case string(ConclusionSuccess), string(ConclusionFailure), string(ConclusionPending):
	default:
		return fmt.Errorf("publisher: status state %q not in GitHub's enum", body.State)
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("publisher: marshal body: %w", err)
	}
	url := fmt.Sprintf("%s/repos/%s/statuses/%s",
		strings.TrimRight(s.BaseURL, "/"),
		r.Repository,
		r.SHA,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("publisher: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "works-execution/1.0")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("publisher: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// 4xx is non-retryable; 5xx is retryable. We don't return the
	// distinction in the error type yet, but we DO put it in the
	// message so callers can grep.
	return fmt.Errorf("publisher: POST %s: status=%d body=%s",
		url, resp.StatusCode, truncate(string(respBody), 512))
}

func (s *StatusAPIPublisher) client() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return http.DefaultClient
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}