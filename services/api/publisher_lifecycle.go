package api

import (
	"context"
)

// Publisher lifecycle (k-068): the publish-on-terminal hook fires
// Publisher.Publish in background goroutines (fire-and-forget so the
// HTTP response is never delayed by GitHub). This file gives those
// goroutines a lifecycle so they cannot outlive the server:
//
//   - publisherShutdown is set when WaitPublisher is called; once set,
//     no NEW publish goroutines are started (fail-closed — we would
//     rather drop a status update than stack goroutines forever during
//     shutdown).
//   - publisherWG tracks in-flight publish goroutines so process exit
//     can wait for them instead of cutting a GitHub call mid-flight.
//
// Both fields are zero-value safe: a Server literal that never calls
// WaitPublisher behaves exactly as before (unbounded fire-and-forget).
//
// Ordering law: the hook checks the shutdown flag BEFORE Add() and the
// waiter sets the flag BEFORE Wait() — the reverse lets Add race a
// concurrent Wait (the classic WaitGroup misuse).

// WaitPublisher blocks until every in-flight publish goroutine has
// completed, or until ctx is done. It first flips the shutdown gate so
// no new publishes are accepted while draining. Call this from the
// process shutdown path AFTER the HTTP server has stopped accepting
// requests (no new state transitions can arrive then).
//
// Safe to call multiple times and on servers with no Publisher.
func (s *Server) WaitPublisher(ctx context.Context) {
	s.publisherShutdown.Store(true)
	done := make(chan struct{})
	go func() {
		s.publisherWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// publisherShutdownGuard returns true when publishes are no longer
// accepted (server is draining). Used by the hook before Add().
func (s *Server) publisherShutdownGuard() bool {
	return s.publisherShutdown.Load()
}
