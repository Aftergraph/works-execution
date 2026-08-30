// Package worker is the local worker daemon. It polls the control plane for
// ready nodes, executes them as subprocesses, and writes evidence back.
//
// V1 worker is intentionally minimal:
//   - Single-threaded execution (one node at a time). Slice 2 adds parallelism.
//   - No leases (worker just claims the node by transitioning state).
//   - No Docker sandboxing — subprocess execution only. Docker comes in slice 2.
//   - No caching — every run is fresh. Fingerprinting + CAS comes in slice 3.
package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// Client is the minimal HTTP client the worker uses to talk to the API.
// It is intentionally narrow so it can be mocked in tests.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// ReadyItem mirrors services/api.readyItem.
type ReadyItem struct {
	WorkID   string            `json:"work_id"`
	NodeID   string            `json:"node_id"`
	Run      string            `json:"run"`
	Env      map[string]string `json:"env,omitempty"`
	TimeoutS int               `json:"timeout_s,omitempty"`
}

// ReadyResponse is the payload from GET /v1/workers/ready.
type ReadyResponse struct {
	Items []ReadyItem `json:"items"`
	Count int         `json:"count"`
}

// Ready polls the API for ready work.
func (c *Client) Ready(ctx context.Context) ([]ReadyItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/workers/ready?limit=10", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ready: %s: %s", resp.Status, body)
	}
	var rr ReadyResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, err
	}
	return rr.Items, nil
}

// NodeResult is what the worker reports back after executing a node.
type NodeResult struct {
	AttemptID string              `json:"attempt_id"`
	NodeID    string              `json:"node_id"`
	Status    string              `json:"status"` // succeeded, failed, timed_out
	ExitCode  int                 `json:"exit_code"`
	Artifact  *workgraph.Artifact `json:"artifact,omitempty"`
	Evidence  []workgraph.Evidence `json:"evidence,omitempty"`
}

// ReportResult records the node result via the store. In slice 1 the worker
// and API share the SQLite file path (WAL mode); in slice 2 this becomes a
// proper /v1/works/{id}/attempts HTTP endpoint with HMAC signing.
func ReportResult(ctx context.Context, s store.Store, workID string, r NodeResult) error {
	attempt := workgraph.Attempt{
		ID:         r.AttemptID,
		NodeID:     r.NodeID,
		Status:     r.Status,
		ExitCode:   r.ExitCode,
		FinishedAt: time.Now().UTC(),
	}
	if r.Status != "succeeded" {
		attempt.Error = fmt.Sprintf("exit %d", r.ExitCode)
	}
	if _, err := s.AppendAttempt(ctx, workID, attempt); err != nil {
		return fmt.Errorf("append attempt: %w", err)
	}
	for _, e := range r.Evidence {
		if _, err := s.AppendEvidence(ctx, workID, e); err != nil {
			return fmt.Errorf("append evidence: %w", err)
		}
	}
	if r.Artifact != nil {
		if _, err := s.AppendArtifact(ctx, workID, *r.Artifact); err != nil {
			return fmt.Errorf("append artifact: %w", err)
		}
	}
	return nil
}

// Logger is the minimal interface the worker uses for logging. *log.Logger
// satisfies it; tests can supply their own implementation.
type Logger interface {
	Printf(format string, args ...any)
}

// Worker is the polling worker daemon.
type Worker struct {
	ID        string
	Client    *Client
	Store     store.Store // for reporting back; nil if not sharing a store
	Artifacts string      // local directory for artifact files
	Logger    Logger
	PollEvery time.Duration
}

// Run polls and executes work forever, or until ctx is done.
func (w *Worker) Run(ctx context.Context) error {
	if w.PollEvery == 0 {
		w.PollEvery = 2 * time.Second
	}
	if w.Artifacts == "" {
		w.Artifacts = filepath.Join(os.TempDir(), "works-artifacts")
	}
	if err := os.MkdirAll(w.Artifacts, 0o755); err != nil {
		return fmt.Errorf("create artifacts dir: %w", err)
	}
	if w.Logger == nil {
		w.Logger = log.Default()
	}

	t := time.NewTicker(w.PollEvery)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := w.tick(ctx); err != nil {
				w.logf("tick error: %v", err)
			}
		}
	}
}

// tick does one polling cycle. Exposed for direct invocation in tests.
func (w *Worker) tick(ctx context.Context) error {
	items, err := w.Client.Ready(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if err := w.execute(ctx, item); err != nil {
			w.logf("execute %s/%s: %v", item.WorkID, item.NodeID, err)
		}
	}
	return nil
}

