//go:build e2e_durable

package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/missionhandoff"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// TestDurableMissionWorkerReplacement proves the property this feature exists
// for: the submitting coordinator and the first worker are disposable, while
// canonical WORKS state plus reconcile-before-mutate prevents replay.
func TestDurableMissionWorkerReplacement(t *testing.T) {
	if testing.Short() {
		t.Skip("durable recovery test intentionally exercises lease expiry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "works.db")
	artDir := filepath.Join(dir, "artifacts")
	marker := filepath.Join(dir, "side-effect.txt")
	if err := os.MkdirAll(artDir, 0o755); err != nil { t.Fatal(err) }

	st, err := store.Open(dbPath)
	if err != nil { t.Fatal(err) }
	defer st.Close()

	apiSrv := &api.Server{Store: st, ArtifactsDir: artDir}
	ts := httptest.NewServer(apiSrv.Routes())
	defer ts.Close()

	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go func() { _ = api.RunLeaseReaper(rctx, st, api.ReaperConfig{Interval: 250 * time.Millisecond}) }()

	binPath := filepath.Join(dir, "works-worker")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/works-worker")
	build.Dir = projectRootDurable(t)
	if out, err := build.CombinedOutput(); err != nil { t.Fatalf("build worker: %v: %s", err, out) }

	cfg := missionhandoff.Config{
		Version: 1,
		MissionID: "e2e-durable-worker-replacement",
		Objective: "apply an external side effect exactly once despite worker loss",
		PurposeBindings: []string{"e2e-recovery"},
		Budget: missionhandoff.BudgetConfig{WallClockH: 1},
		Verification: []missionhandoff.VerificationConfig{{Criterion: "marker contains exactly one x", Kind: "deterministic"}},
		Stages: map[string]missionhandoff.StageConfig{
			"apply": {
				Reconcile: "test -f \"$MARKER\"",
				Run: "printf x >> \"$MARKER\"; sleep 30",
				Env: map[string]string{"MARKER": marker},
				Permissions: []string{"read", "write", "execute"},
				SideEffects: []string{"filesystem_write"},
				TimeoutS: 45,
			},
		},
	}
	work, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatalf("compile mission: %v", err) }

	body := struct {
		*workgraph.Work
		Queue bool `json:"queue"`
	}{Work: work, Queue: true}
	raw, err := json.Marshal(body)
	if err != nil { t.Fatal(err) }
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(string(raw)))
	if err != nil { t.Fatal(err) }
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body); resp.Body.Close()
		t.Fatalf("create: %s: %s", resp.Status, b)
	}
	var created workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil { t.Fatal(err) }
	resp.Body.Close()

	worker1 := startDurableWorker(t, ctx, binPath, ts.URL, artDir, "wrkr_durable_1")
	waitForMarkerAndRunning(t, ts.URL, created.ID, marker, 10*time.Second)

	if err := worker1.Process.Kill(); err != nil { t.Fatalf("kill worker1: %v", err) }
	_ = worker1.Wait()
	t.Logf("worker1 killed after side effect; waiting for lease expiry")

	waitForCancelledAttempt(t, ts.URL, created.ID, 12*time.Second)
	worker2 := startDurableWorker(t, ctx, binPath, ts.URL, artDir, "wrkr_durable_2")
	defer func() { if worker2.Process != nil { _ = worker2.Process.Kill() }; _ = worker2.Wait() }()

	final := waitForTerminalWork(t, ts.URL, created.ID, 15*time.Second)
	if final.State != workgraph.StateSucceeded {
		t.Fatalf("expected SUCCEEDED after replacement, got %s", final.State)
	}
	if len(final.Attempts) < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", len(final.Attempts))
	}
	got, err := os.ReadFile(marker)
	if err != nil { t.Fatal(err) }
	if string(got) != "x" {
		t.Fatalf("side effect replayed or corrupted: marker=%q", got)
	}
	t.Logf("DURABLE RECOVERY PASS: %s attempts=%d marker=%q", final.ID, len(final.Attempts), got)
}

func startDurableWorker(t *testing.T, ctx context.Context, bin, apiURL, artDir, id string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin,
		"-api", apiURL,
		"-id", id,
		"-artifacts", artDir,
		"-poll", "100ms",
		"-lease-ttl", "4s",
		"-heartbeat", "30s",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil { t.Fatalf("start %s: %v", id, err) }
	return cmd
}

func fetchDurableWork(t *testing.T, apiURL, workID string) workgraph.Work {
	t.Helper()
	resp, err := http.Get(apiURL+"/v1/works/"+workID)
	if err != nil { t.Fatal(err) }
	defer resp.Body.Close()
	var w workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil { t.Fatal(err) }
	return w
}

func waitForMarkerAndRunning(t *testing.T, apiURL, workID, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w := fetchDurableWork(t, apiURL, workID)
		running := false
		for _, a := range w.Attempts { if a.Status == "running" { running = true } }
		if running {
			if _, err := os.Stat(marker); err == nil { return }
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("worker never reached running state after applying marker")
}

func waitForCancelledAttempt(t *testing.T, apiURL, workID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w := fetchDurableWork(t, apiURL, workID)
		for _, a := range w.Attempts { if a.Status == "cancelled" { return } }
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("expired worker lease did not cancel the in-flight attempt")
}

func waitForTerminalWork(t *testing.T, apiURL, workID string, timeout time.Duration) workgraph.Work {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w := fetchDurableWork(t, apiURL, workID)
		if w.State.IsTerminal() { return w }
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("work did not reach terminal state after replacement worker")
	return workgraph.Work{}
}

func projectRootDurable(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil { t.Fatal(err) }
	return filepath.Clean(filepath.Join(wd, ".."))
}
