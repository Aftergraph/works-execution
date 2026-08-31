// Package pipeline parses works.yml pipeline documents into
// workgraph.Work templates and fetches them from GitHub.
//
// works.yml is the repo-owned CI contract (RFC-0006): a repo commits
// it to declare how its pushes and pull requests should be verified.
// The webhook receiver (services/api) and the works-ci driver both
// consume it, so the parser lives here instead of being duplicated
// in each command.
//
// A parsed Work is a *template*: the caller fills in ID, State, and
// Source (the webhook knows the delivery; the CLI knows the local
// checkout). Requirements (pool/os/arch/confidence) and the node DAG
// (run/needs/cache/env/timeout) come from the document.
package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// pipelineDoc mirrors works.yml.
type pipelineDoc struct {
	Version int                 `yaml:"version"`
	Work    map[string]nodeGroup `yaml:"work"`
}

type nodeGroup struct {
	Triggers     []string            `yaml:"triggers"`
	Requirements requirementsDoc     `yaml:"requirements"`
	Nodes        map[string]nodeDoc  `yaml:"nodes"`
}

type requirementsDoc struct {
	Confidence string `yaml:"confidence"`
	OS         string `yaml:"os"`
	Arch       string `yaml:"arch"`
	Pool       string `yaml:"pool"`
}

type nodeDoc struct {
	Run      string            `yaml:"run"`
	Needs    []string          `yaml:"needs"`
	Cache    bool              `yaml:"cache"`
	TimeoutS int               `yaml:"timeout_s"`
	Env      map[string]string `yaml:"env"`
}

// Parse converts a works.yml document into a Work template. The
// returned Work has a fresh ID, StateCreated, and no Source — callers
// must stamp those. Requirements and the node DAG are taken from the
// document; an empty requirements block yields zero-value
// Requirements (no pool constraint = schedulable by any runner,
// matching the pre-BYOC default).
func Parse(raw []byte) (*workgraph.Work, error) {
	var doc pipelineDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("pipeline: parse yaml: %w", err)
	}
	if len(doc.Work) == 0 {
		return nil, errors.New("pipeline: config has no work entries")
	}
	// V1: a single pipeline per repo. The first (only) entry wins.
	var name string
	for k := range doc.Work {
		name = k
		break
	}
	c := doc.Work[name]

	nodes := map[string]workgraph.Node{}
	for nName, n := range c.Nodes {
		nodes[nName] = workgraph.Node{
			ID:       nName,
			Run:      n.Run,
			Needs:    n.Needs,
			Cache:    n.Cache,
			Env:      n.Env,
			TimeoutS: n.TimeoutS,
		}
	}

	w := &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Objective: workgraph.Objective{
			Type: "verify_change",
		},
		Requirements: workgraph.Requirements{
			OS:         c.Requirements.OS,
			Arch:       c.Requirements.Arch,
			Confidence: c.Requirements.Confidence,
			Pool:       c.Requirements.Pool,
		},
		Graph: workgraph.Graph{Nodes: nodes},
	}
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("pipeline: invalid work: %w", err)
	}
	return w, nil
}

// MatchesTrigger reports whether the pipeline's declared triggers
// include the given GitHub event name ("push" or "pull_request").
// An empty triggers list matches everything (back-compat with
// pipelines written before triggers were enforced).
func MatchesTrigger(raw []byte, event string) bool {
	var doc pipelineDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return false
	}
	for _, g := range doc.Work {
		if len(g.Triggers) == 0 {
			return true
		}
		for _, t := range g.Triggers {
			if t == event {
				return true
			}
		}
	}
	return false
}

// FetchFromGitHub downloads a repo's works.yml at an exact commit SHA
// using the GitHub Contents API:
//
//	GET /repos/{owner}/{repo}/contents/works.yml?ref={sha}
//
// It returns (nil, nil) when the repo has no works.yml (404) so
// callers can fall back to their default behavior. Other errors are
// returned as-is. The token must have Contents:read on the repo.
func FetchFromGitHub(ctx context.Context, token, repoFullName, sha string) ([]byte, error) {
	return fetchFromGitHub(ctx, token, repoFullName, sha, "https://api.github.com")
}

// fetchFromGitHub is FetchFromGitHub with an overridable API base
// (tests point it at a local server).
func fetchFromGitHub(ctx context.Context, token, repoFullName, sha, base string) ([]byte, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("pipeline: fetch works.yml: no GitHub token configured")
	}
	url := fmt.Sprintf("%s/repos/%s/contents/works.yml?ref=%s",
		strings.TrimRight(base, "/"), repoFullName, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("pipeline: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "works-execution/1.0")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pipeline: GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // repo has no works.yml — caller falls back
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pipeline: GET %s: status=%d body=%s",
			url, resp.StatusCode, truncate(string(body), 512))
	}

	// The Contents API returns base64-encoded content.
	var contents struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&contents); err != nil {
		return nil, fmt.Errorf("pipeline: decode contents: %w", err)
	}
	raw, err := decodeBase64(contents.Content)
	if err != nil {
		return nil, fmt.Errorf("pipeline: decode base64: %w", err)
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// decodeBase64 decodes the base64 content field of the GitHub
// Contents API. The payload may contain newlines (GitHub wraps long
// content); strip them before decoding.
func decodeBase64(s string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
	return base64.StdEncoding.DecodeString(cleaned)
}
