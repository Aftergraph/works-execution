// Package api: M1 webhook handler (k-impl-018).
//
// Receives GitHub deliveries on POST /v1/webhook/github, verifies
// their HMAC-SHA256 signature, deduplicates by X-GitHub-Delivery,
// and creates a Work. The Work is source-mapped (push → branch,
// PR → head ref) so the worker can clone the exact SHA later
// (k-impl-019).
//
// The handler is intentionally NOT behind requireBearer: webhooks
// are server-to-server and the security boundary is the HMAC
// signature, not a Bearer token. Operators must firewall the
// /v1/webhook prefix if they want it off the public listener.
package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/JonasAbde/works-execution/packages/pipeline"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/webhook"
)

// WebhookConfig configures the webhook handler. Secret is the
// shared HMAC secret registered with the GitHub App / webhook
// settings. An empty Secret causes the handler to return 503
// (fail-closed — operators must explicitly opt in).
type WebhookConfig struct {
	// Secret is the HMAC-SHA256 secret. Empty = webhook disabled.
	Secret string
	// AllowedRepos, if non-empty, restricts which repos may
	// create works. Empty = allow all (useful for OSS
	// self-hosted mode where the operator already has admin
	// access to the repo).
	AllowedRepos map[string]bool
	// ProductionAccess, when true, marks every webhook-derived
	// Work with policy.production_access=true so the OPA policy
	// admits it without further evidence. Most M1 pilot runs
	// want this on.
	ProductionAccess bool
	// GitHubToken is the credential used to fetch the repo's
	// works.yml (Contents API, Contents:read). When empty, the
	// webhook falls back to the built-in single-node verify
	// behavior. Usually the same PAT as the publisher.
	GitHubToken string
}

