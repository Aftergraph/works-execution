package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
)

// onboardCmd handles `works onboard <owner/repo>` — the design-
// partner day-1 flow (90-day plan: "Make first successful Work
// consistently <5 minutes from onboarding").
//
// It prints the exact, ordered checklist for connecting a repo to
// this works deployment:
//
//	1. webhook registration values (URL, secret hint, events)
//	2. BYOC pool provisioning (pool name + worker bootstrap command)
//	3. pipeline config (works.yml) to commit to the partner's repo
//	4. a smoke test that proves the round-trip end-to-end
//
// The command is intentionally printable/runbook-style: design
// partner onboarding is a guided session, not a magic one-shot API
// call (the partner's GitHub admin must paste the webhook anyway).
func onboardCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, "usage: works onboard <owner/repo> [--pool NAME] [--api URL]\n")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("onboard", flag.ExitOnError)
	repo := args[0]
	pool := fs.String("pool", "partner-"+owner(repo), "BYOC pool name for this partner (RFC-0004)")
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "works-api public URL")
	_ = fs.Parse(args[1:])

	webhookURL := envOr("WORKS_WEBHOOK_URL", *api)
	secret := envOr("WORKS_WEBHOOK_SECRET", "")

	fmt.Printf("WORKS onboarding — %s\n", repo)
	fmt.Println("Target time-to-first-green: <5 minutes. Follow steps in order.")
	fmt.Println()

	fmt.Println("── 1. GitHub webhook (repo admin, ~1 min) ─────────────────")
	fmt.Printf("   Payload URL : %s/v1/webhook/github\n", webhookURL)
	fmt.Println("   Content type: application/json")
	fmt.Println("   Events      : push, pull_request (nothing else)")
	if secret != "" {
		fmt.Printf("   Secret      : %s…%s (in WORKS_WEBHOOK_SECRET on the works-api)\n",
			secret[:minInt(8, len(secret))], secret[maxInt(0, len(secret)-4):])
	} else {
		fmt.Println("   Secret      : value of WORKS_WEBHOOK_SECRET on the works-api")
	}
	fmt.Println()

	fmt.Println("── 2. BYOC compute pool (RFC-0004, ~2 min) ────────────────")
	fmt.Printf("   Pool name   : %s\n", *pool)
	fmt.Println("   Provision a worker inside the partner's network:")
	fmt.Printf("     works-worker -api %s -enroll-secret $WORKS_ENROLL_SECRET \\\n", *api)
	fmt.Printf("       -id partner-%s-1 -pool %s -lease-ttl 25s -heartbeat 10s\n", owner(repo), *pool)
	fmt.Println("   (run under systemd/supervisor; the worker self-registers")
	fmt.Println("    and heartbeats — verify with: works runners --pool " + *pool + ")")
	fmt.Println()

	fmt.Println("── 3. Pipeline config (~1 min) ────────────────────────────")
	fmt.Printf("   Commit this as works.yml on %s's default branch:\n\n", repo)
	example := "version: 1\n\nwork:\n  verify:\n    triggers: [push, pull_request]\n    requirements:\n      os: linux\n      arch: amd64\n      pool: " + *pool + "\n    nodes:\n      verify:\n        run: echo hello from WORKS\n        cache: true\n        timeout_s: 60\n"
	fmt.Println(indent(example, "   "))
	fmt.Println("   The pool line pins this repo's work to the partner's")
	fmt.Println("   workers (isolation). cache:true makes identical re-runs")
	fmt.Println("   instant (RFC-0005).")
	fmt.Println()

	fmt.Println("── 4. Smoke test (~1 min) ─────────────────────────────────")
	fmt.Println("   Push any commit (or reopen a PR). Then:")
	fmt.Printf("     works pilot %s --expect-webhook --api %s --timeout-s 120\n", repo, *api)
	fmt.Println("   Expected: 'Result: PASS (webhook round-trip OK)' and a")
	fmt.Println("   works-execution status on the commit in GitHub.")
	fmt.Println()

	fmt.Println("── Onboarding checklist summary ───────────────────────────")
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  [ ] webhook added\tevents: push+pull_request")
	fmt.Fprintln(w, "  [ ] worker running\tpool: "+*pool)
	fmt.Fprintln(w, "  [ ] works.yml committed\twith pool: "+*pool)
	fmt.Fprintln(w, "  [ ] works pilot PASS\tstatus visible on commit")
	w.Flush()
}

func owner(repo string) string {
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			return repo[:i]
		}
	}
	return repo
}

func indent(s, prefix string) string {
	out := ""
	for _, line := range splitLines(s) {
		out += prefix + line + "\n"
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
