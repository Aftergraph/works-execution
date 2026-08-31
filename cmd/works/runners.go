package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"
)

// runnersCmd handles `works runners` — lists scheduler-visible runners
// (BYOC pools, RFC-0004). Useful for operators to verify pool
// membership and heartbeat liveness before scoping work to a pool.
func runnersCmd(args []string) {
	fs := flag.NewFlagSet("runners", flag.ExitOnError)
	api := fs.String("api", envOr("WORKS_API", "http://127.0.0.1:8080"), "control plane URL")
	pool := fs.String("pool", "", "filter by pool name (label pool:<name>)")
	alive := fs.Bool("alive", false, "only runners with a fresh heartbeat")
	token := fs.String("token", "", "bearer token (or WORKS_TOKEN env)")
	enroll := fs.String("enroll-secret", "", "enrollment secret (or WORKS_ENROLL_SECRET env)")
	_ = fs.Parse(args)

	auth, err := newCLIAuth(*api, *token, *enroll)
	if err != nil {
		fail("auth: %v", err)
	}

	q := ""
	if *pool != "" {
		q += "pool=" + *pool + "&"
	}
	if *alive {
		q += "alive=true&"
	}
	var resp struct {
		Runners []struct {
			RunnerID       string  `json:"runner_id"`
			TrustClass     string  `json:"trust_class"`
			LifecycleState string  `json:"lifecycle_state"`
			EnrolledAt     string  `json:"enrolled_at"`
			LastHeartbeat  *string `json:"last_heartbeat_at,omitempty"`
			Capabilities   struct {
				OS         []string `json:"os,omitempty"`
				Arch       []string `json:"arch,omitempty"`
				Labels     []string `json:"labels,omitempty"`
				Toolchains []string `json:"toolchains,omitempty"`
			} `json:"capabilities"`
		} `json:"runners"`
		Count int `json:"count"`
	}
	if _, err := auth.getJSON("/v1/runners?"+q, &resp); err != nil {
		fail("list runners: %v", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RUNNER ID\tTRUST\tLIFECYCLE\tPOOL\tLAST HEARTBEAT\tOS/ARCH")
	for _, r := range resp.Runners {
		poolName := "-"
		for _, l := range r.Capabilities.Labels {
			if len(l) > 5 && l[:5] == "pool:" {
				poolName = l[5:]
				break
			}
		}
		hb := "never"
		if r.LastHeartbeat != nil {
			if t, err := time.Parse(time.RFC3339, *r.LastHeartbeat); err == nil {
				hb = time.Since(t).Round(time.Second).String() + " ago"
			} else {
				hb = *r.LastHeartbeat
			}
		}
		osArch := ""
		if len(r.Capabilities.OS) > 0 {
			osArch = r.Capabilities.OS[0]
		}
		if len(r.Capabilities.Arch) > 0 {
			osArch += "/" + r.Capabilities.Arch[0]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.RunnerID, r.TrustClass, r.LifecycleState, poolName, hb, osArch)
	}
	w.Flush()
	fmt.Printf("\n%d runner(s)\n", resp.Count)
}