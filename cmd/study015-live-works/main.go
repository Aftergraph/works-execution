// Command study015-live-works exposes the exact WORKS P2 V2 owner candidate
// as an isolated loopback process for STUDY-015 L3 composition testing.
//
// Research-only: this command does not alter cmd/works-api or make the legacy
// dispatch.acceptance/1.0 surface permissive. It wires only existing V2 routes
// already present on the stacked #127/#134 owner candidate.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

type fixtureReceipt struct {
	Schema        string `json:"schema"`
	BaseURL       string `json:"base_url"`
	WorkID        string `json:"work_id"`
	WorkerLeaseID string `json:"worker_lease_id"`
	WorkerID      string `json:"worker_id"`
	DBPath        string `json:"db_path"`
}

func requiredSecret(name string) string {
	value := os.Getenv(name)
	if len([]byte(value)) < 32 {
		log.Fatalf("%s must be configured with at least 32 bytes", name)
	}
	return value
}

func main() {
	var addr, dbPath, fixtureOut, resumeWorkID, resumeLeaseID string
	flag.StringVar(&addr, "addr", "127.0.0.1:0", "loopback listen address")
	flag.StringVar(&dbPath, "db", "", "SQLite path (default: temp dir)")
	flag.StringVar(&fixtureOut, "fixture-out", "", "required non-secret fixture receipt path")
	flag.StringVar(&resumeWorkID, "resume-work-id", "", "existing work id to reopen without reseeding")
	flag.StringVar(&resumeLeaseID, "resume-worker-lease-id", "", "existing worker lease id to reopen without reseeding")
	flag.Parse()

	if fixtureOut == "" {
		log.Fatal("--fixture-out is required")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		log.Fatalf("invalid --addr: %v", err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		log.Fatal("STUDY-015 live WORKS may bind loopback only")
	}

	platformToken := requiredSecret("WORKS_API_TOKEN")
	_ = requiredSecret("WORKS_PLATFORM_BRIDGE_SECRET")
	verifierToken := requiredSecret("WORKS_VERIFIER_TOKEN")

	if dbPath == "" {
		dir, err := os.MkdirTemp("", "study015-live-works-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		dbPath = filepath.Join(dir, "works.db")
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open WORKS store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	workerID := "wrkr_" + strings.Repeat("7", 32)
	var w *workgraph.Work
	var lease *workgraph.Lease

	resumeRequested := resumeWorkID != "" || resumeLeaseID != ""
	if resumeRequested {
		if resumeWorkID == "" || resumeLeaseID == "" {
			log.Fatal("--resume-work-id and --resume-worker-lease-id must be supplied together")
		}
		w, err = st.GetWork(ctx, resumeWorkID)
		if err != nil {
			log.Fatalf("resume fixture work: %v", err)
		}
		lease, err = st.GetLease(ctx, resumeLeaseID)
		if err != nil {
			log.Fatalf("resume fixture WorkerLease: %v", err)
		}
		if lease.WorkID != w.ID {
			log.Fatalf("resume binding mismatch: lease work %s != requested work %s", lease.WorkID, w.ID)
		}
		workerID = lease.WorkerID
	} else {
		w = &workgraph.Work{
			ID:        workgraph.NewID("wrk"),
			State:     workgraph.StateCreated,
			Source:    workgraph.Source{Type: "study015"},
			Objective: workgraph.Objective{Type: "verify_change"},
			Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
				"effect": {ID: "effect", Run: "true"},
			}},
		}
		if err := st.CreateWork(ctx, w); err != nil {
			log.Fatalf("create fixture work: %v", err)
		}
		if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
			log.Fatalf("queue fixture work: %v", err)
		}
		lease, _, err = st.GrantLease(ctx, w.ID, "effect", workerID, 30*time.Minute)
		if err != nil {
			log.Fatalf("grant fixture WorkerLease: %v", err)
		}
	}

	srv := &api.Server{
		Store:            st,
		Logger:           log.New(os.Stderr, "study015-works ", log.LstdFlags|log.Lmicroseconds),
		AuthEnabled:      false,
		PlatformAPIToken: []byte(platformToken),
		VerifierToken:    []byte(verifierToken),
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	baseURL := "http://" + listener.Addr().String()
	receipt := fixtureReceipt{
		Schema:        "study015.live-works-fixture/1.0",
		BaseURL:       baseURL,
		WorkID:        w.ID,
		WorkerLeaseID: lease.ID,
		WorkerID:      workerID,
		DBPath:        dbPath,
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(fixtureOut, append(raw, '\n'), 0o600); err != nil {
		log.Fatalf("write fixture receipt: %v", err)
	}
	fmt.Fprintf(os.Stdout, "STUDY015_WORKS_READY %s\n", baseURL)

	httpServer := &http.Server{
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		<-stop.Done()
		shutdown, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = httpServer.Shutdown(shutdown)
	}()
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}
