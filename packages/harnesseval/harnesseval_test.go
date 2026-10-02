package harnesseval

import "testing"

func TestBuildCreatesParallelBranchesAndIndependentEvaluator(t *testing.T) {
	work, err := Build(Spec{
		TaskProfileHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TaskClass: "software",
		Branches: []Branch{{ID:"b", Hash:"bb"}, {ID:"a", Hash:"aa"}},
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
}

func TestBuildRejectsSingleBranch(t *testing.T) {
	_, err := Build(Spec{
		TaskProfileHash:"a", TaskClass:"software",
		Branches: []Branch{{ID:"a", Hash:"aa"}},
		EvaluatorRun:"verify",
	})
	if err == nil { t.Fatal("expected error") }
}
