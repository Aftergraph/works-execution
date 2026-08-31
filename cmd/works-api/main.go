// Command works-api runs the control plane HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func main() {
	var (
		addr         = flag.String("addr", envOr("WORKS_ADDR", "127.0.0.1:8080"), "listen address")
		dbPath       = flag.String("db", envOr("WORKS_DB", "/tmp/works.db"), "SQLite db path")
		enrollSecret = flag.String("enroll-secret", envOr("WORKS_ENROLL_SECRET", ""), "shared challenge for POST /v1/workers/enroll; empty disables enrollment (fail-closed)")
		policyPath   = flag.String("policy", envOr("WORKS_POLICY_BUNDLE", "policies/lease_grant.rego"), "OPA Rego policy bundle path; empty disables policy enforcement (legacy)")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

	st, err := store.Open(*dbPath)
	if err != nil {
		logger.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Load the policy bundle. Production deploys ship with the bundle on
	// disk; an empty path disables enforcement (legacy behavior, NOT
	// recommended for production).
	var policyEngine *api.Engine
	if *policyPath != "" {
		engine, perr := api.LoadBundle(*policyPath)
		if perr != nil {
			logger.Fatalf("load policy bundle %s: %v", *policyPath, perr)
		}
		logger.Printf("loaded policy bundle %s (version=%s)", *policyPath, engine.BundleVersion())
		policyEngine = engine
	} else {
		logger.Printf("WARNING: policy enforcement disabled (--policy=\"\")")
	}

	srv := &api.Server{
		Store:        st,
		Logger:       logger,
		ArtifactsDir: envOr("WORKS_ARTIFACTS", ""),
		EnrollSecret: *enrollSecret,
		Policy:       policyEngine,
		AuthEnabled:  true,
	}
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Launch the lease-reaper. Slice 2: lost-worker detection target is <30s,
	// achieved by TTL=25s + reaper interval=5s (worst case 30s).
	reaperCtx, cancelReaper := context.WithCancel(ctx)
	defer cancelReaper()
	go func() {
		if err := api.RunLeaseReaper(reaperCtx, st, api.ReaperConfig{}); err != nil &&
			!errors.Is(err, context.Canceled) {
			logger.Printf("reaper exited: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	logger.Printf("works-api listening on %s (db=%s)", *addr, *dbPath)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("listen: %v", err)
	}
	logger.Printf("works-api stopped")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
