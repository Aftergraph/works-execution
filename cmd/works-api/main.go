// Command works-api runs the control plane HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JonasAbde/works-execution/packages/cache"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/publisher"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func main() {
	var (
		addr         = flag.String("addr", envOr("WORKS_ADDR", "127.0.0.1:8080"), "listen address")
		dbPath       = flag.String("db", envOr("WORKS_DB", "/tmp/works.db"), "SQLite db path")
		enrollSecret = flag.String("enroll-secret", envOr("WORKS_ENROLL_SECRET", ""), "shared challenge for POST /v1/workers/enroll; empty disables enrollment (fail-closed)")
		policyPath   = flag.String("policy", envOr("WORKS_POLICY_BUNDLE", "policies/lease_grant.rego"), "OPA Rego policy bundle path; empty disables policy enforcement (legacy)")
		webhookSecret = flag.String("webhook-secret", envOr("WORKS_WEBHOOK_SECRET", ""), "GitHub webhook HMAC secret; empty disables /v1/webhook/github (returns 503)")
		webhookProduction = flag.Bool("webhook-production-access", envBool("WORKS_WEBHOOK_PRODUCTION_ACCESS", false), "mark webhook-derived Works with policy.production_access=true (requires approved evidence at lease-grant; leave false for M1 verify works)")
		webUIPublic = flag.Bool("webui-public", envBool("WORKS_WEBUI_PUBLIC", false), "serve /v1/ui execution view without a Bearer token (read-only)")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

	st, err := store.Open(*dbPath)
	if err != nil {
		logger.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// RFC-0005: content-addressed cache shares the works database.
	// The cache package owns its table; failures to open it disable
	// caching but must not take down the control plane.
	cacheStore, err := cache.New(st.DB())
	if err != nil {
		logger.Printf("WARNING: cache store disabled: %v", err)
		cacheStore = nil
	}

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
	if *webhookSecret != "" {
		srv.WebhookConfig = &api.WebhookConfig{
			Secret:           *webhookSecret,
			AllowedRepos:     allowedReposFromEnv(),
			ProductionAccess: *webhookProduction,
			// Same credential as the publisher: used to fetch the
			// repo's works.yml (Contents API) on webhook delivery.
			GitHubToken: envOr("WORKS_GITHUB_TOKEN", ""),
		}
		logger.Printf("webhook receiver enabled (/v1/webhook/github, production_access=%v, works.yml=%v)", *webhookProduction, srv.WebhookConfig.GitHubToken != "")
	} else {
		logger.Printf("webhook receiver disabled (no --webhook-secret)")
	}

	// Publisher: prefer GitHub App if both App ID + installation-token
	// command are present; fall back to Status API with PAT; disabled
	// when neither is configured.
	if pub, perr := buildPublisher(); perr != nil {
		logger.Printf("publisher disabled: %v", perr)
	} else {
		srv.Publisher = pub
		logger.Printf("publisher enabled (kind=%s)", pub.Kind())
	}

	// RFC-0005: cache store (nil = disabled with a boot warning).
	srv.CacheStore = cacheStore
	if cacheStore != nil {
		logger.Printf("cache store enabled (/v1/cache/{key})")
	}

	// RFC-0007: execution view.
	srv.WebUI = &api.WebUIConfig{Public: *webUIPublic}
	logger.Printf("web ui enabled (/v1/ui, public=%v)", *webUIPublic)
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

func allowedReposFromEnv() map[string]bool {
	raw := strings.TrimSpace(os.Getenv("WORKS_ALLOWED_REPOS"))
	if raw == "" {
		return nil
	}
	allowed := make(map[string]bool)
	for _, repo := range strings.Split(raw, ",") {
		repo = strings.TrimSpace(repo)
		if repo != "" {
			allowed[repo] = true
		}
	}
	return allowed
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envBool returns the parsed bool of an env var, falling back to
// `def` when unset/empty or unparseable.
func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// buildPublisher selects a publisher based on env. Auto-preference:
// CheckRun (App) when WORKS_GITHUB_APP_ID + WORKS_GITHUB_INSTALLATION_TOKEN_CMD
// are both set; otherwise StatusAPI when WORKS_GITHUB_TOKEN is set;
// otherwise returns an error and the publisher stays disabled.
func buildPublisher() (publisher.Publisher, error) {
	appIDStr := envOr("WORKS_GITHUB_APP_ID", "")
	tokCmd := envOr("WORKS_GITHUB_INSTALLATION_TOKEN_CMD", "")
	if appIDStr != "" && tokCmd != "" {
		appID, err := strconv.ParseInt(appIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		return publisher.NewCheckRunPublisher(appID, func(_ context.Context, _ string) (string, error) {
			out, err := exec.Command("/bin/sh", "-c", tokCmd).Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		})
	}
	pat := envOr("WORKS_GITHUB_TOKEN", "")
	if pat == "" {
		return nil, errors.New("no credentials (set WORKS_GITHUB_TOKEN, or WORKS_GITHUB_APP_ID + WORKS_GITHUB_INSTALLATION_TOKEN_CMD)")
	}
	return publisher.NewStatusAPIPublisher(pat)
}
