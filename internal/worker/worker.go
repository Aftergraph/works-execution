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
	"runtime"
	"sync"
	"time"

	"github.com/JonasAbde/works-execution/internal/sandbox"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/source"
)

// Client is the minimal HTTP client the worker uses to talk to the API.
//
// Slice 4 (k-impl-003) added Zero-Secret enrollment: workers obtain a
// short-lived HS256 JWT from POST /v1/workers/enroll and present it as
// `Authorization: Bearer <token>` on every subsequent request. The
// Client stores the token in the `Token` field; if it is empty, the
// client transparently falls back to the pre-slice-4 behavior of
// sending no Authorization header. That keeps unit tests of the client
// itself trivial while production callers (cmd/works-worker) always
// enroll first and pass the resulting token in.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Token is the bearer credential for /v1/leases/* and /v1/workers/*.
	// Empty means "no token"; the client will not set an Authorization
	// header in that case.
	Token string

	// --- Token renewal (production hardening, 2026-08-31) ---
	// Enrollment JWTs are short-lived (default 1h). When they expire,
	// every request 401s with token_expired. Instead of crashing or
	// spinning, the client transparently re-enrolls once per request
	// when all renewal fields are set. cmd/works-worker sets them from
	// flags; cmd/works CLI sets them from --enroll-secret.
	WorkerID     string
	EnrollSecret string
	EnrollTTL    time.Duration
	// renewMu serializes re-enrollment attempts so a burst of 401s
	// mints one token, not one per request.
	renewMu sync.Mutex
}

// do executes an HTTP request. When c.Token is set, it attaches the
// Bearer credential. On a 401 response with renewal configured, it
// re-enrolls once and retries (token renewal loop). Centralizing this
// here means every new request method automatically inherits auth AND
// renewal.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	// Attach the current token (if any) before the first attempt.
	if c.Token != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || c.EnrollSecret == "" {
		return resp, nil
	}
	// Drain + close the 401 body before retrying.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if !c.renewToken(req.Context()) {
		// Re-enrollment failed; return the original 401 so the caller
		// logs it and the tick loop keeps trying.
		return c.HTTP.Do(req)
	}
	// Re-send with the fresh token.
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+c.Token)
	if req.Body != nil {
		// Body was consumed by the first attempt; replay it.
		if err := req2.Body.Close(); err == nil {
		}
		// For retry we rely on GetBody (set by http.NewRequest for
		// buffer-backed bodies).
		if req.GetBody != nil {
			b, gerr := req.GetBody()
			if gerr == nil {
				req2.Body = b
			}
		}
	}
	return c.HTTP.Do(req2)
}

// renewToken re-enrolls and stores the fresh token. Returns false when
// renewal is impossible (no secret) or failed (server error). Serialized
// so concurrent 401s share one enrollment round-trip.
func (c *Client) renewToken(ctx context.Context) bool {
	c.renewMu.Lock()
	defer c.renewMu.Unlock()
	if c.EnrollSecret == "" || c.WorkerID == "" {
		return false
	}
	tok, err := c.Enroll(ctx, c.WorkerID, c.EnrollSecret, c.EnrollTTL)
	if err != nil {
		return false
	}
	c.Token = tok
	return true
}

// ReadyItem mirrors services/api.readyItem.
type ReadyItem struct {
	WorkID   string            `json:"work_id"`
	NodeID   string            `json:"node_id"`
	Run      string            `json:"run"`
	Env      map[string]string `json:"env,omitempty"`
	TimeoutS int               `json:"timeout_s,omitempty"`
	Image    string            `json:"image,omitempty"` // slice 5: docker image; empty = host subprocess
	Source   *workgraph.Source `json:"source,omitempty"`
	// CacheKey (RFC-0005): non-empty when the node's inputs are
	// cache-enabled and the scheduler computed a fingerprint. The
	// worker may claim a prior identical result via CacheLookup.
	CacheKey string `json:"cache_key,omitempty"`
}

// ReadyResponse is the payload from GET /v1/workers/ready.
type ReadyResponse struct {
	Items []ReadyItem `json:"items"`
	Count int         `json:"count"`
}

// Enroll requests a short-lived JWT from the API. The returned token is
// valid for the routes under /v1/workers/* and /v1/leases/* for the
// duration of ttl. Set Client.Token = <returned token> to use it.
//
// On any non-2xx response, returns an error with the status and body
// so the caller can log/quit.
func (c *Client) Enroll(ctx context.Context, workerID, challenge string, ttl time.Duration) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"worker_id":   workerID,
		"challenge":   challenge,
		"ttl_seconds": int(ttl.Seconds()),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/workers/enroll", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("enroll: %s: %s", resp.Status, b)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("enroll: empty token in response")
	}
	return out.Token, nil
}

