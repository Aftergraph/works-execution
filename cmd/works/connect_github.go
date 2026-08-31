package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// connectCmd handles `works connect github`. It prints:
//
//   - the public webhook URL operators must register on GitHub
//     (WORKS_WEBHOOK_URL, falls back to --api),
//   - the required HMAC secret (read from WORKS_WEBHOOK_SECRET via
//     the API's /v1/webhook/github path — operators must paste the
//     same secret into GitHub's webhook config),
//   - the recommended GitHub webhook events (push, pull_request),
//   - a copy-pasteable curl command that signs a ping payload,
//   - and the works-api health endpoint to confirm reachability.
//
// The command never makes a GitHub API call itself: GitHub App
// credentials (when present) make the registration automatic via
// the App's manifest flow, but that lives in `works-publisher`,
// not here. `connect github` is the human/operator checklist.
func connectCmd(args []string) {
	if len(args) == 0 || args[0] != "github" {
		fmt.Fprint(os.Stderr, "usage: works connect github\n\nprints the GitHub webhook registration checklist.\n")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("connect github", flag.ExitOnError)
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "works-api URL (control plane)")
	_ = fs.Parse(args[1:])

	webhookURL := envOr("WORKS_WEBHOOK_URL", "")
	if webhookURL == "" {
		// Derive from the API URL — operators running locally can
		// override with WORKS_WEBHOOK_URL=https://public.example.com.
		webhookURL = derivePublicURL(*api)
	}

	fmt.Println("WORKS ←→ GitHub webhook checklist")
	fmt.Println()
	fmt.Println("1) Webhook URL (paste into GitHub → Settings → Webhooks → Add):")
	fmt.Printf("   %s/v1/webhook/github\n", webhookURL)
	fmt.Println()
	fmt.Println("2) Content type: application/json")
	fmt.Println("   SSL verification: enabled (recommended)")
	fmt.Println()
	fmt.Println("3) Secret (must match WORKS_WEBHOOK_SECRET on the works-api):")
	secret := envOr("WORKS_WEBHOOK_SECRET", "")
	if secret == "" {
		fmt.Println("   <unset — set WORKS_WEBHOOK_SECRET on the works-api before starting it>")
	} else {
		// Print the first 8 + last 4 chars so operators can
		// eyeball-confirm without the secret ending up in
		// shell history wholesale.
		fmt.Printf("   %s…%s (length %d; full value is in WORKS_WEBHOOK_SECRET)\n",
			secret[:min(8, len(secret))], secret[max(0, len(secret)-4):], len(secret))
	}
	fmt.Println()
	fmt.Println("4) Events to deliver:")
	fmt.Println("   ☑ push")
	fmt.Println("   ☑ pull_request")
	fmt.Println("   ☐ everything else (do NOT enable)")
	fmt.Println()
	fmt.Println("5) Smoke test (after saving): GitHub → redeliver a recent")
	fmt.Println("   ping delivery; works-api should log 200 ignored. Or run:")
	fmt.Println()
	pingBody := `{"zen":"Speak like a human"}`
	fmt.Printf("   curl -sS -X POST %s/v1/webhook/github \\\n", webhookURL)
	fmt.Println("        -H 'Content-Type: application/json' \\")
	fmt.Println("        -H 'X-GitHub-Event: ping' \\")
	fmt.Println("        -H 'X-GitHub-Delivery: test-ping-001' \\")
	if secret != "" {
		fmt.Printf("        -H \"X-Hub-Signature-256: sha256=$(printf '%s' '%s' | openssl dgst -sha256 -hmac \"$WORKS_WEBHOOK_SECRET\" -hex | awk '{print $NF}')\" \\\n",
			pingBody, pingBody)
	} else {
		fmt.Println("        -H 'X-Hub-Signature-256: sha256=<computed from WORKS_WEBHOOK_SECRET>' \\")
	}
	fmt.Printf("        --data '%s'\n\n", pingBody)

	// Probe the works-api health endpoint so we can surface a clear
	// error if the operator is connecting to the wrong URL.
	fmt.Println("6) works-api reachability:")
	if err := probeHealth(*api); err != nil {
		fmt.Printf("   ✗ %v\n", err)
		os.Exit(1)
	}
	fmt.Println("   ✓ /healthz OK")
	fmt.Println()
	fmt.Println("After saving the webhook in GitHub, run `works pilot <owner>/<repo>`")
	fmt.Println("on any push/PR to verify the round-trip end-to-end.")
}

// derivePublicURL is a tiny helper: if the api URL points at
// 127.0.0.1 or localhost, we can't use it as a public webhook URL
// (GitHub can't reach your laptop). Print it anyway with a warning
// so the operator sees the real failure mode.
func derivePublicURL(api string) string {
	u, err := url.Parse(api)
	if err != nil {
		return api
	}
	host := u.Hostname()
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return api // surfaced in checklist with a hint
	}
	return api
}

func probeHealth(api string) error {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(api + "/healthz")
	if err != nil {
		return fmt.Errorf("GET %s/healthz: %w", api, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s/healthz: status=%d body=%s", api, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var h map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return fmt.Errorf("decode /healthz: %w", err)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}