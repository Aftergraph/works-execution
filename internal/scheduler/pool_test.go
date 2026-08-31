package scheduler_test

import (
	"testing"

	"github.com/JonasAbde/works-execution/internal/scheduler"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// poolWork returns a work with Requirements.Pool set.
func poolWork(pool string) *workgraph.Work {
	return &workgraph.Work{
		ID:    "wrk_pooltest",
		State: workgraph.StateQueued,
		Source: workgraph.Source{
			Type:       "github_push",
			Repository: "acme/widgets",
		},
		Objective:    workgraph.Objective{Type: "verify_change"},
		Requirements: workgraph.Requirements{Pool: pool, OS: "linux", Arch: "amd64"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"build": {ID: "build", Run: "echo hi"},
		}},
		Policy: workgraph.Policy{TrustClass: "standard", ProductionAccess: false},
	}
}

func poolRunner(id string, labels []string) *scheduler.Runner {
	return &scheduler.Runner{
		RunnerID:   id,
		TrustClass: "standard",
		Lifecycle:  "active",
		OS:         []string{"linux"},
		Arch:       []string{"amd64"},
		Labels:     labels,
	}
}

// TestSelect_PoolIsolation: a pool-scoped work only lands on runners
// carrying the matching pool:<name> label.
func TestSelect_PoolIsolation(t *testing.T) {
	work := poolWork("acme")
	node := work.Graph.Nodes["build"]

	// Shared runner (no pool label) must be rejected.
	pool := []*scheduler.Runner{poolRunner("wrkr_shared", nil)}
	_, err := scheduler.Select(nil, work, &node, pool)
	if err == nil {
		t.Fatal("expected rejection: shared runner must not serve pool-scoped work")
	}

	// Correct-pool runner must be selected.
	pool = append(pool, poolRunner("wrkr_acme", []string{"pool:acme"}))
	assign, err := scheduler.Select(nil, work, &node, pool)
	if err != nil {
		t.Fatalf("expected assignment to wrkr_acme, got error: %v", err)
	}
	if assign.SelectedRunner.RunnerID != "wrkr_acme" {
		t.Fatalf("selected %q, want wrkr_acme", assign.SelectedRunner.RunnerID)
	}

	// Wrong-pool runner must be rejected too.
	wrongPool := []*scheduler.Runner{poolRunner("wrkr_other", []string{"pool:other"})}
	if _, err := scheduler.Select(nil, work, &node, wrongPool); err == nil {
		t.Fatal("expected rejection: wrong-pool runner must not serve acme work")
	}
}

// TestSelect_NoPoolConstraint: works without Requirements.Pool run on
// any eligible runner (shared or pooled) — back-compat with pre-BYOC.
func TestSelect_NoPoolConstraint(t *testing.T) {
	work := poolWork("") // no pool
	work.ID = "wrk_nopool"
	node := work.Graph.Nodes["build"]

	shared := poolRunner("wrkr_shared", nil)
	pool := []*scheduler.Runner{shared}
	assign, err := scheduler.Select(nil, work, &node, pool)
	if err != nil {
		t.Fatalf("expected assignment on shared runner, got: %v", err)
	}
	if assign.SelectedRunner.RunnerID != "wrkr_shared" {
		t.Fatalf("selected %q, want wrkr_shared", assign.SelectedRunner.RunnerID)
	}
}