// Command works-pilot is the time-to-first-successful-work measurement
// tool. It submits a representative work to the control plane, polls for
// state transitions, and reports the wall-clock time from POST to
// terminal state vs the pack's <5 minute V1 target.
//
// Usage:
//
//	works-pilot run-demo [--api URL] [--timeout-s N]
//
// Exit code: 0 if the work reaches SUCCEEDED within the timeout, 1
// otherwise. 2 on usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const defaultAPI = "http://127.0.0.1:8080"
const defaultTimeout = 300 * time.Second
const targetV1 = 300 * time.Second // pack's <5min V1 goal

type createBody struct {
	Queue    bool                   `json:"queue"`
	Source   map[string]any         `json:"source"`
	Objective map[string]any        `json:"objective"`
	Graph    map[string]any         `json:"graph"`
	Requirements map[string]any     `json:"requirements"`
	Policy   map[string]any         `json:"policy"`
}

type workResponse struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run-demo":
		runDemo(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "works-pilot: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `works-pilot — time-to-first-successful-work

Usage:
  works-pilot run-demo [--api URL] [--timeout-s N]

Submits a 2-node work (one host subprocess, one alpine:3.20 container)
to the control plane and measures the wall-clock time to SUCCEEDED.

Exit codes:
  0  work reached SUCCEEDED within the timeout
  1  work did not reach a terminal state within the timeout
  2  usage error
`)
}

func runDemo(args []string) {
	fs := flag.NewFlagSet("run-demo", flag.ExitOnError)
	api := fs.String("api", defaultAPI, "control plane base URL")
	timeout := fs.Duration("timeout", defaultTimeout, "max time to wait for SUCCEEDED")
	_ = fs.Parse(args)

	// Build a 2-node work: one host subprocess, one alpine:3.20 container.
	// The host node demonstrates the slice-1+2 path; the alpine node
	// demonstrates the slice-5 docker path.
	body := createBody{
		Queue: true,
		Source: map[string]any{
			"type":       "pilot",
			"repository": "works-execution/pilot",
		},
		Objective: map[string]any{"type": "verify_change"},
		Requirements: map[string]any{"os": "linux", "arch": "amd64"},
		Policy: map[string]any{"trust_class": "standard"},
		Graph: map[string]any{
			"nodes": map[string]any{
				"hello": map[string]any{
					"id":  "hello",
					"run": "echo pilot-ready && uname -a",
				},
				"alpine": map[string]any{
					"id":      "alpine",
					"run":     "echo alpine-ready",
					"timeout_s": 60,
					"runtime": map[string]any{
						"image":   "alpine:3.20",
						"command": "echo alpine-ready",
					},
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	httpc := &http.Client{Timeout: 10 * time.Second}

	// POST.
	t0 := time.Now()
	resp, err := httpc.Post(*api+"/v1/works", "application/json", byteReader(bodyBytes))
	if err != nil {
		fmt.Fprintf(os.Stderr, "pilot: POST /v1/works: %v\n", err)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		fmt.Fprintf(os.Stderr, "pilot: POST /v1/works: status=%d body=%s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}
	var w workResponse
	_ = json.NewDecoder(resp.Body).Decode(&w)
	_ = resp.Body.Close()
	fmt.Printf("t=%-7.2fs  POST /v1/works             %d  id=%s\n", time.Since(t0).Seconds(), resp.StatusCode, w.ID)

	// Poll until terminal or timeout.
	deadline := time.Now().Add(*timeout)
	prev := w.State
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case <-time.After(time.Until(deadline)):
			fmt.Printf("pilot: TIMEOUT after %s; last state=%s\n", *timeout, prev)
			os.Exit(1)
		}
		resp, err := httpc.Get(*api + "/v1/works/" + w.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pilot: GET /v1/works/%s: %v\n", w.ID, err)
			continue
		}
		var cur workResponse
		_ = json.NewDecoder(resp.Body).Decode(&cur)
		_ = resp.Body.Close()
		if cur.State != prev {
			fmt.Printf("t=%-7.2fs  state %s -> %s\n", time.Since(t0).Seconds(), prev, cur.State)
			prev = cur.State
		}
		if cur.State == "SUCCEEDED" {
			fmt.Println()
			fmt.Printf("Time-to-first-successful-work: %s\n", time.Since(t0))
			fmt.Printf("Pack V1 target:                 <%s\n", targetV1)
			if time.Since(t0) < targetV1 {
				fmt.Println("Result: PASS (under target)")
				os.Exit(0)
			}
			fmt.Println("Result: SLOW (over target)")
			os.Exit(0)
		}
		if cur.State == "FAILED" || cur.State == "CANCELLED" {
			fmt.Printf("pilot: work ended in %s\n", cur.State)
			os.Exit(1)
		}
	}
}

func byteReader(b []byte) io.Reader { return &bytesReader{b: b} }

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}