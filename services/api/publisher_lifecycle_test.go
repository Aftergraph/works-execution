package api_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/publisher"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// k-068 tests: publisher lifecycle. The hook must (a) be observable —
// tests can WAIT for publishes deterministically instead of polling,
// (b) refuse new publishes once draining, and (c) drain cleanly.

// testWork returns a fully-provenanced SUCCEEDED work that the hook
// will publish.
func testWork(id string) *workgraph.Work {
	return &workgraph.Work{
		ID:    id,
		State: workgraph.StateSucceeded,
		Source: workgraph.Source{
			Type:       "github_push",
			Repository: "JonasAbde/works-execution",
			SHA:        "abcdef0123456789abcdef0123456789abcdef01",
		},
	}
}

// TestPublisherLifecycle_WaitPublisherProvesPublish shows the new
// test-bar: after WaitPublisher returns, the publish has ACTUALLY
// happened (no polling, no sleeps, no race).
func TestPublisherLifecycle_WaitPublisherProvesPublish(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	pub := publisher.NewNoopPublisher("test")
	srv := &api.Server{Store: s, Publisher: pub}

	srv.PublishTerminal(testWork("wrk_wait1"))
	srv.PublishTerminal(testWork("wrk_wait2"))

	// WaitPublisher blocks until every in-flight publish completed.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.WaitPublisher(ctx)

	if got := len(pub.Snapshot()); got != 2 {
		t.Fatalf("Recorded = %d, want 2 after WaitPublisher (publish provably happened)", got)
	}
}

// TestPublisherLifecycle_DrainingRefusesNew verifies fail-closed: once
// WaitPublisher has been called, terminal transitions no longer start
// publish goroutines — and WaitPublisher is safe to call twice.
func TestPublisherLifecycle_DrainingRefusesNew(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	pub := publisher.NewNoopPublisher("test")
	srv := &api.Server{Store: s, Publisher: pub}

	srv.PublishTerminal(testWork("wrk_pre"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.WaitPublisher(ctx)
	if got := len(pub.Snapshot()); got != 1 {
		t.Fatalf("pre-drain publish lost: Recorded = %d, want 1", got)
	}

	// Server is draining now: these must be REFUSED.
	srv.PublishTerminal(testWork("wrk_during1"))
	srv.PublishTerminal(testWork("wrk_during2"))

	// Second Wait is legal and returns immediately.
	srv.WaitPublisher(ctx)

	if got := len(pub.Snapshot()); got != 1 {
		t.Errorf("Recorded = %d, want 1 (draining server must refuse new publishes)", got)
	}
}

// TestPublisherLifecycle_WaitCtxCancel verifies the ctx escape hatch:
// a hung publish cannot block WaitPublisher forever.
func TestPublisherLifecycle_WaitCtxCancel(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := &api.Server{Store: s, Publisher: publisher.NewNoopPublisher("test")}
	srv.PublishTerminal(testWork("wrk_hang"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	srv.WaitPublisher(ctx)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("WaitPublisher blocked %v past ctx deadline", elapsed)
	}
}
