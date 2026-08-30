//go:build e2e

// Package e2e_test exercises the full stack end-to-end through HTTP only
// (slice 2). The slice-1 e2e test shared a store between API and worker
// in-process; slice 2 forces all worker→API traffic through HTTP so the
// lease protocol is actually exercised.
package e2e_test

import (
	"context"
	"encoding/json"
	"io"
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "e2e.db")
	artDir := filepath.Join(dbDir, "artifacts")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	apiSrv := &api.Server{Store: st, ArtifactsDir: artDir}
	ts := httptest.NewServer(apiSrv.Routes())
	defer ts.Close()

	// Reaper goroutine.
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go func() {
		_ = api.RunLeaseReaper(rctx, st, api.ReaperConfig{Interval: 500 * time.Millisecond})
	}()

	// Start the worker via HTTP.
	w := &worker.Worker{
		ID:              "wrkr_e2e",
		Client:          &worker.Client{BaseURL: ts.URL, HTTP: http.DefaultClient},
		ArtifactsDir:    artDir,
		Logger:          testLogger{t: t},
		PollEvery:       200 * time.Millisecond,
		LeaseTTL:        5 * time.Second,
		HeartbeatEvery:  2 * time.Second,
	}
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	go func() { _ = w.Run(wctx) }()

	// Submit a 2-node DAG with queue:true.
	body := `{
        "queue": true,
        "source": {"type": "cli", "repository": "acme/demo"},
        "objective": {"type": "verify_change"},
        "graph": {
            "nodes": {
                "hello":  {"id": "hello",  "run": "echo 'hello from works-execution' && uname -a"},
                "verify": {"id": "verify", "run": "echo verified-ok", "needs": ["hello"]}
            }
        },
        "requirements": {"os": "linux"},
        "policy": {}
    }`
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("create: %d: %s", resp.StatusCode, string(buf))
	}
	var created workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.State != workgraph.StateQueued {
		t.Fatalf("expected QUEUED, got %s", created.State)
	}
	t.Logf("submitted %s", created.ID)

	// Poll for terminal state.
	deadline := time.Now().Add(15 * time.Second)
	var final workgraph.Work
	for time.Now().Before(deadline) {
		r, err := http.Get(ts.URL + "/v1/works/" + created.ID)
		if err != nil {
			t.Fatal(err)
		}
		var w workgraph.Work
		_ = json.NewDecoder(r.Body).Decode(&w)
		r.Body.Close()
		if w.State.IsTerminal() {
			final = w
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if final.ID == "" {
		t.Fatal("work did not reach terminal state")
	}
	if final.State != workgraph.StateSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s (attempts=%d, evidence=%d, artifacts=%d)",
			final.State, len(final.Attempts), len(final.Evidence), len(final.Artifacts))
	}
	if len(final.Attempts) < 2 {
		t.Errorf("expected >=2 attempts, got %d", len(final.Attempts))
	}
	if len(final.Artifacts) < 2 {
		t.Errorf("expected >=2 artifacts, got %d", len(final.Artifacts))
	}
	// Verify log streaming works.
	logs, err := http.Get(ts.URL + "/v1/works/" + created.ID + "/nodes/hello/logs")
	if err != nil {
		t.Fatal(err)
	}
	if logs.StatusCode != http.StatusOK {
		t.Errorf("logs: status %d", logs.StatusCode)
	}
	body2, _ := io.ReadAll(logs.Body)
	logs.Body.Close()
	if !strings.Contains(string(body2), "hello from works-execution") {
		t.Errorf("logs missing expected content; got: %q", string(body2))
	}
	t.Logf("E2E SUCCESS: %s -> %s, %d attempts, %d artifacts; logs streamed",
		final.ID, final.State, len(final.Attempts), len(final.Artifacts))
}

type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...any) { l.t.Logf(format, args...) }