// githubWebhookHandler handles POST /v1/webhook/github.
//
// Idempotency: deliveries are keyed by X-GitHub-Delivery. A
// duplicate delivery returns 200 with no new work created.
//
// Flow:
//  1. Read body, capture delivery_id + event + signature from headers.
//  2. Verify signature.
//  3. Check webhooks table for prior delivery; if seen, return 200.
//  4. Parse payload, derive Delivery.
//  5. If !Delivery.ShouldCreateWork() (e.g. closed PR, ping), return 200.
//  6. Repo allow-list check.
//  7. Create a Work with source/sha/clone/html/pr fields populated.
//  8. Insert webhooks row with delivery_id, event, work_id, raw_body.
//  9. Return 201 with {work_id, status}.
func (s *Server) githubWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if s.WebhookConfig == nil || s.WebhookConfig.Secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":   "webhook_disabled",
			"message": "webhook receiver is not configured (no secret)",
		})
		return
	}

	// Read body fully so we can both verify the signature and
	// store the raw payload for replay/audit.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "read_body",
			"message": err.Error(),
		})
		return
	}

	event := r.Header.Get(webhook.HeaderEvent)
	deliveryID := r.Header.Get(webhook.HeaderDelivery)
	signature := r.Header.Get(webhook.HeaderSignature)

	// Verify signature first. This is the security boundary; do
	// not parse or store anything until we know the request is
	// genuine.
	if err := webhook.VerifySignature(s.WebhookConfig.Secret, signature, body); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "invalid_signature",
			"message": err.Error(),
		})
		return
	}

	// Idempotency: check the webhooks table.
	existing, err := s.Store.LookupWebhookDelivery(r.Context(), deliveryID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":   "idempotency_check",
			"message": err.Error(),
		})
		return
	}
	if existing != "" {
		// Duplicate delivery: return 200 with the prior work_id.
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "duplicate",
			"work_id": existing,
		})
		return
	}

	// Parse the delivery.
	delivery, err := webhook.ParseGitHubDelivery(event, deliveryID, body)
	if err != nil {
		// Bad payload or unsupported event: 200 (we don't want
		// GitHub to retry forever on a bad event).
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ignored",
			"reason": err.Error(),
		})
		return
	}

	if !delivery.ShouldCreateWork() {
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ignored",
			"reason": "delivery should not create a work (e.g. closed PR, ping)",
		})
		return
	}

	// Repo allow-list check.
	if s.WebhookConfig.AllowedRepos != nil && !s.WebhookConfig.AllowedRepos[delivery.RepoFullName] {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "repo_not_allowed",
			"message": "repository " + delivery.RepoFullName + " is not in AllowedRepos",
		})
		return
	}

	// Build the Work. Preferred: the repo's works.yml (RFC-0006) —
	// the repo owns its pipeline (DAG, pool pinning, cache). When
	// the repo has no works.yml (or no token is configured), fall
	// back to the built-in single-node verify behavior.
	workID := workgraph.NewID("wrk")
	g, err := s.workFromPipeline(r.Context(), delivery)
	if err != nil {
		if errors.Is(err, errPipelineSkip) {
			// The repo's works.yml exists but doesn't trigger on
			// this event — ack and skip, like GitHub Actions.
			writeJSON(w, http.StatusOK, map[string]string{
				"status": "ignored",
				"reason": "works.yml does not trigger on " + delivery.Event,
			})
			return
		}
		// A broken works.yml must not silently degrade to the
		// fallback: the repo declared a pipeline and it failed to
		// load. Surface it as a 502 so the operator sees it.
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "pipeline_load_failed",
			"message": err.Error(),
		})
		return
	}
	if g == nil {
		g = s.defaultWebhookWork(delivery, workID)
	} else {
		g.ID = workID
		g.State = workgraph.StateQueued
		g.Source = webhookSource(delivery)
		g.Policy = workgraph.Policy{
			ProductionAccess: s.WebhookConfig.ProductionAccess,
		}
	}
	g.Source.Actor = r.Header.Get("X-GitHub-User")
	g.CorrelationID = workgraph.NewID("cor")

	// Persist the work.
	if err := s.Store.CreateWork(r.Context(), g); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":   "create_work",
			"message": err.Error(),
		})
		return
	}

	// Persist the webhook delivery (idempotency record).
	// Best-effort: a failure here would only break dedup for this
	// one delivery; the Work itself is durable.
	_ = s.Store.RecordWebhookDelivery(r.Context(), deliveryID, event, workID, string(body))

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":      "created",
		"work_id":     workID,
		"repository":  delivery.RepoFullName,
		"sha":         delivery.SHA,
		"event":       event,
		"delivery_id": deliveryID,
	})
}

func verifyCommand(d webhook.Delivery) string {
	switch d.RepoFullName {
	case "JonasAbde/Renos-Control":
		return "npm ci && npm --prefix backend/operations ci && npm run verify"
	case "JonasAbde/works-execution":
		return "go vet ./... && go test ./..."
	default:
		// Allow-list configuration is the primary guard; this second guard
		// makes an accidental config expansion fail, never succeed.
		return "exit 78"
	}
}

// errPipelineSkip is returned by workFromPipeline when the repo's
// works.yml exists but does not declare the delivery's event in its
// triggers — the webhook must not create a work at all (same as
// GitHub Actions ignoring a workflow that doesn't trigger on the
// event).
var errPipelineSkip = errors.New("pipeline: event not in triggers")

