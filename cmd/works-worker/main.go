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
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/JonasAbde/works-execution/internal/worker"
)

func main() {
	var (
		apiURL                  = flag.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
		workerID                = flag.String("id", envOr("WORKS_WORKER_ID", "wrkr_local_"+randomSuffix()), "worker id")
		dbPath                  = flag.String("db", envOr("WORKS_DB", "/tmp/works.db"), "(unused; worker uses HTTP only — kept for backward compat)")
		artDir                  = flag.String("artifacts", envOr("WORKS_ARTIFACTS", ""), "artifact directory")
		pollEvery               = flag.Duration("poll", 2*time.Second, "poll interval")
		leaseTTL                = flag.Duration("lease-ttl", 25*time.Second, "lease TTL")
		heartbeatEvery          = flag.Duration("heartbeat", 10*time.Second, "heartbeat interval")
		enrollSecret            = flag.String("enroll-secret", envOr("WORKS_ENROLL_SECRET", ""), "legacy shared challenge for enrollment; mTLS worker certificates can replace it")
		mtlsCA                  = flag.String("mtls-ca", envOr("WORKS_MTLS_CA", ""), "CA bundle used to verify the WORKS server certificate")
		mtlsCert                = flag.String("mtls-cert", envOr("WORKS_MTLS_CERT", ""), "provisioned client certificate carrying the worker SPIFFE URI SAN")
		mtlsKey                 = flag.String("mtls-key", envOr("WORKS_MTLS_KEY", ""), "private key for the provisioned worker mTLS certificate")
		mtlsServerName          = flag.String("mtls-server-name", envOr("WORKS_MTLS_SERVER_NAME", ""), "DNS identity to verify in the WORKS server certificate")
		allowUnauthenticatedDev = flag.Bool("allow-unauthenticated-dev", false, "allow unauthenticated WORKS only with a loopback API URL for local development")
		enrollTTL               = flag.Duration("enroll-ttl", time.Hour, "requested enrollment-token TTL")
		githubToken             = flag.String("github-token", envOr("WORKS_GITHUB_TOKEN", ""), "GitHub token for work-scoped source checkout")
		sourceRoot              = flag.String("source-root", envOr("WORKS_SOURCE_ROOT", ""), "absolute root for work-scoped source checkout; empty uses OS temp")
		pool                    = flag.String("pool", envOr("WORKS_POOL", ""), "BYOC pool name (RFC-0004); joins pool <name> via label pool:<name>")
		trust                   = flag.String("trust", envOr("WORKS_TRUST_CLASS", ""), "runner trust class override (untrusted|standard|privileged); default standard")
	)
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)
	enrollmentSecret := strings.TrimSpace(*enrollSecret)
	mtlsConfigured := strings.TrimSpace(*mtlsCA) != "" || strings.TrimSpace(*mtlsCert) != "" || strings.TrimSpace(*mtlsKey) != "" || strings.TrimSpace(*mtlsServerName) != ""
	if err := validateWorkerAuthentication(enrollmentSecret, *apiURL, *allowUnauthenticatedDev, mtlsConfigured, *mtlsCA, *mtlsCert, *mtlsKey, *mtlsServerName); err != nil {
		logger.Fatal(err)
	}

	// WORKS traffic uses HTTP semantics with optional inner TLS/mTLS. Run
	// initializes its pinned artifact root when no explicit directory is set.
	_ = dbPath

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	workerHTTP := &http.Client{Timeout: 10 * time.Second}
	if mtlsConfigured {
		var err error
		workerHTTP, err = worker.NewMTLSHTTPClient(10*time.Second, *mtlsCA, *mtlsCert, *mtlsKey, *mtlsServerName)
		if err != nil {
			logger.Fatalf("configure worker mTLS: %v", err)
		}
	}
	cli := &worker.Client{
		BaseURL:               *apiURL,
		HTTP:                  workerHTTP,
		WorkerID:              *workerID,
		EnrollSecret:          enrollmentSecret,
		EnrollTTL:             *enrollTTL,
		CertificateEnrollment: mtlsConfigured,
	}

	// Enroll for a short-lived JWT before
	// the first /ready poll. Missing enrollment configuration fails closed
	// unless local development was explicitly enabled for a loopback API.
	//
	// Boot-resilience: when started alongside works-api under systemd,
	// the API listener may not be up yet (connection refused). Retry
	// network errors with backoff for up to ~60s before giving up.
	// 401/403 (bad secret) fail fast — retrying cannot fix config.
	if enrollmentSecret != "" || mtlsConfigured {
		const maxAttempts = 30
		var enrolled bool
		for attempt := 1; attempt <= maxAttempts && !enrolled; attempt++ {
			token, err := cli.Enroll(ctx, *workerID, enrollmentSecret, *enrollTTL)
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
				if !allowUnauthenticatedFallback(http.StatusServiceUnavailable, *allowUnauthenticatedDev) {
					logger.Fatalf("enrollment disabled on server (503); refusing unauthenticated mode without explicit loopback development opt-in")
				}
				logger.Printf("WARNING: enrollment disabled on server (503); running WITHOUT Bearer token (dev mode)")
				enrolled = true // proceed without token
			case strings.Contains(msg, "401") || strings.Contains(msg, "403"):
				logger.Fatalf("enrollment rejected (%v); check the worker certificate identity and any configured enrollment challenge", err)
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
		logger.Printf("WARNING: WORKS_ENROLL_SECRET not set; explicit loopback development mode is enabled")
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
		SourceRoot:     *sourceRoot,
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

	logger.Printf("works-worker starting: id=%s api=%s lease_ttl=%s heartbeat=%s source_root=%q", *workerID, *apiURL, *leaseTTL, *heartbeatEvery, *sourceRoot)
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

func validateEnrollmentConfiguration(enrollSecret, apiURL string, allowUnauthenticatedDev bool) error {
	return validateWorkerAuthentication(enrollSecret, apiURL, allowUnauthenticatedDev, false, "", "", "", "")
}

func validateWorkerAuthentication(enrollSecret, apiURL string, allowUnauthenticatedDev, mtlsConfigured bool, caFile, certFile, keyFile, serverName string) error {
	apiIsLoopback, err := validateAPIURL(apiURL)
	if err != nil {
		return err
	}
	if mtlsConfigured {
		if strings.TrimSpace(caFile) == "" || strings.TrimSpace(certFile) == "" || strings.TrimSpace(keyFile) == "" || strings.TrimSpace(serverName) == "" {
			return errors.New("worker mTLS requires WORKS_MTLS_CA, WORKS_MTLS_CERT, WORKS_MTLS_KEY, and WORKS_MTLS_SERVER_NAME")
		}
		parsed, _ := url.Parse(apiURL)
		if !strings.EqualFold(parsed.Scheme, "https") {
			return errors.New("worker mTLS requires an HTTPS WORKS_API URL")
		}
		if allowUnauthenticatedDev {
			return errors.New("--allow-unauthenticated-dev cannot be combined with worker mTLS")
		}
	}
	if allowUnauthenticatedDev && !apiIsLoopback {
		return errors.New("--allow-unauthenticated-dev requires a loopback WORKS_API URL")
	}
	if strings.TrimSpace(enrollSecret) == "" && !allowUnauthenticatedDev && !mtlsConfigured {
		return errors.New("WORKS_ENROLL_SECRET or a complete worker mTLS configuration is required; use --allow-unauthenticated-dev only for local loopback development")
	}
	return nil
}

func allowUnauthenticatedFallback(statusCode int, allowUnauthenticatedDev bool) bool {
	return statusCode == http.StatusServiceUnavailable && allowUnauthenticatedDev
}

func validateAPIURL(raw string) (bool, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" ||
		(!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return false, errors.New("WORKS_API must be an absolute HTTP(S) URL without embedded credentials")
	}
	loopback := isLoopbackHostname(u.Hostname())
	if !loopback && !strings.EqualFold(u.Scheme, "https") {
		return false, errors.New("non-loopback WORKS_API URLs must use HTTPS")
	}
	return loopback, nil
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
