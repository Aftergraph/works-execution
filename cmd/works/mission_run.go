package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/missionhandoff"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func missionCmd(args []string) {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "works: usage: works mission run --config mission.yaml [--api URL] [--follow]")
		os.Exit(2)
	}
	if err := runMission(args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "works: %v\n", err)
		os.Exit(1)
	}
}

// runMission is the testable body of `works mission run`. Creation is
// detached by default: once the control plane durably owns the Work, this
// function returns and the submitting coordinator may disappear.
func runMission(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("mission run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "mission.yaml", "durable mission YAML")
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
	follow := fs.Bool("follow", false, "observe until terminal; does not own execution lifetime")
	token := fs.String("token", "", "bearer token (or WORKS_TOKEN env)")
	enroll := fs.String("enroll-secret", "", "enrollment secret (or WORKS_ENROLL_SECRET env)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg, err := missionhandoff.Parse(raw)
	if err != nil {
		return err
	}
	want, err := missionhandoff.Compile(cfg)
	if err != nil {
		return err
	}
	auth, err := newCLIAuth(*api, *token, *enroll)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	existing, err := getMissionIfExists(auth, want.ID)
	if err != nil {
		return fmt.Errorf("reconcile existing mission: %w", err)
	}
	created := false
	var owned *workgraph.Work
	if existing != nil {
		if err := sameMissionSpec(existing, want); err != nil {
			return err
		}
		owned = existing
	} else {
		var accepted workgraph.Work
		body := struct {
			*workgraph.Work
			Queue bool `json:"queue"`
		}{Work: want, Queue: true}
		if _, err := auth.postJSON("/v1/works", body, &accepted); err != nil {
			return fmt.Errorf("submit mission: %w", err)
		}

		// A concurrent submitter may have raced between our preflight GET and
		// POST. Re-read canonical control-plane state before claiming success.
		// This also defends against the store's same-ID idempotent path, which
		// deliberately does not compare full payload bytes.
		confirmed, err := getMissionIfExists(auth, want.ID)
		if err != nil {
			return fmt.Errorf("post-submit reconcile: %w", err)
		}
		if confirmed == nil {
			return fmt.Errorf("post-submit reconcile: work %s not found after create", want.ID)
		}
		if err := sameMissionSpec(confirmed, want); err != nil {
			return fmt.Errorf("post-submit reconcile: %w", err)
		}
		owned = confirmed
		created = true
	}

	verb := "reconciled existing"
	if created {
		verb = "submitted"
	}
	fmt.Fprintf(stdout, "%s durable mission %s work=%s state=%s\n", verb, cfg.MissionID, owned.ID, owned.State)
	fmt.Fprintf(stdout, "track with: works status %s --follow\n", owned.ID)
	if !*follow {
		return nil
	}
	return followMission(auth, owned.ID, stdout)
}

func missionFingerprint(w *workgraph.Work) string {
	if w == nil || w.Objective.Constraints == nil {
		return ""
	}
	v, _ := w.Objective.Constraints["mission_spec_sha256"].(string)
	return v
}

func sameMissionSpec(got, want *workgraph.Work) error {
	if got.IdempotencyKey != want.IdempotencyKey || got.ID != want.ID {
		return fmt.Errorf("mission identity collision: existing work %s does not match requested identity", got.ID)
	}
	gf, wf := missionFingerprint(got), missionFingerprint(want)
	if gf == "" || wf == "" || gf != wf {
		return fmt.Errorf("mission_id %q already exists with a different spec; use a new mission_id", want.IdempotencyKey)
	}
	return nil
}

func getMissionIfExists(auth *cliAuth, workID string) (*workgraph.Work, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(auth.api, "/")+"/v1/works/"+workID, nil)
	if err != nil {
		return nil, err
	}
	if h := auth.authHeader(); h != "" {
		req.Header.Set("Authorization", h)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("GET /v1/works/%s: status=401\n  %s", workID, hint401)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET /v1/works/%s: status=%d", workID, resp.StatusCode)
	}
	var w workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return nil, fmt.Errorf("decode existing work: %w", err)
	}
	return &w, nil
}

func followMission(auth *cliAuth, workID string, out io.Writer) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var w workgraph.Work
		if _, err := auth.getJSON("/v1/works/"+workID, &w); err != nil {
			return fmt.Errorf("follow mission: %w", err)
		}
		fmt.Fprintf(out, "mission work=%s state=%s attempts=%d evidence=%d artifacts=%d\n", w.ID, w.State, len(w.Attempts), len(w.Evidence), len(w.Artifacts))
		if w.State.IsTerminal() {
			if w.State != workgraph.StateSucceeded {
				return fmt.Errorf("mission reached terminal state %s", w.State)
			}
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("mission %s did not reach terminal state within 2 minutes", workID)
}
