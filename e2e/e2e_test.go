//go:build e2e

// Package e2e_test exercises the full stack end-to-end:
//
//  1. Spin up the API server backed by a temp SQLite database.
//  2. Submit a work via the public HTTP API.
//  3. Queue it.
//  4. Run the worker in-process.
//  5. Assert the work reaches SUCCEEDED with evidence and an artifact on disk.
//
// This is the "first proof" required by docs/works-venture-starter-pack/00_START_HERE/FOUNDER_DIRECTIVE_001.md.
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/worker"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func TestE2E_WorkSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "e2e.db")
	artDir := filepath.Join(dbDir, "artifacts")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	apiSrv := &api.Server{Store: st}
	ts := httptest.NewServer(apiSrv.Routes())
	defer ts.Close()

	// 1. Submit a work via the public API with auto-queue.
	body := `{
        "queue": true,
        "source": {"type": "cli", "repository": "acme/demo", "revision": "head"},
        "objective": {"type": "verify_change"},
        "graph": {
            "nodes": {
                "hello": {"id": "hello", "run": "echo 'hello from works-execution' && uname -a"},
                "verify": {"id": "verify", "run": "test -n \"$(echo verified)\" && echo verified-ok", "needs": ["hello"]}
            }
        },
        "requirements": {"os": "linux", "arch": "amd64", "confidence": "development"},
        "policy": {"fork_policy": "deny", "trust_class": "standard"}
    }`
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/works: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		buf := make([]byte, 1024)
		n, _ := resp.Body.Read(buf)
		t.Fatalf("create: status %d: %s", resp.StatusCode, string(buf[:n]))
	}
	var created workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if created.State != workgraph.StateQueued {
		t.Fatalf("expected state QUEUED after create-with-queue, got %s", created.State)
	}
	t.Logf("submitted work %s", created.ID)

	// 2. Run the worker for up to 10 seconds (poll every 250ms).
	w := &worker.Worker{
		ID:        "wrkr_e2e",
		Client:    &worker.Client{BaseURL: ts.URL, HTTP: http.DefaultClient},
		Store:     st,
		Artifacts: artDir,
		Logger:    testLogger{t: t},
		PollEvery: 250 * time.Millisecond,
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	done := make(chan struct{})
	go func() {
		_ = w.Run(runCtx)
		close(done)
	}()

	// 3. Poll for terminal state via the API.
	deadline := time.Now().Add(15 * time.Second)
	var final workgraph.Work
	for time.Now().Before(deadline) {
		r, err := http.Get(ts.URL + "/v1/works/" + created.ID)
		if err != nil {
			t.Fatalf("GET work: %v", err)
		}
		var w workgraph.Work
		_ = json.NewDecoder(r.Body).Decode(&w)
		r.Body.Close()
		if w.State.IsTerminal() {
			final = w
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	runCancel()
	<-done

	if final.ID == "" {
		t.Fatal("work did not reach terminal state")
	}

	// 4. Assertions.
	if final.State != workgraph.StateSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s (attempts=%d, evidence=%d)",
			final.State, len(final.Attempts), len(final.Evidence))
	}
	if len(final.Attempts) < 2 {
		t.Errorf("expected at least 2 attempts (one per node), got %d", len(final.Attempts))
	}
	if len(final.Evidence) < 2 {
		t.Errorf("expected at least 2 evidence records (one per node), got %d", len(final.Evidence))
	}
	if len(final.Artifacts) < 2 {
		t.Errorf("expected at least 2 artifacts (one per node), got %d", len(final.Artifacts))
	}

	// Verify each artifact exists on disk and is non-empty.
	for _, a := range final.Artifacts {
		info, err := os.Stat(a.Path)
		if err != nil {
			t.Errorf("artifact %s missing on disk: %v", a.Path, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("artifact %s is empty", a.Path)
		}
		if a.Size != info.Size() {
			t.Errorf("artifact %s: stored size %d != on-disk size %d", a.Path, a.Size, info.Size())
		}
	}

	// Verify both nodes ran and succeeded.
	seen := map[string]bool{}
	for _, a := range final.Attempts {
		if a.Status != "succeeded" {
			t.Errorf("attempt %s status=%s, want succeeded", a.NodeID, a.Status)
		}
		seen[a.NodeID] = true
	}
	for _, want := range []string{"hello", "verify"} {
		if !seen[want] {
			t.Errorf("node %q did not run", want)
		}
	}

	t.Logf("E2E SUCCESS: %s -> %s, %d attempts, %d evidence, %d artifacts",
		final.ID, final.State, len(final.Attempts), len(final.Evidence), len(final.Artifacts))
}

// testLogger forwards to t.Logf so worker messages surface in the test output.
type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...any) {
	l.t.Logf(format, args...)
}