// RegisterRunner advertises this worker as a scheduler-visible runner
// (BYOC, RFC-0004). Registration is idempotent on runner_id: the API
// returns the stored record unchanged for repeat calls, and a
// successful call refreshes LastHeartbeatAt, so the worker can reuse
// it as its heartbeat. caps are the runner capabilities advertised to
// the scheduler (os/arch/labels — labels carry pool membership).
func (c *Client) RegisterRunner(ctx context.Context, runnerID, trustClass string, caps map[string]any) error {
	if caps == nil {
		caps = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{
		"runner_id":       runnerID,
		"trust_class":     trustClass,
		"lifecycle_state": "active",
		"capabilities":    caps,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/runners/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register runner: %s: %s", resp.Status, b)
	}
	return nil
}

// cacheEntry mirrors services/api.cacheEntryBody.
type cacheEntry struct {
	WorkID   string `json:"work_id"`
	NodeID   string `json:"node_id"`
	ExitCode int    `json:"exit_code"`
	LogTail  string `json:"log_tail,omitempty"`
}

// CacheLookup claims a cached result for a fingerprint. Returns
// os.ErrNotExist when the store has no entry (miss); any other error
// is a transport/server problem and callers should fall through to
// real execution.
func (c *Client) CacheLookup(ctx context.Context, fingerprint string) (*cacheEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/cache/"+fingerprint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, os.ErrNotExist
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cache lookup: %s: %s", resp.Status, b)
	}
	var e cacheEntry
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return nil, err
	}
	return &e, nil
}

// CachePut stores a successful result under its fingerprint. Failures
// are non-fatal for the caller (the run itself already succeeded).
func (c *Client) CachePut(ctx context.Context, fingerprint, workID, nodeID string, log []byte) error {
	body, _ := json.Marshal(map[string]any{
		"work_id":   workID,
		"node_id":   nodeID,
		"exit_code": 0,
		"log_tail":  truncateLogTail(log),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/v1/cache/"+fingerprint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cache put: %s: %s", resp.Status, b)
	}
	return nil
}

// cacheLogTailMax mirrors packages/cache.LogTailMax.
const cacheLogTailMax = 4096

func truncateLogTail(b []byte) string {
	if len(b) <= cacheLogTailMax {
		return string(b)
	}
	return string(b[len(b)-cacheLogTailMax:])
}

// Ready polls the API for ready work.
func (c *Client) Ready(ctx context.Context) ([]ReadyItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/workers/ready?limit=10", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	resp, err := c.do(req)
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
	ID             string
	Client         *Client
	ArtifactsDir   string
	Logger         Logger
	PollEvery      time.Duration
	LeaseTTL       time.Duration // per-lease TTL on grant; default 25s
	HeartbeatEvery time.Duration // heartbeat interval; default 10s
	// GitHubToken is held only in the worker process and is passed to the
	// per-work source checkout. It is never added to the node environment.
	GitHubToken string

	// RunnerIdentity, when non-nil, is registered with the control
	// plane at startup and re-asserted every HeartbeatEvery (BYOC,
	// RFC-0004). Registration makes the worker scheduler-visible so
	// pool routing, capability filters, and heartbeats apply. Nil =
	// legacy behavior (worker polls without a scheduler identity).
	RunnerIdentity *RunnerSpec

	registered bool
}

// RunnerSpec is the runner identity a worker advertises at
// registration. TrustClass defaults to "standard" when empty; Labels
// must include "pool:<name>" for the runner to join a BYOC pool.
type RunnerSpec struct {
	TrustClass string
	Labels     []string
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

	// BYOC (RFC-0004): register as a scheduler-visible runner and
	// re-assert every heartbeat. Registration is idempotent so the
	// same call doubles as the heartbeat (it refreshes
	// LastHeartbeatAt server-side). A failed first registration is
	// logged and retried on the next tick — the worker keeps
	// polling regardless so a broken registry never blocks work.
	if w.RunnerIdentity != nil {
		w.registerRunner(ctx)
	}

	t := time.NewTicker(w.PollEvery)
	defer t.Stop()
	beat := time.NewTicker(w.HeartbeatEvery)
	defer beat.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-beat.C:
			if w.RunnerIdentity != nil {
				// Heartbeat = re-register (idempotent upsert).
				w.registerRunner(ctx)
			}
		case <-t.C:
			if err := w.tick(ctx); err != nil {
				w.logf("tick error: %v", err)
			}
		}
	}
}

