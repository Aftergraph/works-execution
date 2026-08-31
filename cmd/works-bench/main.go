// Command works-bench measures WORKS control-plane latency in two
// modes — subprocess (slice-1+2 path) and Docker (slice-5 path) —
// and writes both a machine-readable JSON report and a human-
// readable Markdown report to docs/benchmarks/.
//
// Scope: the Actions-vs-WORKS benchmark from k-impl-023 requires the
// Check Run publisher to drive a real Actions workflow, so it stays
// parked until GitHub App credentials exist. This binary implements
// the WORKS-subprocess vs WORKS-Docker comparison today, which is
// the more useful internal baseline (it tells us how much the Docker
// hermetic path costs vs the host-subprocess path).
//
// Usage:
//
//	works-bench run [--api URL] [--iterations N] [--out-dir DIR]
//	                [--warm] [--work-spec PATH]
//
// Exit codes: 0 on success, 1 on any iteration FAILED, 2 on usage.
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
	"sort"
	"sync"
	"time"
)

const usage = `works-bench — WORKS control-plane benchmark

Usage:
  works-bench run [--api URL] [--iterations N] [--out-dir DIR]
                  [--warm] [--work-spec PATH]

Measures cold/warm end-to-end latency for the same work executed via
two WORKS backends (subprocess and Docker), then writes both a JSON
report and a Markdown summary to <out-dir>/.

Environment:
  WORKS_API   default control plane URL (default http://127.0.0.1:8080)
`

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "works-api URL")
	iter := fs.Int("iterations", 5, "iterations per backend")
	outDir := fs.String("out-dir", "docs/benchmarks", "report output directory")
	warm := fs.Bool("warm", false, "include warm-cache runs (default: cold only)")
	_ = fs.Parse(os.Args[2:])

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail("mkdir %s: %v", *outDir, err)
	}

	fmt.Printf("works-bench: api=%s iterations=%d out=%s warm=%v\n",
		*api, *iter, *outDir, *warm)

	backends := []backend{
		{Name: "subprocess", Spec: subprocessWorkSpec()},
		{Name: "docker", Spec: dockerWorkSpec()},
	}

	var allRuns []runResult
	for _, b := range backends {
		fmt.Printf("\n--- backend: %s ---\n", b.Name)
		// Cold runs first.
		for i := 0; i < *iter; i++ {
			r := runOne(*api, b.Name, b.Spec, "cold")
			printResult(os.Stdout, r)
			allRuns = append(allRuns, r)
		}
		// Optional warm runs (each re-submits the same spec; cache
		// may or may not apply depending on backend config).
		if *warm {
			for i := 0; i < *iter; i++ {
				r := runOne(*api, b.Name+"-warm", b.Spec, "warm")
				printResult(os.Stdout, r)
				allRuns = append(allRuns, r)
			}
		}
	}

	ts := time.Now().UTC().Format("20060102-150405")
	report := summarize(allRuns)
	report.APIBase = *api
	jsonPath := filepath.Join(*outDir, "m1-"+ts+".json")
	mdPath := filepath.Join(*outDir, "m1-"+ts+".md")
	if err := writeJSON(jsonPath, report); err != nil {
		fail("write json: %v", err)
	}
	if err := writeMarkdown(mdPath, report); err != nil {
		fail("write markdown: %v", err)
	}
	// Also write a 'latest' symlink so dashboards can pick it up
	// without globbing the timestamp.
	_ = os.Remove(filepath.Join(*outDir, "latest.json"))
	_ = os.Remove(filepath.Join(*outDir, "latest.md"))
	_ = os.Symlink(filepath.Base(jsonPath), filepath.Join(*outDir, "latest.json"))
	_ = os.Symlink(filepath.Base(mdPath), filepath.Join(*outDir, "latest.md"))

	fmt.Printf("\nWrote %s\nWrote %s\n", jsonPath, mdPath)

	// Exit non-zero if any iteration FAILED.
	for _, r := range allRuns {
		if !r.OK {
			os.Exit(1)
		}
	}
}

type backend struct {
	Name string
	Spec map[string]any
}

type runResult struct {
	Backend  string        `json:"backend"`
	Phase    string        `json:"phase"` // cold | warm
	WorkID   string        `json:"work_id"`
	OK       bool          `json:"ok"`
	TotalMS  int64         `json:"total_ms"`
	QueueMS  int64         `json:"queue_ms"`
	ExecMS   int64         `json:"exec_ms"`
	Terminal string        `json:"terminal_state"`
	Err      string        `json:"err,omitempty"`
	RunStart time.Time     `json:"run_start"`
	RunEnd   time.Time     `json:"run_end"`
	Exec     time.Duration `json:"-"`
}

type report struct {
	StartedAt time.Time  `json:"started_at"`
	APIBase   string     `json:"api_base"`
	Runs      []runResult `json:"runs"`
	Summaries map[string]backendSummary `json:"summaries"`
}

