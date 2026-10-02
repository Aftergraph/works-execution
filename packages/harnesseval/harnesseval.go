// Package harnesseval builds deterministic WORKS graphs for shadow harness evaluation.
//
// It does not select, promote, or authorize a harness. It only materializes
// branch-vs-branch evaluation work so existing WORKS scheduling, leases,
// evidence, and verification semantics remain authoritative.
package harnesseval

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

type Branch struct {
	ID   string
	Hash string
}

type Spec struct {
	TaskProfileHash string
	TaskClass       string
	Branches        []Branch
	EvaluatorRun    string
	RuntimeImage    string
	TimeoutS        int
}

func Build(spec Spec) (*workgraph.Work, error) {
	if strings.TrimSpace(spec.TaskProfileHash) == "" || strings.TrimSpace(spec.TaskClass) == "" {
		return nil, errors.New("harnesseval: task profile hash and task class are required")
	}
	if len(spec.Branches) < 2 {
		return nil, errors.New("harnesseval: at least two branches are required")
	}
	if strings.TrimSpace(spec.EvaluatorRun) == "" {
		return nil, errors.New("harnesseval: evaluator command is required")
	}

	branches := append([]Branch(nil), spec.Branches...)
	sort.Slice(branches, func(i, j int) bool { return branches[i].ID < branches[j].ID })
	nodes := make(map[string]workgraph.Node, len(branches)+1)
	needs := make([]string, 0, len(branches))
	seen := map[string]bool{}

	for _, branch := range branches {
		if strings.TrimSpace(branch.ID) == "" || strings.TrimSpace(branch.Hash) == "" {
			return nil, errors.New("harnesseval: branch id and hash are required")
		}
		if seen[branch.ID] {
			return nil, fmt.Errorf("harnesseval: duplicate branch %s", branch.ID)
		}
		seen[branch.ID] = true
		id := "branch-" + branch.ID
		needs = append(needs, id)
		nodes[id] = workgraph.Node{
			ID: id,
			Run: "aftergraph-harness-eval run",
			Env: map[string]string{
				"AFTERGRAPH_HARNESS_BRANCH_ID": branch.ID,
				"AFTERGRAPH_HARNESS_BRANCH_HASH": branch.Hash,
				"AFTERGRAPH_TASK_PROFILE_HASH": spec.TaskProfileHash,
				"AFTERGRAPH_TASK_CLASS": spec.TaskClass,
			},
			TimeoutS: spec.TimeoutS,
			Runtime: workgraph.RuntimeSpec{Image: spec.RuntimeImage},
			Evidence: workgraph.EvidenceSpec{Required: true, Types: []string{"test", "artifact"}},
			Permissions: []string{"read", "execute"},
		}
	}

	nodes["independent-evaluator"] = workgraph.Node{
		ID: "independent-evaluator",
		Run: spec.EvaluatorRun,
		Needs: needs,
		TimeoutS: spec.TimeoutS,
		Runtime: workgraph.RuntimeSpec{Image: spec.RuntimeImage},
		Evidence: workgraph.EvidenceSpec{Required: true, Types: []string{"test", "artifact"}},
		Permissions: []string{"read", "execute"},
	}

	work := &workgraph.Work{
		ID: workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Objective: workgraph.Objective{
			Type: "custom",
			Description: "shadow harness branch evaluation",
			Constraints: map[string]any{
				"harness_mode": "shadow",
				"task_profile_hash": spec.TaskProfileHash,
				"task_class": spec.TaskClass,
				"independent_evaluator": true,
			},
		},
		Policy: workgraph.Policy{ProductionAccess: false, TrustClass: "untrusted"},
		Graph: workgraph.Graph{Nodes: nodes},
	}
	if err := work.Validate(); err != nil {
		return nil, fmt.Errorf("harnesseval: invalid work: %w", err)
	}
	return work, nil
}
