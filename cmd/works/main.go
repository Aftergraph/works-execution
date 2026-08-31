// Command works is the developer CLI for works-execution.
//
// Subcommands:
//
//	works init    Create a works.yaml config in the current directory.
//	works run     Submit a work defined in works.yaml to the control plane.
//	works status  Poll a work by ID until terminal state.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

const usage = `works — developer CLI for works-execution

Usage:
  works init [--out works.yaml]
  works run --config works.yaml [--api http://127.0.0.1:8080] [--idempotency-key KEY]
  works status <work_id> [--api http://127.0.0.1:8080] [--follow]
  works runners [--pool NAME] [--alive] [--api URL]   # BYOC: list scheduler-visible runners
  works onboard <owner/repo> [--pool NAME] [--api URL] # design-partner day-1 checklist
  works connect github [--api URL]    # M1 k-impl-024: print webhook URL + secret + repo-policy hint
  works pilot <owner/repo> [--ref REF] [--sha SHA] [--api URL] [--timeout-s N] [--once]
                                     # M1 k-impl-024: submit work for repo, poll until terminal, print timeline

Environment:
  WORKS_API                default control plane URL (default http://127.0.0.1:8080)
  WORKS_WEBHOOK_URL        public works-api URL to register as a webhook target (used by 'connect github')
  WORKS_GITHUB_TOKEN       PAT with repo:status scope (used by 'connect github' to read repo info)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		initCmd(os.Args[2:])
	case "run":
		runCmd(os.Args[2:])
	case "status":
		statusCmd(os.Args[2:])
	case "runners":
		runnersCmd(os.Args[2:])
	case "onboard":
		onboardCmd(os.Args[2:])
	case "connect":
		connectCmd(os.Args[2:])
	case "pilot":
		pilotCmd(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

// --- init -------------------------------------------------------------------

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	out := fs.String("out", "works.yaml", "output path")
	_ = fs.Parse(args)
	cfg := defaultConfig()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		fail("marshal: %v", err)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fail("write: %v", err)
	}
	fmt.Printf("wrote %s\n", *out)
}

// defaultConfig is the starter works.yaml matching the pack's example shape.
func defaultConfig() map[string]any {
	return map[string]any{
		"version": 1,
		"work": map[string]any{
			"verify": map[string]any{
				"triggers": []string{"pull_request"},
				"requirements": map[string]any{
					"confidence": "development",
					"os":         "linux",
					"arch":       "amd64",
				},
				"nodes": map[string]any{
					"hello": map[string]any{
						"run":   "echo 'hello from works-execution' && uname -a",
						"cache": true,
					},
				},
			},
		},
	}
}

// --- run --------------------------------------------------------------------

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "works.yaml", "config file")
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
	idem := fs.String("idempotency-key", "", "idempotency key (optional)")
	_ = fs.Parse(args)

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		fail("read config: %v", err)
	}
	w, err := configToWork(raw)
	if err != nil {
		fail("parse config: %v", err)
	}
	if *idem != "" {
		w.IdempotencyKey = *idem
	}
	w.CorrelationID = workgraph.NewID("cor")

	body, _ := json.Marshal(w)
	resp, err := http.Post(*api+"/v1/works", "application/json", bytes.NewReader(wireCreate(body)))
	if err != nil {
		fail("POST /v1/works: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		fail("create: %s: %s", resp.Status, string(respBody))
	}
	var created workgraph.Work
	if err := json.Unmarshal(respBody, &created); err != nil {
		fail("decode response: %v", err)
	}
	fmt.Printf("submitted work %s (state=%s)\n", created.ID, created.State)
	fmt.Printf("track with: works status %s --follow\n", created.ID)
}

// wireCreate wraps a Work JSON payload with a `queue:true` field at the top
// level. The API expects the Work fields to be flat with `queue` as a
// sibling (see createBody in services/api/api.go), so we inject the queue
// flag into the Work's JSON object directly.
func wireCreate(workJSON []byte) []byte {
	// workJSON is `{"id":...,"source":...,...}`. We want
	// `{"id":...,"source":...,"queue":true,...}`. Easiest: insert "queue":true
	// right after the opening brace.
	out := make([]byte, 0, len(workJSON)+16)
	out = append(out, '{')
	// Skip the opening '{'
	rest := workJSON[1:]
	// Find the first key:value pair boundary (end of first quoted key)
	// Insert before it. Simplest correct approach: inject "queue":true after
	// the opening brace.
	out = append(out, []byte(`"queue":true,`)...)
	out = append(out, rest...)
	return out
}

// configToWork maps a works.yaml document to a workgraph.Work.
func configToWork(raw []byte) (*workgraph.Work, error) {
	var doc struct {
		Version int                       `yaml:"version"`
		Work    map[string]yamlWorkConfig `yaml:"work"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if len(doc.Work) == 0 {
		return nil, fmt.Errorf("config has no work entries")
	}
	// Pick the (only) work for V1; multi-work support comes later.
	var name string
	for k := range doc.Work {
		name = k
		break
	}
	c := doc.Work[name]

	nodes := map[string]workgraph.Node{}
	for nName, n := range c.Nodes {
		nodes[nName] = workgraph.Node{
			ID:       nName,
			Run:      n.Run,
			Needs:    n.Needs,
			Cache:    n.Cache,
			Env:      n.Env,
			TimeoutS: n.TimeoutS,
		}
	}

	w := &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Source: workgraph.Source{
			Type:       "cli",
			Repository: filepath.Base(mustCwd()),
			Revision:   "HEAD",
		},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: nodes,
		},
		Requirements: workgraph.Requirements{
			OS:         c.Requirements.OS,
			Arch:       c.Requirements.Arch,
			Confidence: c.Requirements.Confidence,
		},
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return w, nil
}

