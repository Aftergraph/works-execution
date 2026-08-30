// Package worker is the local worker daemon. It polls the control plane for
// ready nodes, acquires a lease, executes the node as a subprocess, and
// reports the result back via the lease.
//
// Slice 2 changes vs slice 1:
//   - Lease-based claiming (POST /v1/leases/grant).
//   - Periodic heartbeat to keep the lease alive while the node runs.
//   - Subprocess killed on lease loss (revoke or heartbeat 409).
//   - Results reported via /complete and /release, not via direct store
//     writes (control plane owns state per ADR-0002).
//   - All worker→API transport is HTTP. Slice 1 used shared SQLite.
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
	"sync"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Client is the minimal HTTP client the worker uses to talk to the API.
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

// GrantLease requests a lease for the given (work_id, node_id, worker_id).
// Returns the lease ID + attempt ID.
func (c *Client) GrantLease(ctx context.Context, workID, nodeID, workerID string, ttl time.Duration) (leaseID, attemptID string, err error) {
	body, _ := json.Marshal(map[string]any{
		"work_id":     workID,
		"node_id":     nodeID,
		"worker_id":   workerID,
		"ttl_seconds": int(ttl.Seconds()),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/leases", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("grant: %s: %s", resp.Status, body)
	}
	var out struct {
		Lease   workgraph.Lease   `json:"lease"`
		Attempt workgraph.Attempt `json:"attempt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	return out.Lease.ID, out.Attempt.ID, nil
}

// Heartbeat extends the lease TTL.
func (c *Client) Heartbeat(ctx context.Context, leaseID string, ttl time.Duration) error {
	body, _ := json.Marshal(map[string]any{"ttl_seconds": int(ttl.Seconds())})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/leases/"+leaseID+"/heartbeat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("heartbeat: %s: %s", resp.Status, body)
	}
	return nil
}

// CompleteLease reports a terminal result.
func (c *Client) CompleteLease(ctx context.Context, leaseID string, exitCode int, artifact *workgraph.Artifact, evidence []workgraph.Evidence) error {
	body, _ := json.Marshal(map[string]any{
		"exit_code": exitCode,
		"artifact":  artifact,
		"evidence":  evidence,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/leases/"+leaseID+"/complete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("complete: %s: %s", resp.Status, body)
	}
	return nil
}

// ReleaseLease voluntarily gives the lease back (e.g. setup error before run).
func (c *Client) ReleaseLease(ctx context.Context, leaseID, reason string) error {
	body, _ := json.Marshal(map[string]any{"reason": reason})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/leases/"+leaseID+"/release", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("release: %s: %s", resp.Status, body)
	}
	return nil
}

// Logger is the minimal interface the worker uses for logging.
type Logger interface {
	Printf(format string, args ...any)
}

// Worker is the polling worker daemon.
type Worker struct {
	ID            string
	Client        *Client
	ArtifactsDir  string
	Logger        Logger
	PollEvery     time.Duration
	LeaseTTL      time.Duration // per-lease TTL on grant; default 25s
	HeartbeatEvery time.Duration // heartbeat interval; default 10s
}

// Run polls and executes work forever, or until ctx is done.
func (w *Worker) Run(ctx context.Context) error {
	if w.PollEvery == 0 {
		w.PollEvery = 2 * time.Second
	}
	if w.LeaseTTL == 0 {
		w.LeaseTTL = 25 * time.Second
	}
	if w.HeartbeatEvery == 0 {
		w.HeartbeatEvery = 10 * time.Second
	}
	if w.ArtifactsDir == "" {
		w.ArtifactsDir = filepath.Join(os.TempDir(), "works-artifacts")
	}
	if err := os.MkdirAll(w.ArtifactsDir, 0o755); err != nil {
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

// tick does one polling cycle.
func (w *Worker) tick(ctx context.Context) error {
	items, err := w.Client.Ready(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := w.execute(ctx, item); err != nil {
			w.logf("execute %s/%s: %v", item.WorkID, item.NodeID, err)
		}
	}
	return nil
}

// execute grants a lease, runs the node, reports the result.
func (w *Worker) execute(ctx context.Context, item ReadyItem) error {
	leaseID, _, err := w.Client.GrantLease(ctx, item.WorkID, item.NodeID, w.ID, w.LeaseTTL)
	if err != nil {
		// Conflict or other failure — skip silently. This is normal under
		// contention.
		return nil
	}

	// Heartbeat goroutine: extends the lease until execution finishes or
	// the lease is lost (heartbeat returns 409). If heartbeat fails, we
	// cancel the subprocess via the killCh.
	killCh := make(chan struct{})
	hbCtx, cancelHB := context.WithCancel(ctx)
	defer cancelHB()
	var hbErr error
	var hbMu sync.Mutex
	go w.heartbeatLoop(hbCtx, leaseID, killCh, &hbErr, &hbMu)

	timeout := time.Duration(item.TimeoutS) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}

	w.logf("running %s/%s (lease=%s): %s", item.WorkID, item.NodeID, leaseID, item.Run)
	res := runCommand(ctx, item.Run, item.Env, timeout, killCh)

	cancelHB() // stop heartbeats

	// If the heartbeat reported lease loss, the attempt is already cancelled
	// by the reaper or by RevokeLease. We still POST /complete but expect
	// 409 (lease not active). That's fine — the system has already moved on.
	var artifact *workgraph.Artifact
	if res.Status == "succeeded" {
		path, sum, size, err := writeArtifact(w.ArtifactsDir, item.WorkID, item.NodeID, res.CombinedLog)
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
		Type:        "build",
		Result:      evidenceResult(res.Status),
		Signer:      w.ID,
		Environment: fmt.Sprintf("worker=%s", w.ID),
		Details: map[string]any{
			"exit_code":   res.ExitCode,
			"duration_ms": res.Duration.Milliseconds(),
			"command":     item.Run,
			"lease_lost":  res.LeaseLost,
		},
	}}
	if err := w.Client.CompleteLease(ctx, leaseID, res.ExitCode, artifact, evidence); err != nil {
		w.logf("complete lease: %v", err)
		// Fall back to release so the attempt isn't stuck running.
		_ = w.Client.ReleaseLease(ctx, leaseID, "complete failed: "+err.Error())
	}
	return nil
}

// heartbeatLoop POSTs /heartbeat every HeartbeatEvery. If a heartbeat
// fails (likely 409 = lease lost), it signals killCh so the subprocess
// is killed.
func (w *Worker) heartbeatLoop(ctx context.Context, leaseID string, killCh chan<- struct{}, hbErr *error, mu *sync.Mutex) {
	t := time.NewTicker(w.HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Client.Heartbeat(ctx, leaseID, w.LeaseTTL); err != nil {
				mu.Lock()
				*hbErr = err
				mu.Unlock()
				close(killCh)
				return
			}
		}
	}
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
	LeaseLost   bool // true if heartbeat detected lease loss and killed the subprocess
}

// runCommand runs the command and returns when:
//   - the command exits naturally (succeeded / failed)
//   - the context is cancelled
//   - killCh is closed (lease lost — kill the subprocess)
//
// When killCh fires, the subprocess is killed and LeaseLost=true is set.
func runCommand(ctx context.Context, command string, env map[string]string, timeout time.Duration, killCh <-chan struct{}) execResult {
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

	// Watch for lease loss and kill the subprocess.
	leaseLost := false
	if killCh != nil {
		doneCh := make(chan struct{})
		go func() {
			select {
			case <-killCh:
				leaseLost = true
				_ = cmd.Process.Kill()
			case <-doneCh:
			}
		}()
		defer close(doneCh)
	}

	err := cmd.Run()
	combined := append(stdout.Bytes(), '\n')
	combined = append(combined, stderr.Bytes()...)

	res := execResult{
		CombinedLog: combined,
		Duration:    time.Since(start),
		LeaseLost:   leaseLost,
	}
	if leaseLost {
		res.Status = "cancelled"
		res.ExitCode = -1
		return res
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
// path, sha256 sum, and size. The sum is the artifact ID (content-addressed).
func writeArtifact(dir, workID, nodeID string, data []byte) (path, sum string, size int64, err error) {
	d := filepath.Join(dir, workID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", "", 0, err
	}
	path = filepath.Join(d, nodeID+".log")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", "", 0, err
	}
	h := sha256.Sum256(data)
	sum = hex.EncodeToString(h[:])
	size = int64(len(data))
	return path, sum, size, nil
}

func evidenceResult(status string) string {
	switch status {
	case "succeeded":
		return "pass"
	case "failed", "timed_out", "cancelled":
		return "fail"
	}
	return "skip"
}