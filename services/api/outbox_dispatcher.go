package api

// Outbox dispatcher wiring (ADR-0033).
//
// Store.UpdateState records a work.terminal obligation in the same
// transaction as the terminal state change. This file is the consumer:
// it claims entries, decides whether each one is actually publishable, and
// delivers it — retrying on failure until the entry's attempt budget is
// spent.
//
// The handler is deliberately thin. The store owns durability and the claim
// protocol; this file owns the only knowledge specific to GitHub. Nothing
// here writes Works state, so a publisher outage cannot corrupt execution
// state — the worst outcome is a Work that finished with a late status
// update, which is exactly the failure the old fire-and-forget hook had
// with no recourse at all.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/services/work/store"
)

// outboxPublishDeliveryTimeout bounds a single GitHub call made by the
// dispatcher. It must be comfortably below the dispatcher's default claim
// TTL (30s), because a delivery that outlives its own claim can be
// redelivered by a second dispatcher — see store.ClaimOutbox.
const outboxPublishDeliveryTimeout = 20 * time.Second

// outboxPublishHandler returns the handler that delivers work.terminal
// entries to the configured publisher.
//
// Delivery semantics, restated because they are easy to get wrong:
//
//   - A Work with no GitHub provenance (CLI works, `works run`) is NOT an
//     error. terminalPublishResult reports "not publishable" and the entry
//     is marked delivered without a side effect — otherwise every CLI run
//     would burn its retry budget on a permanently impossible publish and
//     end up DEAD, which is noise rather than signal.
//   - A real publisher error IS retried. That is the entire reason the
//     outbox exists: a transient GitHub 5xx during a Work's completion used
//     to be dropped on the floor.
//   - An unknown topic is a programming error and fails loudly, so a
//     misrouted entry surfaces instead of being silently retired.
func (s *Server) outboxPublishHandler() store.OutboxHandler {
	return func(ctx context.Context, e store.OutboxEntry) error {
		if e.Topic != store.OutboxTopicWorkTerminal {
			return fmt.Errorf("outbox: unknown topic %q", e.Topic)
		}
		payload, err := store.DecodeTerminalWorkPayload(e.PayloadJSON)
		if err != nil {
			return fmt.Errorf("outbox: %w", err)
		}
		work, err := s.Store.GetWork(ctx, payload.WorkID)
		if err != nil {
			// A Work that no longer exists can never be published. Retrying
			// will not bring it back, so retire the entry rather than
			// looping it to DEAD.
			if errors.Is(err, store.ErrNotFound) {
				if s.Logger != nil {
					s.Logger.Printf("outbox: terminal work %s no longer exists; retiring entry %s", payload.WorkID, e.ID)
				}
				return nil
			}
			return fmt.Errorf("outbox: load terminal work %s: %w", payload.WorkID, err)
		}
		// k-068: the same shutdown gate the old hook used. During drain we
		// stop starting NEW publishes; entries stay CLAIMED-then-released
		// (delivery returns an error, so FailOutboxAttempt returns them to
		// PENDING) and are picked up by the next process to start.
		if s.publisherShutdownGuard() {
			return errServerDraining
		}
		deliverCtx, cancel := context.WithTimeout(ctx, outboxPublishDeliveryTimeout)
		defer cancel()
		if err := s.publishWorkNow(deliverCtx, work); err != nil {
			return err
		}
		if s.Logger != nil {
			s.Logger.Printf("outbox: published terminal work=%s state=%s entry=%s attempt=%d/%d",
				work.ID, work.State, e.ID, e.Attempts, e.MaxAttempts)
		}
		return nil
	}
}

// errServerDraining is returned by the handler while the server is
// shutting down. It is a plain error (not a sentinel the caller compares
// against) because the dispatcher's reaction is uniform: release the claim
// and try again later.
var errServerDraining = errors.New("api: server draining, outbox delivery deferred")

// OutboxPublishHandlerForTest is the test-exported alias for the private
// outboxPublishHandler. Test packages in api_test cannot call unexported
// methods, so we expose this thin wrapper — the same precedent as
// PublishTerminal.
func (s *Server) OutboxPublishHandlerForTest() store.OutboxHandler {
	return s.outboxPublishHandler()
}

// StartOutboxDispatcher launches the outbox dispatcher as a background
// goroutine and returns a function that stops it. Intended to be called
// once from cmd/works-api after the store is open.
//
// The dispatcher is safe to run concurrently with request handling: it
// claims rows through the same fenced, single-writer store pool every other
// writer uses, and two live dispatchers can never hold the same row.
//
// A Server with no Publisher still starts a dispatcher: the outbox may
// already hold terminal entries from before a publisher was configured, and
// the handler retires unpublishable Works cleanly rather than wedging.
func (s *Server) StartOutboxDispatcher(ctx context.Context, cfg store.OutboxConfig) func() {
	dispatchCtx, cancel := context.WithCancel(ctx)
	go func() {
		_ = store.RunOutboxDispatcher(dispatchCtx, s.Store, s.outboxPublishHandler(), cfg)
	}()
	return cancel
}