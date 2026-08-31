// Package webhook ingests external CI events (currently GitHub) and
// translates them into Works. The package is intentionally
// transport-agnostic at the boundary: anything that can sign a
// payload and tell us "this is event X, delivery Y" can be added
// behind a new Provider implementation.
//
// This file implements the GitHub provider. It is the only provider
// in M1.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Event names we handle. Other events are accepted, acknowledged, and
// dropped without creating a Work.
const (
	EventPush = "push"
	EventPR   = "pull_request"
	EventPing = "ping"
)

// Sentinel errors. The HTTP layer maps these to status codes.
var (
	ErrMissingSignature  = errors.New("missing X-Hub-Signature-256 header")
	ErrInvalidSignature  = errors.New("invalid HMAC-SHA256 signature")
	ErrMissingDeliveryID = errors.New("missing X-GitHub-Delivery header")
	ErrMissingEvent      = errors.New("missing X-GitHub-Event header")
	ErrUnsupportedEvent  = errors.New("unsupported event type")
	ErrDuplicateDelivery = errors.New("duplicate delivery (idempotent)")
)

// Headers. GitHub sets these on every webhook POST.
const (
	HeaderEvent       = "X-GitHub-Event"
	HeaderDelivery    = "X-GitHub-Delivery"
	HeaderSignature   = "X-Hub-Signature-256"
	HeaderRequestID   = "X-GitHub-Hook-Installation-Target-ID"
	SignaturePrefix   = "sha256="
)

// Delivery is the parsed-in-memory shape of a webhook POST. It is
// what the API layer hands to the rest of WORKS to create a Work.
type Delivery struct {
	DeliveryID string    // X-GitHub-Delivery — idempotency key
	Event      string    // X-GitHub-Event
	ReceivedAt time.Time // when the API received it (server clock)
	Provider   string    // "github" — for future multi-provider

	// Repository identity. Always populated for push + PR events.
	RepoFullName string // e.g. "JonasAbde/works-execution"
	RepoHTMLURL  string
	RepoCloneURL string

	// Git ref / SHA. For push: branch ref + commit SHA. For PR: head SHA.
	Ref string // refs/heads/main or refs/pull/123/head
	SHA string // 40-char hex

	// PR-specific (only for pull_request events).
	PRNumber int
	PRAction string // opened, synchronize, reopened, closed (filtered)
	PRHead   string // source branch
	PRBase   string // target branch
}

// ShouldCreateWork reports whether this delivery should produce a
// new Work. We filter out PR events that aren't actionable (closed,
// labeled, etc.) to keep the work queue clean.
func (d Delivery) ShouldCreateWork() bool {
	switch d.Event {
	case EventPush:
		// Don't create a work for branch deletions (ref becomes
		// refs/heads/<deleted>). Push to a real branch = work.
		return strings.HasPrefix(d.Ref, "refs/heads/") && d.SHA != ""
	case EventPR:
		// Only opened, synchronize (new commits), or reopened.
		switch d.PRAction {
		case "opened", "synchronize", "reopened":
			return d.SHA != "" && d.RepoFullName != ""
		}
	}
	return false
}

// VerifySignature checks the HMAC-SHA256 signature of the raw body
// against the shared webhook secret. The signature header is
// `sha256=<hex>`; we recompute the hex and compare in constant time.
func VerifySignature(secret, signatureHeader string, body []byte) error {
	if signatureHeader == "" {
		return ErrMissingSignature
	}
	if !strings.HasPrefix(signatureHeader, SignaturePrefix) {
		return ErrInvalidSignature
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, SignaturePrefix))
	if err != nil {
		return fmt.Errorf("%w: bad hex: %v", ErrInvalidSignature, err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	if !hmac.Equal(got, want) {
		return ErrInvalidSignature
	}
	return nil
}

// parsePushPayload decodes the subset of the GitHub push event we
// care about. We deliberately accept only the fields needed to
// create a Work — anything else is discarded.
type pushPayload struct {
	Ref     string `json:"ref"`
	After   string `json:"after"` // commit SHA (may be all-zero for branch deletion)
	Before  string `json:"before"`
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

// parsePullRequestPayload decodes the subset of the pull_request
// event we care about.
type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
				CloneURL string `json:"clone_url"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

// ParseGitHubDelivery extracts a Delivery from raw headers and body.
// It does NOT verify the signature — that is the caller's job
// (VerifySignature) so we can return distinct errors for "no header"
// vs "wrong sig" vs "bad payload".
func ParseGitHubDelivery(event, deliveryID string, body []byte) (Delivery, error) {
	if event == "" {
		return Delivery{}, ErrMissingEvent
	}
	if deliveryID == "" {
		return Delivery{}, ErrMissingDeliveryID
	}
	d := Delivery{
		DeliveryID: deliveryID,
		Event:      event,
		Provider:   "github",
		ReceivedAt: time.Now().UTC(),
	}

	switch event {
	case EventPing:
		// ping has no payload we care about. Return a Delivery with
		// the metadata filled but ShouldCreateWork() = false.
		return d, nil

	case EventPush:
		var p pushPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return Delivery{}, fmt.Errorf("decode push payload: %w", err)
		}
		d.Ref = p.Ref
		d.SHA = p.After
		d.RepoFullName = p.Repository.FullName
		d.RepoHTMLURL = p.Repository.HTMLURL
		d.RepoCloneURL = p.Repository.CloneURL

	case EventPR:
		var p pullRequestPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return Delivery{}, fmt.Errorf("decode pull_request payload: %w", err)
		}
		d.PRAction = p.Action
		d.PRNumber = p.Number
		d.SHA = p.PullRequest.Head.SHA
		d.PRHead = p.PullRequest.Head.Ref
		d.PRBase = p.PullRequest.Base.Ref
		d.Ref = "refs/pull/" + strconv.Itoa(p.Number) + "/head"
		// For PRs, the head repo (fork) is the one we clone, not
		// the base repo. Falls back to base repo if the head is
		// the same (no fork).
		if p.PullRequest.Head.Repo.FullName != "" {
			d.RepoFullName = p.PullRequest.Head.Repo.FullName
			d.RepoCloneURL = p.PullRequest.Head.Repo.CloneURL
		} else {
			d.RepoFullName = p.Repository.FullName
			d.RepoHTMLURL = p.Repository.HTMLURL
			d.RepoCloneURL = p.Repository.CloneURL
		}

	default:
		return Delivery{}, fmt.Errorf("%w: %q", ErrUnsupportedEvent, event)
	}
	return d, nil
}
