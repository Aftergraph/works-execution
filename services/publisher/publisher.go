// Package publisher posts work results to external CI surfaces
// (currently GitHub). It defines a small Publisher interface so
// the same call site can target either:
//
//   - the GitHub Statuses API (works with a Personal Access Token;
//     produces commit-status checks), or
//   - the GitHub Check Runs API (requires a GitHub App + installation
//     token; produces rich check runs with annotations, and is the
//     only path GitHub Apps are allowed to use for "must authenticate
//     via a GitHub App" endpoints).
//
// M1 ships both: StatusAPIPublisher is fully implemented and is the
// default for the pilot (no App needed). CheckRunPublisher is wired
// end-to-end so that adding App credentials is a one-line config
// change with no API surface changes for callers.
package publisher

import (
	"context"
	"errors"
	"fmt"
)

// Conclusion mirrors the GitHub Check Run / Status conclusion values
// that WORKS emits. Anything outside this set is a programming error.
type Conclusion string

const (
	ConclusionSuccess Conclusion = "success"
	ConclusionFailure Conclusion = "failure"
	ConclusionPending Conclusion = "pending"
)

// Result is the publish input. We deliberately keep it small and
// flat: the publisher's job is to translate one Result into one or
// more external API calls, not to introspect the Work.
type Result struct {
	// Repository is "owner/name".
	Repository string
	// SHA is the commit SHA the result is for (40-char hex).
	SHA string
	// Conclusion is success/failure/pending.
	Conclusion Conclusion
	// Description is a one-line summary shown in the GitHub UI.
	Description string
	// DetailsURL is the link from the GitHub UI to the work's
	// detail page (usually the works-api detail URL).
	DetailsURL string
	// Output is optional structured output shown in the GitHub UI
	// (text or markdown). Empty = omit the output block.
	Output string
}

// Validate returns nil if the Result has the minimum fields required
// by every Publisher implementation.
func (r *Result) Validate() error {
	if r.Repository == "" {
		return errors.New("publisher: Repository is required")
	}
	if r.SHA == "" {
		return errors.New("publisher: SHA is required")
	}
	if len(r.SHA) != 40 {
		return fmt.Errorf("publisher: SHA must be 40 hex chars, got %d", len(r.SHA))
	}
	switch r.Conclusion {
	case ConclusionSuccess, ConclusionFailure, ConclusionPending:
	default:
		return fmt.Errorf("publisher: invalid conclusion %q", r.Conclusion)
	}
	return nil
}

// Publisher posts a Result to its underlying transport. Implementations
// MUST be safe to call from multiple goroutines. They MUST be
// idempotent on (Repository, SHA) so retries don't produce duplicate
// statuses / check runs.
type Publisher interface {
	// Publish returns nil on success or a non-retryable error. The
	// HTTP-level retry layer in the caller distinguishes 5xx
	// (retry) from 4xx (drop + log).
	Publish(ctx context.Context, r Result) error
	// Kind is "status" or "check-run" — useful for logging and
	// for tests that need to assert which transport was used.
	Kind() string
}