// Command works-worker is the local worker daemon.
//
// It polls the control plane for ready nodes, executes them as subprocesses,
// and writes evidence back. The control plane and worker share a SQLite file
// in V1 (see ADR-0005); slice 2 introduces a proper /v1/works/{id}/attempts
// HTTP reporting endpoint with HMAC signing.
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
	"github.com/JonasAbde/works-execution/services/work/store"
)

func main() {
	var (
		apiURL    = flag.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
		workerID  = flag.String("id", envOr("WORKS_WORKER_ID", "wrkr_local_"+randomSuffix()), "worker id")
		dbPath    = flag.String("db", envOr("WORKS_DB", "/tmp/works.db"), "SQLite db path (shared with API for V1)")
		artDir    = flag.String("artifacts", envOr("WORKS_ARTIFACTS", "/tmp/works-artifacts"), "artifact directory")
		pollEvery = flag.Duration("poll", 2*time.Second, "poll interval")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

	st, err := store.Open(*dbPath)
	if err != nil {
		logger.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	w := &worker.Worker{
		ID:        *workerID,
		Client:    &worker.Client{BaseURL: *apiURL, HTTP: &http.Client{Timeout: 10 * time.Second}},
		Store:     st,
		Artifacts: *artDir,
		Logger:    logger,
		PollEvery: *pollEvery,
	}

	logger.Printf("works-worker starting: id=%s api=%s db=%s", *workerID, *apiURL, *dbPath)
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