//go:build e2e_chaos

// Package e2e_chaos_test exercises the worker-loss recovery path (slice 2,
// RFC-0001 §"Chaos test"). It is gated by the `e2e_chaos` build tag because
// it kills -9 a worker process and waits up to ~35s; it is not part of the
// default `go test ./...` run.
//
// Run with:  go test -tags=e2e_chaos ./e2e/ -v -run TestChaos
package e2e_chaos_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// TestChaos_KillWorkerMidNode asserts that the lease-reaper detects a lost
// worker within the slice-2 SLO bound (TTL + reaper interval = 25s + 5s = 30s,
// with a 10s slack = 40s hard cap).
func TestChaos_KillWorkerMidNode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping chaos test in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "chaos.db")
	artDir := filepath.Join(dbDir, "artifacts")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	apiSrv := &api.Server{Store: st, ArtifactsDir: artDir}
	ts := httptest.NewServer(apiSrv.Routes())
	defer ts.Close()

	// Aggressive reaper for the test: every 1s.
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go func() {
		_ = api.RunLeaseReaper(rctx, st, api.ReaperConfig{Interval: time.Second})
	}()

	// Build the works-worker binary on the fly from the current source.
	binPath := filepath.Join(dbDir, "works-worker")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/works-worker")
	build.Dir = projectRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v: %s", err, string(out))
	}

	// Launch the worker as a child process. Short lease TTL so we don't wait
	// long; no heartbeats needed since we'll kill it.
	workerCmd := exec.CommandContext(ctx, binPath,
		"-api", ts.URL,
		"-id", "wrkr_chaos",
		"-artifacts", artDir,
		"-poll", "200ms",
		"-lease-ttl", "15s",
		"-heartbeat", "20s", // heartbeats never fire before kill
	)
	workerCmd.Stdout = os.Stdout
	workerCmd.Stderr = os.Stderr
	if err := workerCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if workerCmd.Process != nil {
			_ = workerCmd.Process.Kill()
		}
	}()

	// Submit a work whose node command sleeps for 60s — long enough that
	// the worker is mid-execution when we kill it.
	body := `{
        "queue": true,
        "source": {"type": "cli"},
        "objective": {"type": "verify_change"},
        "graph": {
            "nodes": {
                "long": {"id": "long", "run": "sleep 60 && echo too-late", "timeout_s": 120}
            }
        },
        "requirements": {"os": "linux"},
        "policy": {}
    }`
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	t.Logf("submitted %s (node will sleep 60s)", created.ID)

	// Wait for the worker to acquire a lease (poll /v1/leases? — we just
	// observe that an attempt status flips to 'running').
	deadline := time.Now().Add(10 * time.Second)
	var sawRunning bool
	for time.Now().Before(deadline) {
		r, err := http.Get(ts.URL + "/v1/works/" + created.ID)
		if err != nil {
			t.Fatal(err)
		}
		var w workgraph.Work
		_ = json.NewDecoder(r.Body).Decode(&w)
		r.Body.Close()
		for _, a := range w.Attempts {
			if a.Status == "running" {
				sawRunning = true
				break
			}
		}
		if sawRunning {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !sawRunning {
		t.Fatal("worker never reached running state")
	}

	// Kill -9 the worker.
	t.Logf("kill -9 worker pid=%d", workerCmd.Process.Pid)
	if err := workerCmd.Process.Kill(); err != nil {
		t.Fatalf("kill worker: %v", err)
	}
	killTime := time.Now()
	_ = workerCmd.Wait() // reap zombie

	// The reaper (1s interval) should detect the lease (15s TTL) within
	// ~16s. Allow up to 40s for slow CI.
	expireDeadline := killTime.Add(40 * time.Second)
	var leaseExpired bool
	for time.Now().Before(expireDeadline) {
		// Check work state — once the attempt is 'cancelled' the node is
		// ready again and a fresh attempt can be granted.
		r, err := http.Get(ts.URL + "/v1/works/" + created.ID)
		if err != nil {
			t.Fatal(err)
		}
		var w workgraph.Work
		_ = json.NewDecoder(r.Body).Decode(&w)
		r.Body.Close()
		for _, a := range w.Attempts {
			if a.Status == "cancelled" {
				leaseExpired = true
				t.Logf("attempt %s cancelled %v after kill", a.ID, time.Since(killTime))
				break
			}
		}
		if leaseExpired {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !leaseExpired {
			t.Fatalf("lease never expired within 40s; check API output above")
		}
	t.Logf("CHAOS SUCCESS: lease detected lost + attempt cancelled within %v", time.Since(killTime))
}

// projectRoot finds the repository root by walking up from the test file.
func projectRoot(t *testing.T) string {
	t.Helper()
	// e2e package lives at <root>/e2e. Parent is the repo root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, ".."))
}