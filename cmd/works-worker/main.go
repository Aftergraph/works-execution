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
	"syscall"
	"time"

	"github.com/JonasAbde/works-execution/internal/worker"
)

func main() {
	var (
		apiURL          = flag.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
		workerID        = flag.String("id", envOr("WORKS_WORKER_ID", "wrkr_local_"+randomSuffix()), "worker id")
		dbPath          = flag.String("db", envOr("WORKS_DB", "/tmp/works.db"), "(unused; worker uses HTTP only — kept for backward compat)")
		artDir          = flag.String("artifacts", envOr("WORKS_ARTIFACTS", "/tmp/works-artifacts"), "artifact directory")
		pollEvery       = flag.Duration("poll", 2*time.Second, "poll interval")
		leaseTTL        = flag.Duration("lease-ttl", 25*time.Second, "lease TTL")
		heartbeatEvery  = flag.Duration("heartbeat", 10*time.Second, "heartbeat interval")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

	// Open the store only to share the artifacts dir creation with the API
	// if the user is co-locating them. The worker itself uses HTTP only.
	_ = dbPath

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	w := &worker.Worker{
		ID:             *workerID,
		Client:         &worker.Client{BaseURL: *apiURL, HTTP: &http.Client{Timeout: 10 * time.Second}},
		ArtifactsDir:   *artDir,
		Logger:         logger,
		PollEvery:      *pollEvery,
		LeaseTTL:       *leaseTTL,
		HeartbeatEvery: *heartbeatEvery,
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