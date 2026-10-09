package missionhandoff_test

import (
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/missionhandoff"
)

func TestCompileRejectsDependencyCycle(t *testing.T) {
	raw := `version: 1
mission_id: cycle-test
objective: reject cyclic work graph
purpose_bindings: [test]
budget: {wall_clock_h: 1}
verification: [{criterion: never deadlocks, kind: deterministic}]
stages:
  a:
    needs: [b]
    run: echo a
  b:
    needs: [a]
    run: echo b
`
	cfg, err := missionhandoff.Parse([]byte(raw))
	if err != nil { t.Fatal(err) }
	if _, err := missionhandoff.Compile(cfg); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cyclic mission accepted: %v", err)
	}
}

func TestConsequentialStageRequiresReconcile(t *testing.T) {
	raw := strings.Replace(validYAML, `    reconcile: test -f "$MARKER"
`, "", 1)
	cfg, err := missionhandoff.Parse([]byte(raw))
	if err != nil { t.Fatal(err) }
	if _, err := missionhandoff.Compile(cfg); err == nil || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("consequential stage without reconcile accepted: %v", err)
	}
}

func TestParseRejectsTrailingYAMLDocument(t *testing.T) {
	raw := validYAML + "---\nversion: 1\nmission_id: shadow\n"
	if _, err := missionhandoff.Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "single YAML document") {
		t.Fatalf("trailing YAML document accepted: %v", err)
	}
}

func TestCompileRejectsBlankPurposeBinding(t *testing.T) {
	raw := strings.Replace(validYAML, "purpose_bindings: [aftergraph-maintenance]", `purpose_bindings: [""]`, 1)
	cfg, err := missionhandoff.Parse([]byte(raw))
	if err != nil { t.Fatal(err) }
	if _, err := missionhandoff.Compile(cfg); err == nil || !strings.Contains(err.Error(), "purpose_bindings") {
		t.Fatalf("blank purpose binding accepted: %v", err)
	}
}
