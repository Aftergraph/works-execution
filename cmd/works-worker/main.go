// Command works-worker is the local worker daemon.
//
// It polls the control plane for ready nodes, acquires a lease for each,
// executes the node as a subprocess, heartbeats the lease while running,
// kills the subprocess if the lease is lost, and reports the terminal
// result via the lease's /complete endpoint.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/JonasAbde/works-execution/internal/worker"
)

func main() {
	var (
		apiURL         = flag.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
		workerID       = flag.String("id", envOr("WORKS_WORKER_ID", "wrkr_local_"+randomSuffix()), "worker id")
		dbPath         = flag.String("db", envOr("WORKS_DB", "/tmp/works.db"), "(unused; worker uses HTTP only — kept for backward compat)")
		artDir         = flag.String("artifacts", envOr("WORKS_ARTIFACTS", "/tmp/works-artifacts"), "artifact directory")
		pollEvery      = flag.Duration("poll", 2*time.Second, "poll interval")
		leaseTTL       = flag.Duration("lease-ttl", 25*time.Second, "lease TTL")
		heartbeatEvery = flag.Duration("heartbeat", 10*time.Second, "heartbeat interval")
		enrollSecret   = flag.String("enroll-secret", envOr("WORKS_ENROLL_SECRET", ""), "shared secret for /v1/workers/enroll (Zero-Secret: required)")
		enrollTTL      = flag.Duration("enroll-ttl", time.Hour, "requested enrollment-token TTL")
		githubToken    = flag.String("github-token", envOr("WORKS_GITHUB_TOKEN", ""), "GitHub token for work-scoped source checkout")
		pool           = flag.String("pool", envOr("WORKS_POOL", ""), "BYOC pool name (RFC-0004); joins pool <name> via label pool:<name>")
		trust          = flag.String("trust", envOr("WORKS_TRUST_CLASS", ""), "runner trust class override (untrusted|standard|privileged); default standard")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

	// Open the store only to share the artifacts dir creation with the API
	// if the user is co-locating them. The worker itself uses HTTP only.
	_ = dbPath

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cli := &worker.Client{
		BaseURL:       *apiURL,
		HTTP:          &http.Client{Timeout: 10 * time.Second},
		WorkerID:      *workerID,
		EnrollSecret:  *enrollSecret,
		EnrollTTL:     *enrollTTL,
	}

	// Zero-Secret enrollment (k-impl-003): mint a short-lived JWT before
	// the first /ready poll. If the server has enrollment disabled
	// (EnrollSecret empty) we still try — the server returns 503 and we
	// fall back to unauthenticated mode for dev/test, but log it loudly
	// so production operators see the misconfiguration.
	//
	// Boot-resilience: when started alongside works-api under systemd,
	// the API listener may not be up yet (connection refused). Retry
	// network errors with backoff for up to ~60s before giving up.
	// 401/403 (bad secret) fail fast — retrying cannot fix config.
	if *enrollSecret != "" {
		const maxAttempts = 30
		var enrolled bool
		for attempt := 1; attempt <= maxAttempts && !enrolled; attempt++ {
			token, err := cli.Enroll(ctx, *workerID, *enrollSecret, *enrollTTL)
			if err == nil {
				cli.Token = token
				logger.Printf("enrolled: worker_id=%s ttl=%s", *workerID, *enrollTTL)
				enrolled = true
				break
			}
			// Classify the error: 503 = enrollment disabled (fall back
			// to unauthenticated dev mode); 4xx = config error (fail
			// fast); everything else (network, 5xx) = transient (retry).
			msg := err.Error()
			switch {
			case strings.Contains(msg, "503"):
				logger.Printf("WARNING: enrollment disabled on server (503); running WITHOUT Bearer token (dev mode)")
				enrolled = true // proceed without token
			case strings.Contains(msg, "401") || strings.Contains(msg, "403"):
				logger.Fatalf("enrollment rejected (%v); check -enroll-secret against the server's WORKS_ENROLL_SECRET", err)
			default:
				if attempt == maxAttempts {
					logger.Fatalf("enrollment failed after %d attempts: %v", maxAttempts, err)
				}
				logger.Printf("enrollment attempt %d/%d failed (%v); retrying in 2s", attempt, maxAttempts, err)
				select {
				case <-time.After(2 * time.Second):
				case <-ctx.Done():
					logger.Fatalf("enrollment aborted: %v", ctx.Err())
				}
			}
		}
	} else {
		logger.Printf("WARNING: WORKS_ENROLL_SECRET not set; worker running without Bearer token (dev mode)")
	}

	w := &worker.Worker{
		ID:           *workerID,
		Client:       cli,
		ArtifactsDir: *artDir,
		Logger:       logger,
		PollEvery:    *pollEvery,
		LeaseTTL:     *leaseTTL,
		// HeartbeatEvery is both the lease heartbeat and the runner
		// re-registration (BYOC) interval. Keep the default.
		HeartbeatEvery: *heartbeatEvery,
		GitHubToken:    *githubToken,
	}
	// BYOC (RFC-0004): when -pool or -trust is set, the worker
	// registers itself as a scheduler-visible runner and keeps the
	// registration alive via heartbeats. Pool membership is the
	// "pool:<name>" label; the scheduler's hard filter uses it to
	// route pool-scoped works exclusively to this pool's runners.
	if *pool != "" || *trust != "" {
		labels := []string{}
		if *pool != "" {
			labels = append(labels, "pool:"+*pool)
		}
		w.RunnerIdentity = &worker.RunnerSpec{
			TrustClass: *trust,
			Labels:     labels,
		}
		logger.Printf("byoc runner enabled: pool=%q trust=%q", *pool, *trust)
	}

	logger.Printf("works-worker starting: id=%s api=%s lease_ttl=%s heartbeat=%s", *workerID, *apiURL, *leaseTTL, *heartbeatEvery)
	if err := w.Run(ctx); err != nil && err != context.Canceled {
		logger.Fatalf("worker exited: %v", err)
	}
	logger.Printf("works-worker stopped")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func randomSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
