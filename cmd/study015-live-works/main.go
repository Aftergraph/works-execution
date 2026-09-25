// Command study015-live-works exposes the exact WORKS P2 V2 owner candidate
// as an isolated loopback process for STUDY-015 composition/recovery testing.
//
// Research-only: this command does not alter cmd/works-api or make the legacy
// dispatch.acceptance/1.0 surface permissive. It wires only existing V2 routes
// already present on the stacked #127/#134 owner candidate.
package main

import (
	"context"
	"crypto/subtle"
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
	Recovered     bool   `json:"recovered"`
	Relocated     bool   `json:"relocated"`
	PriorDBPath   string `json:"prior_db_path,omitempty"`
}

func requiredSecret(name string) string {
	value := os.Getenv(name)
	if len([]byte(value)) < 32 {
		log.Fatalf("%s must be configured with at least 32 bytes", name)
	}
	return value
}

func readFixture(path string) (fixtureReceipt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fixtureReceipt{}, err
	}
	var prior fixtureReceipt
	if err := json.Unmarshal(raw, &prior); err != nil {
		return fixtureReceipt{}, err
	}
	if prior.WorkID == "" || prior.WorkerLeaseID == "" || prior.WorkerID == "" || prior.DBPath == "" {
		return fixtureReceipt{}, errors.New("resume fixture is missing durable identity")
	}
	return prior, nil
}

func validateResumePath(priorPath, dbPath string, allowRelocated bool) error {
	if filepath.Clean(priorPath) == filepath.Clean(dbPath) {
		return nil
	}
	if allowRelocated {
		return nil
	}
	return errors.New("resume fixture DB path mismatch")
}

func recoverFixture(
	ctx context.Context,
	st *store.SQLiteStore,
	prior fixtureReceipt,
	dbPath string,
	now time.Time,
	allowRelocated bool,
) (*workgraph.Work, *workgraph.Lease, error) {
	if err := validateResumePath(prior.DBPath, dbPath, allowRelocated); err != nil {
		return nil, nil, err
	}
	w, err := st.GetWork(ctx, prior.WorkID)
	if err != nil {
		return nil, nil, fmt.Errorf("recover work: %w", err)
	}
	lease, err := st.GetLease(ctx, prior.WorkerLeaseID)
	if err != nil {
		return nil, nil, fmt.Errorf("recover lease: %w", err)
	}
	if lease.WorkID != prior.WorkID || lease.WorkerID != prior.WorkerID {
		return nil, nil, errors.New("resume fixture lease identity mismatch")
	}
	if lease.Status != workgraph.LeaseActive {
		return nil, nil, fmt.Errorf("resume fixture lease is not ACTIVE: %s", lease.Status)
	}
	if !lease.ExpiresAt.After(now) {
		return nil, nil, errors.New("resume fixture lease expired")
	}
	return w, lease, nil
}

func study015Handler(st *store.SQLiteStore, bridgeSecret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/study015/execution-policy-correlation" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method_not_allowed", http.StatusMethodNotAllowed)
			return
		}
		presented := r.Header.Get("X-Works-Platform-Bridge")
		if len(presented) != len(bridgeSecret) ||
			subtle.ConstantTimeCompare([]byte(presented), []byte(bridgeSecret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		executionContextID := strings.TrimSpace(r.URL.Query().Get("execution_context_id"))
		if executionContextID == "" {
			http.Error(w, "execution_context_id_required", http.StatusBadRequest)
			return
		}
		rec, err := st.GetExecutionPolicyCorrelation(r.Context(), executionContextID)
		if err != nil {
			http.Error(w, "correlation_read_failed", http.StatusInternalServerError)
			return
		}
		if rec == nil {
			http.Error(w, "not_found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema":               "study015.execution-policy-correlation/1.0",
			"work_id":              rec.WorkID,
			"execution_context_id": rec.ExecutionContextID,
			"execution_pdr_id":     rec.ExecutionPDRID,
			"binding_digest":       rec.BindingDigest,
			"recorded_at":          rec.RecordedAt.UTC().Format(time.RFC3339Nano),
		})
	})
}

func createFixture(ctx context.Context, st *store.SQLiteStore) (*workgraph.Work, *workgraph.Lease, error) {
	workerID := "wrkr_" + strings.Repeat("7", 32)
	w := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "study015"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"effect": {ID: "effect", Run: "true"},
		}},
	}
	if err := st.CreateWork(ctx, w); err != nil {
		return nil, nil, fmt.Errorf("create fixture work: %w", err)
	}
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		return nil, nil, fmt.Errorf("queue fixture work: %w", err)
	}
	lease, _, err := st.GrantLease(ctx, w.ID, "effect", workerID, 30*time.Minute)
	if err != nil {
		return nil, nil, fmt.Errorf("grant fixture WorkerLease: %w", err)
	}
	return w, lease, nil
}

func main() {
	var addr, dbPath, fixtureOut, resumeFixture string
	var resumeRelocated bool
	flag.StringVar(&addr, "addr", "127.0.0.1:0", "loopback listen address")
	flag.StringVar(&dbPath, "db", "", "SQLite path (default: temp dir)")
	flag.StringVar(&fixtureOut, "fixture-out", "", "required non-secret fixture receipt path")
	flag.StringVar(&resumeFixture, "resume-fixture", "", "existing fixture receipt to recover without minting new work/lease")
	flag.BoolVar(&resumeRelocated, "resume-relocated", false, "permit resume from the same durable identities at a different host-local DB path")
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
	bridgeSecret := requiredSecret("WORKS_PLATFORM_BRIDGE_SECRET")
	verifierToken := requiredSecret("WORKS_VERIFIER_TOKEN")

	var prior fixtureReceipt
	if resumeFixture != "" {
		prior, err = readFixture(resumeFixture)
		if err != nil {
			log.Fatalf("read --resume-fixture: %v", err)
		}
		if dbPath == "" {
			dbPath = prior.DBPath
		}
	}

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
	var w *workgraph.Work
	var lease *workgraph.Lease
	recovered := false
	if resumeFixture != "" {
		w, lease, err = recoverFixture(ctx, st, prior, dbPath, time.Now().UTC(), resumeRelocated)
		if err != nil {
			log.Fatalf("recover durable fixture: %v", err)
		}
		recovered = true
	} else {
		w, lease, err = createFixture(ctx, st)
		if err != nil {
			log.Fatal(err)
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
		Schema:        "study015.live-works-fixture/1.2",
		BaseURL:       baseURL,
		WorkID:        w.ID,
		WorkerLeaseID: lease.ID,
		WorkerID:      lease.WorkerID,
		DBPath:        dbPath,
		Recovered:     recovered,
		Relocated:     recovered && resumeRelocated && filepath.Clean(prior.DBPath) != filepath.Clean(dbPath),
	}
	if receipt.Relocated {
		receipt.PriorDBPath = prior.DBPath
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(fixtureOut, append(raw, '\n'), 0o600); err != nil {
		log.Fatalf("write fixture receipt: %v", err)
	}
	fmt.Fprintf(os.Stdout, "STUDY015_WORKS_READY %s recovered=%t\n", baseURL, recovered)

	httpServer := &http.Server{
		Handler:           study015Handler(st, bridgeSecret, srv.Routes()),
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