// workFromPipeline tries to build the Work from the repo's works.yml
// (RFC-0006). Returns:
//
//   - (nil, nil) when the repo has no works.yml or no GitHub token is
//     configured — the caller falls back to defaultWebhookWork;
//   - (work, nil) when the pipeline loaded and the delivery's event
//     matches a declared trigger;
//   - (nil, errPipelineSkip) when the pipeline exists but the event is
//     not in its triggers — the caller must NOT create a work;
//   - (nil, err) when the pipeline exists but cannot be loaded or
//     parsed — the caller must surface the error, not degrade.
//
// The returned Work is a template: ID/State/Source/Policy are stamped
// by the caller.
func (s *Server) workFromPipeline(ctx context.Context, d webhook.Delivery) (*workgraph.Work, error) {
	if s.WebhookConfig == nil || s.WebhookConfig.GitHubToken == "" {
		return nil, nil
	}
	fetch := s.PipelineFetcher
	if fetch == nil {
		fetch = pipeline.FetchFromGitHub
	}
	raw, err := fetch(ctx, s.WebhookConfig.GitHubToken, d.RepoFullName, d.SHA)
	if err != nil {
		return nil, fmt.Errorf("fetch works.yml: %w", err)
	}
	if raw == nil {
		return nil, nil // repo has no works.yml — fall back
	}
	// Parse FIRST: a broken works.yml must surface as an error, not
	// be masked by the trigger check (which returns false for
	// unparseable documents).
	w, err := pipeline.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse works.yml: %w", err)
	}
	if !pipeline.MatchesTrigger(raw, d.Event) {
		return nil, errPipelineSkip
	}
	return w, nil
}

// defaultWebhookWork builds the built-in single-node verify Work used
// when the repo has no works.yml (or no token is configured). The
// graph is single-node ("build") with a runtime that calls out to the
// real runner (k-impl-019/020).
func (s *Server) defaultWebhookWork(d webhook.Delivery, workID string) *workgraph.Work {
	g := &workgraph.Work{
		ID:    workID,
		State: workgraph.StateQueued,
		Source: webhookSource(d),
		Objective: workgraph.Objective{
			Type: "verify_change",
		},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{}},
		Policy: workgraph.Policy{
			ProductionAccess: s.WebhookConfig.ProductionAccess,
		},
	}
	g.Graph.Nodes["build"] = workgraph.Node{
		ID:       "build",
		Run:      verifyCommand(d),
		TimeoutS: 600,
		// M1 delegates actual building to services/runner which
		// is called by the worker. The node's `Run` is a marker
		// for slice-1+2 evidence; the real execution path is
		// the worker's k-impl-020 integration that checks out
		// source/019 and runs go test ./... directly.
	}
	return g
}

// webhookSource maps a webhook delivery to its workgraph.Source.
func webhookSource(d webhook.Delivery) workgraph.Source {
	return workgraph.Source{
		Type:       sourceTypeFor(d),
		Repository: d.RepoFullName,
		Ref:        d.Ref,
		Actor:      "", // set by the handler from X-GitHub-User
		Branch:     branchFromRef(d.Ref, d.Event),
		SHA:        d.SHA,
		HTMLURL:    d.RepoHTMLURL,
		CloneURL:   d.RepoCloneURL,
		PRNumber:   d.PRNumber,
		PRAction:   d.PRAction,
		PRHead:     d.PRHead,
		PRBase:     d.PRBase,
	}
}

// sourceTypeFor maps a webhook delivery to its workgraph.Source.Type.
// Push and PR are kept distinct so consumers (UI, audit, future
// scheduling) can filter on them without re-deriving from the event.
func sourceTypeFor(d webhook.Delivery) string {
	switch d.Event {
	case webhook.EventPush:
		return "github_push"
	case webhook.EventPR:
		return "github_pull_request"
	default:
		return "github_" + d.Event
	}
}

// branchFromRef strips the "refs/heads/" or "refs/pull/" prefix from
// a git ref so Source.Branch is human-friendly. PR refs keep their
// "pull/" prefix to disambiguate from a real branch of the same
// number.
func branchFromRef(ref, event string) string {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/pull/") && event == webhook.EventPR:
		return ref
	}
	return ref
}

// shaID is reserved for deterministic work IDs derived from webhook
// deliveries (e.g. when callers want a stable mapping delivery_id
// → work_id across replays). Not used by the default handler, which
// prefers fresh workgraph.NewID per delivery to keep retries safe.
func shaID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(h[:16])
}