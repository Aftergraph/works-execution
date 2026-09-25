package main

import "testing"

func TestStudy015ProbeExecutesDispatchAcceptanceLaws(t *testing.T) {
	out, err := runProbe()
	if err != nil {
		t.Fatal(err)
	}
	if out.Schema != "study015.probe/1.0" {
		t.Fatalf("schema=%q", out.Schema)
	}
	if out.Component != "works-execution" {
		t.Fatalf("component=%q", out.Component)
	}
	if len(out.SourceHead) != 40 {
		t.Fatalf("source_head=%q", out.SourceHead)
	}
	if out.NetworkUsed {
		t.Fatal("probe must be local-only")
	}
	for name, ok := range out.Mechanisms {
		if !ok {
			t.Fatalf("mechanism %s not demonstrated", name)
		}
	}
	if got := out.Observations["unknown_effect_state"]; got != "INDETERMINATE" {
		t.Fatalf("unknown_effect_state=%v", got)
	}
}
