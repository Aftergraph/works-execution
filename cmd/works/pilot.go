package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// pilotCmd handles `works pilot <owner/repo>`. It submits a Work
// referencing the given repo, polls for terminal state, and prints
// a timeline.
//
// Two flows:
//
//   1. GitHub-driven: when the repo has the WORKS webhook configured,
//      a push/PR on that repo triggers a Work. `works pilot` here is
//      just a status poller that watches the API for terminal
//      Works associated with that repo. It exits when ANY terminal
//      work for the repo appears (with --timeout) or immediately
//      after the first poll if --once is set.
//
//   2. CLI-driven: when the repo does NOT have a webhook configured,
//      `works pilot` constructs a Work locally and POSTs it. The
//      control plane then runs the work end-to-end against the
//      repo's local clone (services/source + services/runner).
//
// The flag `--expect-webhook` forces flow 1; the default is flow 2
// for the operator-friendly "make it work right now" path.
func pilotCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, "usage: works pilot <owner/repo> [--ref REF] [--sha SHA] [--api URL] [--timeout-s N] [--once] [--expect-webhook]\n")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("pilot", flag.ExitOnError)
	repo := fs.String("repo", args[0], "owner/name (positional)")
	ref := fs.String("ref", "", "git ref to check out (e.g. refs/heads/main)")
	sha := fs.String("sha", "", "commit SHA to check out (40 hex chars)")
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "works-api URL")
	timeoutS := fs.Int("timeout-s", 300, "max seconds to wait for terminal state")
	once := fs.Bool("once", false, "poll exactly once and exit (no waiting)")
	expectWebhook := fs.Bool("expect-webhook", false, "do NOT submit a work; instead, watch for webhook-triggered work to appear")
	_ = fs.Parse(args[1:])

	if !strings.Contains(*repo, "/") {
		fmt.Fprintf(os.Stderr, "works pilot: --repo must be in owner/name form, got %q\n", *repo)
		os.Exit(2)
	}
	owner, name := splitOwnerrepo(*repo)

	t0 := time.Now()
	if *expectWebhook {
		pilotWebhookFlow(*api, *repo, *timeoutS, *once, t0)
		return
	}
	pilotSubmitFlow(*api, owner, name, *ref, *sha, *timeoutS, *once, t0)
}

// pilotSubmitFlow constructs and POSTs a Work for the given repo.
// Returns exit 0 on SUCCEEDED, 1 on FAILED/CANCELLED/timeout.
func pilotSubmitFlow(api, owner, name, ref, sha string, timeoutS int, once bool, t0 time.Time) {
	body := map[string]any{
		"queue": true,
		"source": map[string]any{
			"type":       "cli_pilot",
			"repository": owner + "/" + name,
			"actor":      "works pilot",
		},
		"objective": map[string]any{"type": "verify_change"},
		"requirements": map[string]any{
			"os":         "linux",
			"arch":       "amd64",
			"confidence": "development",
		},
		"policy": map[string]any{"trust_class": "standard", "production_access": true},
		"graph": map[string]any{
			"nodes": map[string]any{
				"verify": map[string]any{
					"id":        "verify",
					"run":       "echo pilot-verify && uname -a",
					"timeout_s": 60,
				},
			},
		},
	}
	if ref != "" {
		body["source"].(map[string]any)["ref"] = ref
	}
	if sha != "" {
		body["source"].(map[string]any)["sha"] = sha
	}
	buf, _ := json.Marshal(body)
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Post(api+"/v1/works", "application/json", bytes.NewReader(buf))
	if err != nil {
		fmt.Fprintln(os.Stderr, "works pilot: POST /v1/works:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "works pilot: POST /v1/works: status=%d body=%s\n", resp.StatusCode, string(b))
		os.Exit(1)
	}
	var created struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	fmt.Printf("t=%-7.2fs  POST /v1/works             201  id=%s repo=%s/%s\n",
		time.Since(t0).Seconds(), created.ID, owner, name)
	pollUntilTerminal(api, created.ID, timeoutS, once, t0)
}

// pilotWebhookFlow polls /v1/works for any terminal work whose
// Source.Repository matches the requested repo. Used when the
// operator wants to verify the webhook round-trip without
// submitting a CLI work of their own.
func pilotWebhookFlow(api, repo string, timeoutS int, once bool, t0 time.Time) {
	fmt.Printf("works pilot: watching %s for webhook-triggered terminal work (timeout=%ds)\n",
		repo, timeoutS)
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	for {
		if !once && time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "works pilot: TIMEOUT after %ds, no terminal work seen for %s\n", timeoutS, repo)
			os.Exit(1)
		}
		w, err := findTerminalWorkForRepo(api, repo)
		if err != nil {
			fmt.Fprintln(os.Stderr, "works pilot:", err)
		} else if w != nil {
			fmt.Printf("t=%-7.2fs  work %s reached %s for %s\n",
				time.Since(t0).Seconds(), w.ID, w.State, repo)
			fmt.Println("Result: PASS (webhook round-trip OK)")
			os.Exit(0)
		}
		if once {
			fmt.Println("works pilot: --once set; no terminal work found yet")
			os.Exit(2)
		}
		time.Sleep(2 * time.Second)
	}
}

type workResponse struct {
	ID     string                `json:"id"`
	State  workgraph.State       `json:"state"`
	Source workgraph.Source      `json:"source"`
}

func findTerminalWorkForRepo(api, repo string) (*workResponse, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(api + "/v1/works?limit=50")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/works: status=%d", resp.StatusCode)
	}
	var list struct {
		Works []workResponse `json:"works"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	for i := range list.Works {
		w := list.Works[i]
		if w.Source.Repository != repo {
			continue
		}
		if w.State.IsTerminal() {
			return &w, nil
		}
	}
	return nil, nil
}

// pollUntilTerminal polls GET /v1/works/{id} every 1s until terminal
// state or timeout. Mirrors the works-pilot run-demo output format.
func pollUntilTerminal(api, id string, timeoutS int, once bool, t0 time.Time) {
	if once {
		printStatusOnce(api, id)
		return
	}
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	var prev workgraph.State = ""
	for {
		w, err := fetchWork(api, id)
		if err != nil {
			fmt.Fprintln(os.Stderr, "works pilot:", err)
		} else if w != nil && w.State != prev {
			fmt.Printf("t=%-7.2fs  state %s -> %s\n",
				time.Since(t0).Seconds(), prev, w.State)
			prev = w.State
			if w.State.IsTerminal() {
				fmt.Println()
				fmt.Printf("Time-to-first-successful-work: %s\n", time.Since(t0))
				if w.State == workgraph.StateSucceeded {
					fmt.Println("Result: PASS")
					os.Exit(0)
				}
				fmt.Printf("Result: FAIL (work reached %s)\n", w.State)
				os.Exit(1)
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "works pilot: TIMEOUT after %ds, last state=%s\n", timeoutS, prev)
			os.Exit(1)
		}
		time.Sleep(1 * time.Second)
	}
}

func fetchWork(api, id string) (*workResponse, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(api + "/v1/works/" + id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("work %s not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/works/%s: status=%d", id, resp.StatusCode)
	}
	var w workResponse
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return nil, err
	}
	return &w, nil
}

func printStatusOnce(api, id string) {
	w, err := fetchWork(api, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "works pilot:", err)
		os.Exit(1)
	}
	fmt.Printf("work %s state=%s\n", w.ID, w.State)
}

func splitOwnerrepo(s string) (string, string) {
	i := strings.Index(s, "/")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}