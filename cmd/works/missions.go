package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// missionsCmd handles `works missions` (k-037) — lists mission Works from
// GET /v1/ filtered to those carrying the ADR-0008 mission contract
// (mission.budget_ceiling present), ordered by the NOW attention law
// mirrored from packages/shell.NowProjection: WAITING_HUMAN first, then
// RUNNING, then everything else in stable work-id order.
func missionsCmd(args []string) {
	if err := runMissions(args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "works: %v\n", err)
		os.Exit(1)
	}
}

// runMissions is the testable body of missionsCmd: every failure returns
// an error (written to stderr by the caller) instead of os.Exit, so unit
// tests can drive it against an httptest stub.
func runMissions(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("missions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
	limit := fs.Int("limit", 50, "max works to fetch from the control plane")
	jsonOut := fs.Bool("json", false, "print the filtered works as a raw JSON array")
	token := fs.String("token", "", "bearer token (or WORKS_TOKEN env)")
	enroll := fs.String("enroll-secret", "", "enrollment secret (or WORKS_ENROLL_SECRET env)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *limit < 1 {
		return fmt.Errorf("--limit must be >= 1, got %d", *limit)
	}

	auth, err := newCLIAuth(*api, *token, *enroll)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	var raw json.RawMessage
	path := "/v1/works?limit=" + url.QueryEscape(fmt.Sprint(*limit))
	if _, err := auth.getJSON(path, &raw); err != nil {
		return fmt.Errorf("list works: %w", err)
	}
	works, err := parseWorksList(raw)
	if err != nil {
		return err
	}

	missions := filterMissionWorks(works)
	sortMissionsByNow(missions)
	if len(missions) > *limit {
		missions = missions[:*limit]
	}

	if *jsonOut {
		buf, err := json.MarshalIndent(missions, "", "  ")
		if err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
		buf = append(buf, '\n')
		stdout.Write(buf)
		return nil
	}

	if len(missions) == 0 {
		fmt.Fprintln(stdout, "no missions")
		return nil
	}

	w := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "WORK ID\tSTATE\tNEEDS_HUMAN\tOBJECTIVE\tCEILING_EUR")
	for _, mw := range missions {
		needs := ""
		if needsHumanState(mw.State) {
			needs = "yes"
		}
		objective := mw.Objective.Type
		if objective == "" {
			objective = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.2f\n",
			shortID(mw.ID), mw.State, needs, objective, mw.Mission.BudgetCeiling.ComputeEUR)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	fmt.Fprintf(stdout, "\n%d mission(s)\n", len(missions))
	return nil
}

// parseWorksList decodes the GET /v1/works list body. The control plane
// serves the wrapped shape {"works":[...],"count":N} (services/api
// listWorks); a bare array is also accepted defensively so the CLI keeps
// working against older or minimal stub servers.
func parseWorksList(raw json.RawMessage) ([]*workgraph.Work, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var list []*workgraph.Work
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("decode works list: %w", err)
		}
		return list, nil
	}
	var wrapped struct {
		Works []*workgraph.Work `json:"works"`
	}
	if err := json.Unmarshal(trimmed, &wrapped); err != nil {
		return nil, fmt.Errorf("decode works list: %w", err)
	}
	return wrapped.Works, nil
}

// filterMissionWorks keeps only mission Works — those carrying the
// ADR-0008 mission contract with a budget ceiling present on the wire
// (mirrors workgraph.Work.IsMission; CI Works have mission == nil).
func filterMissionWorks(works []*workgraph.Work) []*workgraph.Work {
	out := make([]*workgraph.Work, 0, len(works))
	for _, wk := range works {
		if wk == nil || !wk.IsMission() {
			continue
		}
		out = append(out, wk)
	}
	return out
}

// needsHumanState reports whether a state awaits a human decision
// (work.schema/1.0 mission-only pause states).
func needsHumanState(s workgraph.State) bool {
	switch s {
	case workgraph.StateWaitingHuman, workgraph.StateSuspended, workgraph.StateBudgetExhausted:
		return true
	}
	return false
}

// sortMissionsByNow mirrors the k-now-01 ordering law from
// packages/shell.NowProjection locally (no shell import from cmd/works):
// WAITING_HUMAN works first, then RUNNING, then all others, each group
// in stable work-id order.
func sortMissionsByNow(works []*workgraph.Work) {
	sort.SliceStable(works, func(i, j int) bool {
		a, b := works[i], works[j]
		aw := a.State == workgraph.StateWaitingHuman
		bw := b.State == workgraph.StateWaitingHuman
		if aw != bw {
			return aw
		}
		ar := a.State == workgraph.StateRunning
		br := b.State == workgraph.StateRunning
		if ar != br {
			return ar
		}
		return a.ID < b.ID
	})
}

// shortID truncates a work id for the table's first column.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