// execute runs a single node and reports the result.
func (w *Worker) execute(ctx context.Context, item ReadyItem) error {
	if w.Store == nil {
		return errors.New("worker.Store is required for V1 (no HTTP reporting endpoint yet)")
	}

	// Transition QUEUED -> RUNNING. Idempotent.
	if cur, err := w.Store.GetWork(ctx, item.WorkID); err == nil && cur.State == workgraph.StateQueued {
		if _, err := w.Store.UpdateState(ctx, item.WorkID, workgraph.StateRunning); err != nil {
			w.logf("transition to RUNNING: %v", err)
		}
	}

	attemptID := workgraph.NewID("att")

	w.logf("running %s/%s: %s", item.WorkID, item.NodeID, item.Run)

	if _, err := w.Store.AppendAttempt(ctx, item.WorkID, workgraph.Attempt{
		ID:        attemptID,
		NodeID:    item.NodeID,
		WorkerID:  w.ID,
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("append running attempt: %w", err)
	}

	timeout := time.Duration(item.TimeoutS) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	res := runCommand(ctx, item.Run, item.Env, timeout)

	var artifact *workgraph.Artifact
	if res.Status == "succeeded" {
		path, sum, size, err := writeArtifact(w.Artifacts, item.WorkID, item.NodeID, res.CombinedLog)
		if err != nil {
			w.logf("write artifact: %v", err)
		} else {
			artifact = &workgraph.Artifact{
				ID:       sum,
				NodeID:   item.NodeID,
				MimeType: "text/plain",
				Size:     size,
				Path:     path,
			}
		}
	}

	evidence := []workgraph.Evidence{{
		ID:          workgraph.NewID("evd"),
		NodeID:      item.NodeID,
		AttemptID:   attemptID,
		Type:        "build",
		Result:      evidenceResult(res.Status),
		Signer:      w.ID,
		Environment: fmt.Sprintf("worker=%s", w.ID),
		Details: map[string]any{
			"exit_code":   res.ExitCode,
			"duration_ms": res.Duration.Milliseconds(),
			"command":     item.Run,
		},
	}}

	if err := ReportResult(ctx, w.Store, item.WorkID, NodeResult{
		AttemptID: attemptID,
		NodeID:    item.NodeID,
		Status:    res.Status,
		ExitCode:  res.ExitCode,
		Artifact:  artifact,
		Evidence:  evidence,
	}); err != nil {
		return fmt.Errorf("report result: %w", err)
	}

	// Finalize the Work state if all nodes have a successful attempt.
		if cur, err := w.Store.GetWork(ctx, item.WorkID); err == nil {
			allOK := true
			for nodeID := range cur.Graph.Nodes {
				nodeOK := false
				for _, a := range cur.Attempts {
					if a.NodeID == nodeID && a.Status == "succeeded" {
						nodeOK = true
						break
					}
				}
				if !nodeOK {
					allOK = false
					break
				}
			}
			switch {
			case res.Status == "failed":
				if _, err := w.Store.UpdateState(ctx, item.WorkID, workgraph.StateFailed); err != nil {
					w.logf("transition to FAILED: %v", err)
				}
			case allOK && cur.State == workgraph.StateRunning:
				if _, err := w.Store.UpdateState(ctx, item.WorkID, workgraph.StateVerifying); err != nil {
					w.logf("transition to VERIFYING: %v", err)
				}
				if _, err := w.Store.UpdateState(ctx, item.WorkID, workgraph.StateSucceeded); err != nil {
					w.logf("transition to SUCCEEDED: %v", err)
				}
			}
		}

	return nil
}

func (w *Worker) logf(format string, args ...any) {
	if w.Logger != nil {
		w.Logger.Printf("[worker %s] "+format, append([]any{w.ID}, args...)...)
	}
}

// execResult captures the outcome of running one node command.
type execResult struct {
	Status      string
	ExitCode    int
	CombinedLog []byte
	Duration    time.Duration
}

func runCommand(ctx context.Context, command string, env map[string]string, timeout time.Duration) execResult {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	combined := append(stdout.Bytes(), '\n')
	combined = append(combined, stderr.Bytes()...)

	res := execResult{
		CombinedLog: combined,
		Duration:    time.Since(start),
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			res.Status = "timed_out"
			res.ExitCode = -1
			return res
		} else {
			res.ExitCode = -1
		}
		res.Status = "failed"
		return res
	}
	res.Status = "succeeded"
	res.ExitCode = 0
	return res
}

// writeArtifact saves bytes to <dir>/<workID>/<nodeID>.log and returns the
// path, sha256 sum, size. The sum becomes the artifact ID (content-addressed).
func writeArtifact(dir, workID, nodeID string, data []byte) (path, sum string, size int64, err error) {
	d := filepath.Join(dir, workID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", "", 0, err
	}
	path = filepath.Join(d, nodeID+".log")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", "", 0, err
	}
	sum = hex.EncodeToString(sha256.New().Sum(data)[:0]) // bug: sum is appended to input
	_ = sum
	// proper:
	h := sha256.Sum256(data)
	sum = hex.EncodeToString(h[:])
	size = int64(len(data))
	return path, sum, size, nil
}

func evidenceResult(status string) string {
	switch status {
	case "succeeded":
		return "pass"
	case "failed", "timed_out":
		return "fail"
	}
	return "skip"
}