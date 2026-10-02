package api

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/publisher"
)

// terminalPublishResult maps a terminal Work onto a publisher.Result, and
// reports false when the Work is not publishable at all.
//
// This is the SINGLE publish-decision implementation. Before ADR-0033 it
// lived inline inside maybePublishOnTerminal; it is factored out because
// two drivers now use it — the outbox dispatcher handler and the direct
// PublishTerminal escape hatch — and two copies of "which states publish,
// and what conclusion they map to" would inevitably diverge.
//
// Guard order is preserved exactly as it was:
//   - no publisher configured            -> skip
//   - nil Work, or non-terminal state    -> skip
//   - no repository, or SHA not 40 chars -> skip (CLI works, `works run`)
//
// ADR-0032: TIMED_OUT is terminal and therefore publishes, mapped to
// GitHub's failure conclusion for the same reason CANCELLED is — GitHub's
// status API has no distinct conclusion for either, and the description
// carries the real reason.
func (s *Server) terminalPublishResult(w *workgraph.Work) (publisher.Result, bool) {
	if s.Publisher == nil || w == nil || !w.State.IsTerminal() {
		return publisher.Result{}, false
	}
	if w.Source.Repository == "" || len(w.Source.SHA) != 40 {
		return publisher.Result{}, false
	}
	// Map our state to GitHub's conclusion enum.
	var conc publisher.Conclusion
	switch w.State {
	case workgraph.StateSucceeded:
		conc = publisher.ConclusionSuccess
	case workgraph.StateFailed, workgraph.StateCancelled, workgraph.StateTimedOut:
		// GitHub has no "cancelled" or "timed out" conclusion for
		// statuses; map to failure so the UI is informative. The
		// Description names the Works run, so the distinction is
		// visible without inventing an enum value GitHub rejects.
		conc = publisher.ConclusionFailure
	default:
		return publisher.Result{}, false
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
	return res, true
}

// publishWorkNow performs the publish synchronously. It is the shared
// delivery primitive: the outbox handler calls it inside the dispatcher's
// context, and maybePublishOnTerminal calls it inside a goroutine.
func (s *Server) publishWorkNow(ctx context.Context, w *workgraph.Work) error {
	res, ok := s.terminalPublishResult(w)
	if !ok {
		return nil
	}
	return s.Publisher.Publish(ctx, res)
}

// maybePublishOnTerminal fires a publisher.Publish in a background
// goroutine for a terminal Work.
//
// ADR-0033 MIGRATION — this is no longer on the request-handling path.
// It used to be called from completeLease immediately after
// Store.CompleteLease returned, which meant the publish decision was
// made outside the state mutation's transaction: a crash between commit
// and the GitHub call lost the status update permanently, and nothing
// durable recorded that it was owed. The terminal transition now records
// a work.terminal obligation in the outbox inside UpdateState's own
// transaction, and (s *Server).outboxPublishHandler delivers it.
//
// The function is retained, deliberately, as:
//
//   - the delivery primitive for operator tooling and tests, exposed via
//     PublishTerminal; and
//   - the shape of the k-068 lifecycle guard, which still applies to the
//     outbox handler because delivery also happens during shutdown.
//
// Design notes (unchanged):
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
	if _, ok := s.terminalPublishResult(w); !ok {
		return
	}
	// k-068 lifecycle: check the shutdown gate BEFORE Add() so we
	// cannot race a concurrent WaitPublisher (Add-after-Wait is the
	// classic WaitGroup misuse). Fail-closed: once the server is
	// draining, a terminal transition loses its GitHub status update
	// rather than stacking goroutines past process exit.
	if s.publisherShutdownGuard() {
		if s.Logger != nil {
			s.Logger.Printf("publisher: skipping publish work=%s: server draining", w.ID)
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
		if err := s.publishWorkNow(ctx, w); err != nil && logger != nil {
			logger.Printf("publisher: publish failed work=%s err=%v", w.ID, err)
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