type backendSummary struct {
	N     int     `json:"n"`
	OK    int     `json:"ok"`
	Mean  float64 `json:"mean_ms"`
	Min   float64 `json:"min_ms"`
	Max   float64 `json:"max_ms"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
}

// runOne submits a single work, polls until terminal, and returns the
// measured latencies. Returns a runResult with OK=false on failure.
func runOne(api, backendName string, spec map[string]any, phase string) runResult {
	start := time.Now().UTC()
	res := runResult{
		Backend:  backendName,
		Phase:    phase,
		RunStart: start,
	}

	// POST.
	body, _ := json.Marshal(spec)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Post(api+"/v1/works", "application/json", bytes.NewReader(body))
	if err != nil {
		res.Err = "POST: " + err.Error()
		res.RunEnd = time.Now().UTC()
		res.TotalMS = time.Since(start).Milliseconds()
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		res.Err = fmt.Sprintf("POST status=%d body=%s", resp.StatusCode, string(b))
		res.RunEnd = time.Now().UTC()
		res.TotalMS = time.Since(start).Milliseconds()
		return res
	}
	var created struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		res.Err = "decode POST response: " + err.Error()
		res.RunEnd = time.Now().UTC()
		res.TotalMS = time.Since(start).Milliseconds()
		return res
	}
	res.WorkID = created.ID

	// Poll.
	pollStart := time.Now()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		<-tick.C
		if time.Now().After(deadline) {
			res.Err = "poll: timeout"
			res.RunEnd = time.Now().UTC()
			res.TotalMS = time.Since(start).Milliseconds()
			return res
		}
		w, err := fetchWork(api, res.WorkID)
		if err != nil {
			continue // transient; keep polling
		}
		switch w.State {
		case "SUCCEEDED":
			res.OK = true
			res.Terminal = w.State
			res.Exec = time.Since(pollStart)
			res.ExecMS = res.Exec.Milliseconds()
			res.TotalMS = time.Since(start).Milliseconds()
			res.QueueMS = res.TotalMS - res.ExecMS
			res.RunEnd = time.Now().UTC()
			return res
		case "FAILED", "CANCELLED":
			res.OK = false
			res.Terminal = w.State
			res.Exec = time.Since(pollStart)
			res.ExecMS = res.Exec.Milliseconds()
			res.TotalMS = time.Since(start).Milliseconds()
			res.QueueMS = res.TotalMS - res.ExecMS
			res.RunEnd = time.Now().UTC()
			return res
		}
	}
}

type workResp struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func fetchWork(api, id string) (*workResp, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(api + "/v1/works/" + id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status=%d", resp.StatusCode)
	}
	var w workResp
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return nil, err
	}
	return &w, nil
}

func summarize(rs []runResult) report {
	r := report{
		StartedAt: time.Now().UTC(),
		Summaries: map[string]backendSummary{},
	}
	for _, x := range rs {
		r.Runs = append(r.Runs, x)
	}
	// Per-backend (merge cold+warm).
	groups := map[string][]runResult{}
	for _, x := range rs {
		// Use base backend name (strip "-warm" suffix) for the summary.
		base := x.Backend
		if x.Phase == "warm" {
			base = x.Backend // already includes the suffix; we keep them separate.
		}
		groups[base] = append(groups[base], x)
	}
	for name, xs := range groups {
		s := backendSummary{N: len(xs)}
		var totalMS []float64
		for _, x := range xs {
			if x.OK {
				s.OK++
			}
			totalMS = append(totalMS, float64(x.TotalMS))
		}
		if len(totalMS) > 0 {
			s.Min, s.Max = totalMS[0], totalMS[0]
			var sum float64
			for _, v := range totalMS {
				sum += v
				if v < s.Min {
					s.Min = v
				}
				if v > s.Max {
					s.Max = v
				}
			}
			s.Mean = sum / float64(len(totalMS))
			sort.Float64s(totalMS)
			s.P50 = totalMS[len(totalMS)*50/100]
			s.P95 = totalMS[min(len(totalMS)*95/100, len(totalMS)-1)]
		}
		r.Summaries[name] = s
	}
	return r
}

func printResult(w io.Writer, r runResult) {
	status := "OK"
	if !r.OK {
		status = "FAIL"
		if r.Err != "" {
			status = "FAIL(" + r.Err + ")"
		}
	}
	fmt.Fprintf(w, "  [%s] %s/%-8s work=%s total=%dms exec=%dms terminal=%s\n",
		status, r.Backend, r.Phase, r.WorkID, r.TotalMS, r.ExecMS, r.Terminal)
}

func writeJSON(p string, r report) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func writeMarkdown(p string, r report) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# M1 benchmark report\n\n")
	fmt.Fprintf(&buf, "Generated: %s\n\n", r.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&buf, "API: %s\n\n", r.APIBase)
	fmt.Fprintf(&buf, "## Summary\n\n")
	fmt.Fprintf(&buf, "| backend | n | ok | mean ms | p50 ms | p95 ms | min ms | max ms |\n")
	fmt.Fprintf(&buf, "|---|---|---|---|---|---|---|---|\n")
	names := make([]string, 0, len(r.Summaries))
	for k := range r.Summaries {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		s := r.Summaries[n]
		fmt.Fprintf(&buf, "| %s | %d | %d | %.1f | %.1f | %.1f | %.1f | %.1f |\n",
			n, s.N, s.OK, s.Mean, s.P50, s.P95, s.Min, s.Max)
	}
	fmt.Fprintf(&buf, "\n## Individual runs\n\n")
	fmt.Fprintf(&buf, "| backend | phase | work_id | total_ms | exec_ms | terminal | ok |\n")
	fmt.Fprintf(&buf, "|---|---|---|---|---|---|---|\n")
	for _, x := range r.Runs {
		fmt.Fprintf(&buf, "| %s | %s | %s | %d | %d | %s | %v |\n",
			x.Backend, x.Phase, x.WorkID, x.TotalMS, x.ExecMS, x.Terminal, x.OK)
	}
	return os.WriteFile(p, buf.Bytes(), 0o644)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "works-bench: "+format+"\n", args...)
	os.Exit(1)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// unused but referenced via time.Time format strings
var _ = time.RFC3339
var _ = sync.Mutex{}