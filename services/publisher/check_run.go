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

// CheckRunPublisher posts Results via the GitHub Check Runs API:
//
//	POST /repos/{owner}/{repo}/check-runs
//
// Authentication: a GitHub App installation token. PATs are NOT
// allowed for this endpoint (GitHub returns 403 "must authenticate
// via a GitHub App"). M1 ships the full implementation; activate it
// by setting an installation token via SetInstallationToken once the
// App is created.
//
// Idempotency: GitHub dedups on (repository, head_sha, name). We
// always use name="works-execution", so a retry updates the same
// check run instead of creating a new one. To force a fresh run
// per Work, pass a unique ExternalID; we set one derived from the
// Result's Description so retries land on the same row.
type CheckRunPublisher struct {
	// BaseURL is the API base. Default: https://api.github.com.
	BaseURL string
	// AppID is the GitHub App ID. Required at construction.
	AppID int64
	// Name is the check run name. Default: "works-execution".
	Name string
	// InstallationToken returns a fresh installation access token
	// for the repo. The caller (or an apps.Auth helper) supplies
	// this; CheckRunPublisher just calls it for every Publish so
	// tokens can rotate without restart.
	InstallationToken func(ctx context.Context, repo string) (string, error)
	// HTTPClient overrides the default client.
	HTTPClient *http.Client
}

// NewCheckRunPublisher returns a CheckRunPublisher. installationToken
// is the only required runtime dependency; without it Publish will
// return an error every time. We don't fail at construction so that
// processes that load the publisher before the App is fully wired
// still start cleanly.
func NewCheckRunPublisher(appID int64, installationToken func(ctx context.Context, repo string) (string, error)) (*CheckRunPublisher, error) {
	if installationToken == nil {
		return nil, errors.New("publisher: CheckRunPublisher requires a non-nil InstallationToken function")
	}
	return &CheckRunPublisher{
		BaseURL:           "https://api.github.com",
		AppID:             appID,
		Name:              "works-execution",
		InstallationToken: installationToken,
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

// Kind implements Publisher.
func (c *CheckRunPublisher) Kind() string { return "check-run" }

// checkRunBody is the GitHub Check Runs API request body.
type checkRunBody struct {
	Name         string `json:"name"`
	HeadSHA      string `json:"head_sha"`
	Status       string `json:"status,omitempty"`         // queued/in_progress/completed
	Conclusion   string `json:"conclusion,omitempty"`      // success/failure/...
	DetailsURL   string `json:"details_url,omitempty"`
	ExternalID   string `json:"external_id,omitempty"`     // idempotency key (unique per run)
	OutputTitle  string `json:"output,omitempty"`          // we use the same struct for both; see below
}

// Publish implements Publisher.
func (c *CheckRunPublisher) Publish(ctx context.Context, r Result) error {
	if err := r.Validate(); err != nil {
		return err
	}
	tok, err := c.InstallationToken(ctx, r.Repository)
	if err != nil {
		return fmt.Errorf("publisher: get installation token: %w", err)
	}
	body := map[string]any{
		"name":        c.Name,
		"head_sha":    r.SHA,
		"status":      "completed",
		"conclusion":  string(r.Conclusion),
		"details_url": r.DetailsURL,
		// external_id is GitHub's idempotency key. We derive it from
		// the Description (which the caller sets to "works-execution/<work_id>")
		// so retries update the existing row. Falls back to SHA when empty.
		"external_id": externalIDFrom(r),
	}
	if r.Description != "" {
		body["output"] = map[string]any{
			"title":   "works-execution",
			"summary": r.Description,
			"text":    r.Output,
		}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("publisher: marshal body: %w", err)
	}
	url := fmt.Sprintf("%s/repos/%s/check-runs",
		strings.TrimRight(c.BaseURL, "/"),
		r.Repository,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("publisher: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "works-execution/1.0")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("publisher: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("publisher: POST %s: status=%d body=%s",
		url, resp.StatusCode, truncate(string(respBody), 512))
}

func externalIDFrom(r Result) string {
	if r.Description != "" {
		// "works-execution/<work_id>" — works as the idempotency key.
		return r.Description
	}
	return r.SHA
}

func (c *CheckRunPublisher) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// NoopPublisher is a Publisher that records calls but never makes
// a network request. Used by tests and by deployments that
// explicitly disable GitHub publication.
type NoopPublisher struct {
	Recorded [] Result
	KindStr   string
}

// NewNoopPublisher returns a NoopPublisher with the given Kind label.
func NewNoopPublisher(kind string) *NoopPublisher {
	return &NoopPublisher{KindStr: kind}
}

// Kind implements Publisher.
func (n *NoopPublisher) Kind() string { return n.KindStr }

// Publish implements Publisher.
func (n *NoopPublisher) Publish(_ context.Context, r Result) error {
	if err := r.Validate(); err != nil {
		return err
	}
	n.Recorded = append(n.Recorded, r)
	return nil
}