// registerRunner performs (or refreshes) this worker's scheduler
// registration. Caps are derived from the host + spec: OS/arch from
// runtime, pool labels from RunnerSpec.
func (w *Worker) registerRunner(ctx context.Context) {
	trust := w.RunnerIdentity.TrustClass
	if trust == "" {
		trust = "standard"
	}
	caps := map[string]any{
		"os":   []string{runtime.GOOS},
		"arch": []string{runtime.GOARCH},
	}
	if len(w.RunnerIdentity.Labels) > 0 {
		caps["labels"] = w.RunnerIdentity.Labels
	}
	if err := w.Client.RegisterRunner(ctx, w.ID, trust, caps); err != nil {
		if !w.registered {
			w.logf("runner registration pending: %v (retrying every heartbeat)", err)
		}
		return
	}
	if !w.registered {
		w.registered = true
		w.logf("runner registered: id=%s trust=%s labels=%v", w.ID, trust, w.RunnerIdentity.Labels)
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

	cacheHit := false
	var res execResult
	if item.CacheKey != "" {
		if e, err := w.Client.CacheLookup(ctx, item.CacheKey); err == nil && e.ExitCode == 0 {
			cacheHit = true
			res = execResult{
				Status:      "succeeded",
				ExitCode:    0,
				CombinedLog: []byte("[cache hit " + item.CacheKey[:min(12, len(item.CacheKey))] + "] replayed from prior identical run:\n" + e.LogTail),
				Duration:    0,
			}
			w.logf("cache HIT %s/%s (lease=%s): replayed result", item.WorkID, item.NodeID, leaseID)
		}
	}

	if !cacheHit {
		executionStarted := time.Now()
		var checkedOut *source.Source
		var sourceDir string
		if item.Source != nil && item.Source.CloneURL != "" && item.Source.SHA != "" {
			checkedOut, err = source.Checkout(ctx, source.Options{
				RepoURL: item.Source.CloneURL,
				Ref:     item.Source.Ref,
				SHA:     item.Source.SHA,
				Token:   w.GitHubToken,
			})
			if err != nil {
				res = execResult{
					Status:      "failed",
					ExitCode:    -1,
					CombinedLog: []byte("source checkout failed: " + err.Error()),
					Duration:    time.Since(executionStarted),
				}
			} else {
				sourceDir = checkedOut.WorkDir
				defer func() { _ = checkedOut.Cleanup() }()
			}
		}
		if res.Status == "" {
			w.logf("running %s/%s (lease=%s): %s", item.WorkID, item.NodeID, leaseID, item.Run)
			if item.Image != "" {
				res = runDocker(ctx, item.Image, item.Run, item.Env, timeout, killCh)
			} else {
				res = runCommand(ctx, item.Run, item.Env, timeout, killCh, sourceDir, nil)
			}
		}
	}

	cancelHB() // stop heartbeats

	// RFC-0005: publish successful, really-executed results to the
	// cache so the next byte-identical node can skip execution.
	// Cache-replayed results are NOT re-published (they add no new
	// information and would mask the original creator).
	if !cacheHit && res.Status == "succeeded" && item.CacheKey != "" {
		if err := w.Client.CachePut(ctx, item.CacheKey, item.WorkID, item.NodeID, res.CombinedLog); err != nil {
			w.logf("cache put: %v", err) // non-fatal
		}
	}

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
	// Failed nodes persist a log tail in evidence Details so CI
	// failures are diagnosable from the API/CLI without SSHing to
	// the worker. (Successful nodes store the full log as artifact.)
	evidenceDetails := map[string]any{
		"exit_code":   res.ExitCode,
		"duration_ms": res.Duration.Milliseconds(),
		"command":     item.Run,
		"lease_lost":  res.LeaseLost,
	}
	if res.Status != "succeeded" && len(res.CombinedLog) > 0 {
		tail := string(res.CombinedLog)
		if len(tail) > 2048 {
			tail = tail[len(tail)-2048:]
		}
		evidenceDetails["error"] = tail
	}
	if res.Status == "succeeded" {
		if cacheHit {
			evidenceDetails["cache"] = "hit" // replayed, zero execution
		} else if item.CacheKey != "" {
			evidenceDetails["cache"] = "miss" // really executed, now stored
		} else {
			evidenceDetails["cache"] = "disabled" // node opted out
		}
	}
	evidence := []workgraph.Evidence{{
		ID:          workgraph.NewID("evd"),
		NodeID:      item.NodeID,
		Type:        "build",
		Result:      evidenceResult(res.Status),
		Signer:      w.ID,
		Environment: fmt.Sprintf("worker=%s", w.ID),
		Details:     evidenceDetails,
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
//
// Slice-4 wiring: the subprocess runs inside a hermetic sandbox built
// from `manifest` (or the Hermetic Execution Standard #111 defaults when
// nil). Environment is allow-listed against the manifest, the working
// directory is an isolated per-attempt workspace, and network egress is
// denied by default. A nil manifest preserves the slice-2 behaviour
// (os.Environ + caller-supplied env, current working directory) — used
// by legacy callers and tests that don't opt into the sandbox.
func runCommand(ctx context.Context, command string, env map[string]string, timeout time.Duration, killCh <-chan struct{}, workDir string, manifest ...*sandbox.Manifest) execResult {
	start := time.Now()

	// ADR-0022 (k-057): resolve secret REFs against the process env at
	// execution time, once here, before the sandbox/legacy branch below,
	// so both paths (and runDocker's docker path, which calls the same
	// helper) see resolved values only. On failure the node does NOT
	// execute: the error names the REF, never a value. With no refs in
	// env this returns the input map unchanged (byte-identical legacy
	// behavior).
	resolvedEnv, resolveErr := resolveItemEnv(ctx, env)
	if resolveErr != nil {
		return execResult{
			Status:      "failed",
			ExitCode:    -1,
			CombinedLog: []byte("secret resolution failed: " + resolveErr.Error()),
			Duration:    time.Since(start),
		}
	}
	env = resolvedEnv

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}

	var prepared *sandbox.Prepared
	if len(manifest) > 0 && manifest[0] != nil {
		p, prepErr := sandbox.Prepare(cctx, command, env, *manifest[0])
		if prepErr != nil {
			return execResult{
				Status:      "failed",
				ExitCode:    -1,
				CombinedLog: []byte("sandbox prepare failed: " + prepErr.Error()),
				Duration:    time.Since(start),
			}
		}
		prepared = p
		cmd.Env = prepared.Env
		cmd.Dir = prepared.Workdir
		defer prepared.Cleanup()
	} else {
		// Legacy path: full process env + caller overrides. Kept so the
		// slice-2 API surface (ReadyItem.Env as opaque pass-through) is
		// unchanged. Slice-4 callers should always pass a manifest.
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
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

// runDocker is the slice-5 path: execute `command` inside an OCI
// container pulled from `image`. Same external behaviour as
// runCommand (returns when the command exits, the context is
// cancelled, or killCh is closed) but uses internal/sandbox.Docker
// instead of a host subprocess. The Docker backend enforces the
// slice-4 hermetic defaults (--read-only, --cap-drop=ALL,
// --network=none, no-new-privileges, memory + CPU + PIDs caps) so a
// docker run is strictly more isolated than the host path.
func runDocker(ctx context.Context, image, command string, env map[string]string, timeout time.Duration, killCh <-chan struct{}) execResult {
	// ADR-0022 (k-057): resolve secret REFs before handing env to the
	// container, same law as the host path. Fail closed: an unresolved
	// ref fails the node without running docker, naming the REF only.
	resolvedEnv, resolveErr := resolveItemEnv(ctx, env)
	if resolveErr != nil {
		return execResult{
			Status:      "failed",
			ExitCode:    -1,
			CombinedLog: []byte("secret resolution failed: " + resolveErr.Error()),
		}
	}
	env = resolvedEnv

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Watch for lease loss and abort the docker run.
	leaseLost := false
	if killCh != nil {
		doneCh := make(chan struct{})
		go func() {
			select {
			case <-killCh:
				leaseLost = true
				cancel()
			case <-doneCh:
			}
		}()
		defer close(doneCh)
	}

	res, err := sandbox.Run(cctx, image, command, sandbox.RunOptions{
		Env: env,
	})
	r := execResult{}
	if res != nil {
		r.CombinedLog = res.CombinedLog
		r.Duration = res.Duration
		r.ExitCode = res.ExitCode
	}
	if leaseLost {
		r.Status = "cancelled"
		r.ExitCode = -1
		return r
	}
	if err != nil {
		// Distinguish OOM from other failures for the classifier.
		if res != nil && res.OOMKilled {
			r.Status = "oom_killed"
		} else {
			r.Status = "failed"
		}
		return r
	}
	if res.ExitCode != 0 {
		r.Status = "failed"
		return r
	}
	r.Status = "succeeded"
	return r
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
