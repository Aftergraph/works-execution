package harnesseval

import "testing"

func hash(c string) string {
	out := ""
	for len(out) < 64 { out += c }
	return out[:64]
}

func TestBuildCreatesParallelBranchesAndIndependentEvaluator(t *testing.T) {
	decisionHash := hash("d")
	profileHash := hash("c")
	work, err := Build(Spec{
		RoutingDecisionHash: decisionHash,
		TaskProfileHash: profileHash,
		TaskClass: "software",
		Branches: []Branch{
			{ID:"b", Hash:hash("b"), LineageHash:hash("2")},
			{ID:"a", Hash:hash("a"), LineageHash:hash("1")},
		},
		EvaluatorRun: "sentinel verify --input artifacts",
		TimeoutS: 300,
	})
	if err != nil { t.Fatal(err) }
	if work.Policy.ProductionAccess { t.Fatal("evaluation work must never have production access") }
	eval := work.Graph.Nodes["independent-evaluator"]
	if len(eval.Needs) != 2 || eval.Needs[0] != "branch-a" || eval.Needs[1] != "branch-b" {
		t.Fatalf("unexpected deterministic evaluator needs: %#v", eval.Needs)
	}
	if work.Objective.Constraints["harness_mode"] != "shadow" {
		t.Fatal("expected shadow-only evaluation")
	}
	if work.Objective.Constraints["routing_decision_hash"] != decisionHash {
		t.Fatal("routing decision hash must be bound to work objective")
	}
	branch := work.Graph.Nodes["branch-a"]
	if branch.Env["AFTERGRAPH_ROUTING_DECISION_HASH"] != decisionHash || branch.Env["AFTERGRAPH_HARNESS_LINEAGE_HASH"] != hash("1") {
		t.Fatal("branch execution must carry routing and lineage provenance")
	}
	if eval.Env["AFTERGRAPH_ROUTING_DECISION_HASH"] != decisionHash || eval.Env["AFTERGRAPH_TASK_PROFILE_HASH"] != profileHash {
		t.Fatal("independent evaluator must carry routing provenance")
	}
}

func TestBuildRejectsInvalidProvenance(t *testing.T) {
	_, err := Build(Spec{
		RoutingDecisionHash: hash("d"),
		TaskProfileHash: hash("p"),
		TaskClass:"software",
		Branches: []Branch{
			{ID:"a", Hash:"aa", LineageHash:hash("1")},
			{ID:"b", Hash:hash("b"), LineageHash:hash("2")},
		},
		EvaluatorRun:"verify",
	})
	if err == nil { t.Fatal("expected invalid branch hash error") }
}

func TestBuildRejectsSingleBranch(t *testing.T) {
	_, err := Build(Spec{
		RoutingDecisionHash: hash("d"), TaskProfileHash:hash("p"), TaskClass:"software",
		Branches: []Branch{{ID:"a", Hash:hash("a"), LineageHash:hash("1")}},
		EvaluatorRun:"verify",
	})
	if err == nil { t.Fatal("expected error") }
}
