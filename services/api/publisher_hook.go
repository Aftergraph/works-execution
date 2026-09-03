package api

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/publisher"
)

// maybePublishOnTerminal fires a publisher.Publish in a background
// goroutine when a Work has just transitioned to a terminal state
// (SUCCEEDED, FAILED, CANCELLED) and has the minimum source info
// required to address the GitHub API (Repository + SHA).
//
// Design notes:
//
//   - Fire-and-forget: the caller's HTTP response is not delayed
//     by the GitHub API.
//   - No retries here: the API surface is one-shot. Operators can
//     re-publish via `works-publisher`.
//   - Errors are logged via s.Logger when set; the goroutine has
//     no other failure surface.
//   - The goroutine uses a derived context with a 30s timeout so
//     a stuck GitHub call cannot leak forever.
//   - Repository is only present for webhook-derived Works (the
//     Source.Repository field added in M1 k-impl-018). CLI Works
//     and `works run` Works skip publish silently.
func (s *Server) maybePublishOnTerminal(w *workgraph.Work) {
	if s.Publisher == nil || w == nil || !w.State.IsTerminal() {
		return
	}
	if w.Source.Repository == "" || len(w.Source.SHA) != 40 {
		return
	}
	// Map our state to GitHub's conclusion enum.
	var conc publisher.Conclusion
	switch w.State {
	case workgraph.StateSucceeded:
		conc = publisher.ConclusionSuccess
	case workgraph.StateFailed:
		conc = publisher.ConclusionFailure
	case workgraph.StateCancelled:
		// GitHub has no "cancelled" conclusion for statuses; map
		// to failure with a descriptive message so the UI is
		// informative.
		conc = publisher.ConclusionFailure
	default:
		return
	}
	res := publisher.Result{
		Repository:  w.Source.Repository,
		SHA:         w.Source.SHA,
		Conclusion:  conc,
		Description: "works-execution/" + w.ID,
		DetailsURL:  s.publisherDetailsURL(w),
	}
	if len(w.Evidence) > 0 {
		if m, ok := w.Evidence[0].Details["summary"].(string); ok {
			res.Output = m
		}
	}
	// k-068 lifecycle: check the shutdown gate BEFORE Add() so we
	// cannot race a concurrent WaitPublisher (Add-after-Wait is the
	// classic WaitGroup misuse). Fail-closed: once the server is
	// draining, a terminal transition loses its GitHub status update
	// rather than stacking goroutines past process exit.
	if s.publisherShutdownGuard() {
		if s.Logger != nil {
			s.Logger.Printf("publisher: skipping publish work=%s repo=%s: server draining",
				w.ID, res.Repository)
		}
		return
	}
	s.publisherWG.Add(1)
	go func() {
		defer s.publisherWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Snapshot the logger at goroutine start: the Server may be
		// torn down while we are in flight.
		logger := s.Logger
		if err := s.Publisher.Publish(ctx, res); err != nil && logger != nil {
			logger.Printf("publisher: publish failed work=%s repo=%s sha=%s err=%v",
				w.ID, res.Repository, res.SHA, err)
		}
	}()
}

// PublishTerminal is the test-exported alias for the private
// maybePublishOnTerminal. Test packages in api_test cannot call
// unexported methods, so we expose this thin wrapper.
func (s *Server) PublishTerminal(w *workgraph.Work) {
	s.maybePublishOnTerminal(w)
}

// publisherDetailsURL returns the works-api detail URL for the
// work. The host comes from WORKS_PUBLIC_URL env; when unset, the
// URL is omitted and GitHub falls back to no link.
func (s *Server) publisherDetailsURL(w *workgraph.Work) string {
	base := os.Getenv("WORKS_PUBLIC_URL")
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/v1/works/" + w.ID
}
