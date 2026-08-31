// Command works-ci is the self-hosted CI driver for the
// works-execution repository itself (dogfooding, RFC-0006).
//
// It submits the pipeline defined in works.yml as a single Work (the
// DAG: vet → test → build), waits for a terminal state, and exits
// with the pipeline's verdict. GitHub Actions is NOT involved: the
// control plane receives the push webhook, this binary (invoked by an
// operator, a cron job, or the webhook processing path) drives the
// run against the avc-core pool.
//
// Usage:
//
//	works-ci run [--config works.yml] [--api URL] [--enroll-secret S] [--timeout-s N]
//	works-ci watch <work_id> [...]           # re-attach to a running pipeline
//
// Exit codes: 0 pipeline SUCCEEDED, 1 FAILED/CANCELLED/timeout, 2 usage.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/pipeline"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

const usage = `works-ci — self-hosted CI driver for works-execution

Usage:
  works-ci run [--config works.yml] [--api URL] [--enroll-secret S] [--timeout-s N]
  works-ci watch <work_id> [--api URL] [--enroll-secret S] [--timeout-s N]

The pipeline shape (works.yml) maps to a workgraph.Work: each node
becomes a graph node with ` + "`needs`" + ` edges preserved. The work is
submitted with requirements.pool=avc-core so it runs exclusively on
the avc-core BYOC pool (RFC-0004).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		runPipeline(os.Args[2:])
	case "watch":
		if len(os.Args) < 3 {
			fmt.Fprint(os.Stderr, "usage: works-ci watch <work_id>\n")
			os.Exit(2)
		}
		watchCmd(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "works-ci: unknown subcommand %q\n", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// configToWork converts the pipeline document into a Work. Mirrors
// cmd/works.configToWork but keeps pool + cache fields intact.
func configToWork(raw []byte) (*workgraph.Work, error) {
	return pipeline.Parse(raw)
}

// runPipeline reads the config, submits the work, waits for terminal.
func runPipeline(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "works.yml", "pipeline config")
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
	enroll := fs.String("enroll-secret", envOr("WORKS_ENROLL_SECRET", ""), "enrollment secret")
	timeoutS := fs.Int("timeout-s", 900, "max seconds to wait")
	_ = fs.Parse(args)

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		fail("read config: %v", err)
	}
	w, err := configToWork(raw)
	if err != nil {
		fail("config: %v", err)
	}

	// Stamp the commit we're verifying (best-effort; works locally
	// and on the VDS where git is available).
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		w.Source.SHA = strings.TrimSpace(string(out))
	}
	w.CorrelationID = workgraph.NewID("cor")

	// Submit via the same auth path as the works CLI.
	auth, err := newAuthFor(*api, *enroll)
	if err != nil {
		fail("auth: %v", err)
	}
	var created struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if _, err := auth.postJSON("/v1/works", map[string]any{
		"queue":         true,
		"source":        w.Source,
		"objective":     w.Objective,
		"requirements":  w.Requirements,
		"policy":        w.Policy,
		"graph":         w.Graph,
		"correlation_id": w.CorrelationID,
	}, &created); err != nil {
		fail("submit: %v", err)
	}
	fmt.Printf("works-ci: submitted %s (pool=%q sha=%.8s)\n", created.ID, w.Requirements.Pool, w.Source.SHA)

	waitTerminal(auth, created.ID, *timeoutS)
}

// watchCmd re-attaches to an existing work.
func watchCmd(id string) {
	api := envOr("WORKS_API", "http://127.0.0.1:8080")
	enroll := envOr("WORKS_ENROLL_SECRET", "")
	timeoutS := 900

	auth, err := newAuthFor(api, enroll)
	if err != nil {
		fail("auth: %v", err)
	}
	waitTerminal(auth, id, timeoutS)
}

// waitTerminal polls until terminal state, prints the timeline, and
// exits with the pipeline's verdict.
func waitTerminal(auth *apiAuth, id string, timeoutS int) {
	t0 := time.Now()
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)
	var prev workgraph.State = ""
	for {
		var w workgraph.Work
		if _, err := auth.getJSON("/v1/works/"+id, &w); err != nil {
			fmt.Fprintln(os.Stderr, "works-ci:", err)
		} else if w.State != prev {
			fmt.Printf("t=%-7.2fs  %s -> %s (attempts=%d)\n",
				time.Since(t0).Seconds(), prev, w.State, len(w.Attempts))
			prev = w.State
			for _, e := range w.Evidence {
				fmt.Printf("           evidence %s: %s %s\n", e.NodeID, e.Type, e.Result)
			}
			if w.State.IsTerminal() {
				if w.State == workgraph.StateSucceeded {
					fmt.Printf("works-ci: PASS (%s)\n", time.Since(t0).Round(time.Millisecond))
					os.Exit(0)
				}
				fmt.Printf("works-ci: FAIL (%s after %s)\n", w.State, time.Since(t0).Round(time.Millisecond))
				os.Exit(1)
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "works-ci: TIMEOUT after %ds (last state=%s)\n", timeoutS, prev)
			os.Exit(1)
		}
		time.Sleep(1 * time.Second)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "works-ci: "+format+"\n", args...)
	os.Exit(1)
}