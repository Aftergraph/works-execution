package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/publisher"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// TestPublisherHook_OnTerminalWork exercises the publisher path
// end-to-end: when a Work reaches SUCCEEDED and has Source
// provenance, the configured publisher is invoked.
func TestPublisherHook_OnTerminalWork(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	pub := publisher.NewNoopPublisher("test")
	srv := &api.Server{Store: s, Publisher: pub}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	// Create a work with full Source provenance.
	body := `{
        "queue": true,
        "source": {
            "type": "github_push",
            "repository": "JonasAbde/works-execution",
            "sha": "abcdef0123456789abcdef0123456789abcdef01"
        },
        "objective": {"type": "verify_change"},
        "graph": {"nodes": {"build": {"id": "build", "run": "echo hi"}}},
        "policy": {"production_access": true}
    }`
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/works: status=%d", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	// Walk the work to SUCCEEDED, calling the hook at each step so
	// we can observe that only the terminal step triggers a publish.
	for _, st := range []workgraph.State{
		workgraph.StateRunning,
		workgraph.StateVerifying,
		workgraph.StateSucceeded,
	} {
		if _, err := s.UpdateState(context.Background(), created.ID, st); err != nil {
			t.Fatalf("UpdateState %s: %v", st, err)
		}
		wk, gerr := s.GetWork(context.Background(), created.ID)
		if gerr != nil {
			t.Fatalf("GetWork: %v", gerr)
		}
		srv.PublishTerminal(wk)
	}

	// NoopPublisher records calls; give the goroutine a moment.
	rec := pub.Snapshot()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
		rec = pub.Snapshot()
	}
	if len(rec) != 1 {
		t.Fatalf("Recorded = %d, want exactly 1 (only SUCCEEDED should publish)", len(rec))
	}
	got := rec[0]
	if got.Repository != "JonasAbde/works-execution" {
		t.Errorf("repo = %q, want JonasAbde/works-execution", got.Repository)
	}
	if got.Conclusion != publisher.ConclusionSuccess {
		t.Errorf("conclusion = %q, want success", got.Conclusion)
	}
	if got.SHA != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("sha = %q", got.SHA)
	}
}

// TestPublisherHook_SkipsNonTerminal verifies that publish does not
// fire for CREATED/QUEUED/RUNNING/VERIFYING works.
func TestPublisherHook_SkipsNonTerminal(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	pub := publisher.NewNoopPublisher("test")
	srv := &api.Server{Store: s, Publisher: pub}

	for _, st := range []workgraph.State{
		workgraph.StateCreated,
		workgraph.StateQueued,
		workgraph.StateRunning,
		workgraph.StateVerifying,
	} {
		srv.PublishTerminal(&workgraph.Work{
			ID:    "wrk_test",
			State: st,
			Source: workgraph.Source{
				Type:       "github_push",
				Repository: "JonasAbde/works-execution",
				SHA:        "abcdef0123456789abcdef0123456789abcdef01",
			},
		})
	}
	if len(pub.Snapshot()) != 0 {
		t.Errorf("publish fired for non-terminal work: %d calls", len(pub.Snapshot()))
	}
}

// TestPublisherHook_SkipsMissingProvenance verifies the safety check:
// no repository or short SHA → no publish (no error).
func TestPublisherHook_SkipsMissingProvenance(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	pub := publisher.NewNoopPublisher("test")
	srv := &api.Server{Store: s, Publisher: pub}

	srv.PublishTerminal(&workgraph.Work{
		ID: "wrk_a", State: workgraph.StateSucceeded,
		Source: workgraph.Source{Type: "cli"},
	})
	srv.PublishTerminal(&workgraph.Work{
		ID: "wrk_b", State: workgraph.StateSucceeded,
		Source: workgraph.Source{Repository: "x/y", SHA: "short"},
	})
	srv.PublishTerminal(&workgraph.Work{
		ID: "wrk_c", State: workgraph.StateSucceeded,
		Source: workgraph.Source{Repository: "x/y", SHA: "abcdef0123456789abcdef0123456789abcdef01"},
	})
	// maybePublishOnTerminal fires a goroutine; give it a beat.
	rec := pub.Snapshot()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec = pub.Snapshot()
		if len(rec) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rec) != 1 {
		t.Errorf("Recorded = %d, want 1 (only fully-provenanced terminal work)", len(rec))
	}
}

// TestPublisherHook_NilPublisher_Noop verifies that a Server without
// a Publisher configured never panics on terminal works.
func TestPublisherHook_NilPublisher_Noop(t *testing.T) {
	srv := &api.Server{Publisher: nil}
	srv.PublishTerminal(&workgraph.Work{
		ID:    "wrk_x",
		State: workgraph.StateSucceeded,
		Source: workgraph.Source{
			Repository: "x/y",
			SHA:        "abcdef0123456789abcdef0123456789abcdef01",
		},
	})
}