type yamlWorkConfig struct {
	Triggers     []string                 `yaml:"triggers"`
	Requirements yamlRequirements         `yaml:"requirements"`
	Nodes        map[string]yamlNodeConfig `yaml:"nodes"`
}

type yamlRequirements struct {
	OS         string `yaml:"os"`
	Arch       string `yaml:"arch"`
	Confidence string `yaml:"confidence"`
}

type yamlNodeConfig struct {
	Run      string            `yaml:"run"`
	Needs    []string          `yaml:"needs"`
	Cache    bool              `yaml:"cache"`
	Env      map[string]string `yaml:"env"`
	TimeoutS int               `yaml:"timeout_s"`
}

// --- status -----------------------------------------------------------------

func statusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
	follow := fs.Bool("follow", false, "poll until terminal state")
	// Split flag-style args from positional args. Each known flag consumes the
	// next arg as its value (for --api and --idempotency-key style). Anything
	// starting with "-" that isn't recognized ends up in positional, which is
	// a bug we'll surface as a usage error.
	var positional []string
	var flagArgs []string
	knownFlags := map[string]bool{"--api": true, "-api": true, "--follow": true, "-follow": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if knownFlags[a] {
			flagArgs = append(flagArgs, a)
			// consume value for non-bool flags
			if a == "--api" || a == "-api" {
				if i+1 < len(args) {
					flagArgs = append(flagArgs, args[i+1])
					i++
				}
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a) // let flag.Parse error
			continue
		}
		positional = append(positional, a)
	}
	_ = fs.Parse(flagArgs)
	rest := positional
	if len(rest) != 1 {
		fail("usage: works status <work_id> [--follow] [--api URL]")
	}
	id := rest[0]

	if !*follow {
		printStatus(*api, id)
		return
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		terminal := printStatus(*api, id)
		if terminal {
			return
		}
		time.Sleep(1 * time.Second)
	}
	fmt.Fprintln(os.Stderr, "timeout: work did not reach terminal state in 2 minutes")
	os.Exit(1)
}

func printStatus(api, id string) bool {
	resp, err := http.Get(api + "/v1/works/" + id)
	if err != nil {
		fail("GET work: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintln(os.Stderr, "work not found")
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		fail("status: %s", resp.Status)
	}
	var w workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		fail("decode: %v", err)
	}
	fmt.Printf("work %s state=%s attempts=%d evidence=%d artifacts=%d\n",
		w.ID, w.State, len(w.Attempts), len(w.Evidence), len(w.Artifacts))
	for _, a := range w.Attempts {
		fmt.Printf("  attempt %s node=%s status=%s exit=%d\n", a.ID, a.NodeID, a.Status, a.ExitCode)
	}
	for _, e := range w.Evidence {
		fmt.Printf("  evidence %s node=%s type=%s result=%s\n", e.ID, e.NodeID, e.Type, e.Result)
	}
	return w.State.IsTerminal()
}

// --- helpers ----------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustCwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "works: "+format+"\n", args...)
	os.Exit(1)
}