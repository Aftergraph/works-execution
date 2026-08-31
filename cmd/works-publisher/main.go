// Command works-publisher is a one-shot CLI that posts a single
// status update to GitHub. It exists so the worker can publish
// terminal-state updates without bundling GitHub client code, and
// so operators can re-publish a missed status manually:
//
//	works-publisher \
//	  --repository JonasAbde/works-execution \
//	  --sha abcdef... \
//	  --conclusion success \
//	  --description "works-execution/wrk_xxx" \
//	  --details-url "https://works.example.com/v1/works/wrk_xxx"
//
// Authentication:
//
//	WORKS_GITHUB_TOKEN env var, or --token flag (rare; flag is
//	intentionally hidden from --help to avoid leaking the token in
//	process listings).
//
// The binary selects StatusAPIPublisher when a PAT is present and
// CheckRunPublisher when the App credentials are also present
// (env: WORKS_GITHUB_APP_ID + WORKS_GITHUB_APP_INSTALLATION_TOKEN_CMD).
// PAT-mode is the M1 default — it works today, no App required.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/services/publisher"
)

const usage = `works-publisher — post a status update to GitHub

Usage:
  works-publisher --repository OWNER/NAME --sha SHA --conclusion success|failure|pending
                  [--description TEXT] [--details-url URL]
                  [--kind status|check-run]   # default: status

Environment:
  WORKS_GITHUB_TOKEN                   PAT for Status API (default publisher)
  WORKS_GITHUB_APP_ID                  GitHub App ID (optional; activates Check Runs when set)
  WORKS_GITHUB_INSTALLATION_TOKEN_CMD  Shell command that prints an installation token (optional)

Exit codes:
  0  published
  1  publish failed (network, validation, or non-2xx)
  2  usage error
`

func main() {
	fs := flag.NewFlagSet("works-publisher", flag.ExitOnError)
	repo := fs.String("repository", "", "repository (owner/name)")
	sha := fs.String("sha", "", "commit SHA (40 hex chars)")
	conc := fs.String("conclusion", "", "success|failure|pending")
	desc := fs.String("description", "", "short status text")
	details := fs.String("details-url", "", "link to the work's detail page")
	kind := fs.String("kind", "", "publisher kind override (status|check-run). default: auto from env")
	_ = fs.Parse(os.Args[1:])

	if *repo == "" || *sha == "" || *conc == "" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var conclusion publisher.Conclusion
	switch strings.ToLower(*conc) {
	case "success":
		conclusion = publisher.ConclusionSuccess
	case "failure", "fail", "failed":
		conclusion = publisher.ConclusionFailure
	case "pending":
		conclusion = publisher.ConclusionPending
	default:
		fmt.Fprintf(os.Stderr, "works-publisher: invalid conclusion %q (want success|failure|pending)\n", *conc)
		os.Exit(2)
	}

	r := publisher.Result{
		Repository:  *repo,
		SHA:         *sha,
		Conclusion:  conclusion,
		Description: *desc,
		DetailsURL:  *details,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p, err := selectPublisher(*kind)
	if err != nil {
		fmt.Fprintln(os.Stderr, "works-publisher:", err)
		os.Exit(1)
	}
	if err := p.Publish(ctx, r); err != nil {
		fmt.Fprintln(os.Stderr, "works-publisher: publish:", err)
		os.Exit(1)
	}
	shaShort := *sha
	if len(shaShort) > 7 {
		shaShort = shaShort[:7]
	}
	fmt.Printf("published via %s: %s/%s %s\n", p.Kind(), *repo, shaShort, *conc)
}

// selectPublisher returns StatusAPI by default (works with PAT), or
// CheckRun if App credentials are present in env. The kind flag
// forces one or the other for debugging.
func selectPublisher(forceKind string) (publisher.Publisher, error) {
	pat := envOr("WORKS_GITHUB_TOKEN", "")
	appIDStr := envOr("WORKS_GITHUB_APP_ID", "")
	tokCmd := envOr("WORKS_GITHUB_INSTALLATION_TOKEN_CMD", "")

	switch strings.ToLower(forceKind) {
	case "status":
		return publisher.NewStatusAPIPublisher(pat)
	case "check-run":
		if appIDStr == "" || tokCmd == "" {
			return nil, fmt.Errorf("check-run publisher requires WORKS_GITHUB_APP_ID and WORKS_GITHUB_INSTALLATION_TOKEN_CMD")
		}
		appID, err := strconv.ParseInt(appIDStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse WORKS_GITHUB_APP_ID: %w", err)
		}
		return publisher.NewCheckRunPublisher(appID, func(_ context.Context, _ string) (string, error) {
			out, err := exec.Command("/bin/sh", "-c", tokCmd).Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		})
	}

	// Auto: prefer CheckRun if fully configured, else StatusAPI.
	if appIDStr != "" && tokCmd != "" {
		appID, err := strconv.ParseInt(appIDStr, 10, 64)
		if err == nil {
			p, perr := publisher.NewCheckRunPublisher(appID, func(_ context.Context, _ string) (string, error) {
				out, err := exec.Command("/bin/sh", "-c", tokCmd).Output()
				if err != nil {
					return "", err
				}
				return strings.TrimSpace(string(out)), nil
			})
			if perr == nil {
				return p, nil
			}
		}
	}
	if pat == "" {
		return nil, fmt.Errorf("no publisher credentials: set WORKS_GITHUB_TOKEN (PAT) or WORKS_GITHUB_APP_ID + WORKS_GITHUB_INSTALLATION_TOKEN_CMD (App)")
	}
	return publisher.NewStatusAPIPublisher(pat)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}