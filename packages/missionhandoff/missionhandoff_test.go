package missionhandoff_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/missionhandoff"
)

const validYAML = `version: 1
mission_id: mission-tooltime-001
objective: finish the approved task independently of the submitting chat
purpose_bindings: [aftergraph-maintenance]
budget:
  wall_clock_h: 1
verification:
  - criterion: artifact marker exists
    kind: deterministic
stages:
  prepare:
    run: printf prepared
    permissions: [read, execute]
  apply:
    needs: [prepare]
    reconcile: test -f "$MARKER"
    run: printf x >> "$MARKER"
    permissions: [read, write, execute]
    side_effects: [filesystem_write]
`

func TestCompileBindsStableMissionIdentityToWork(t *testing.T) {
	cfg, err := missionhandoff.Parse([]byte(validYAML))
	if err != nil { t.Fatal(err) }
	w, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	if w.IdempotencyKey != "mission-tooltime-001" { t.Fatalf("idempotency=%q", w.IdempotencyKey) }
	if w.CorrelationID != "mission-tooltime-001" { t.Fatalf("correlation=%q", w.CorrelationID) }
	if w.Mission == nil || len(w.Mission.Verification) != 1 { t.Fatal("mission contract missing") }
	if got := w.Graph.Nodes["apply"].Needs; len(got) != 1 || got[0] != "prepare" { t.Fatalf("needs=%v", got) }
}

func TestCompileRejectsMissingRequiredMissionFields(t *testing.T) {
	cases := []string{
		"version: 1\nobjective: x\npurpose_bindings: [p]\nbudget: {wall_clock_h: 1}\nverification: [{criterion: ok}]\nstages: {a: {run: echo ok}}\n",
		"version: 1\nmission_id: m\nobjective: x\npurpose_bindings: []\nbudget: {wall_clock_h: 1}\nverification: [{criterion: ok}]\nstages: {a: {run: echo ok}}\n",
	}
	for _, raw := range cases {
		cfg, err := missionhandoff.Parse([]byte(raw))
		if err == nil { _, err = missionhandoff.Compile(cfg) }
		if err == nil { t.Fatalf("invalid mission accepted: %s", raw) }
	}
}

func TestReconcileMakesConsequentialStageReplaySafe(t *testing.T) {
	cfg, err := missionhandoff.Parse([]byte(validYAML))
	if err != nil { t.Fatal(err) }
	w, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	cmdText := w.Graph.Nodes["apply"].Run
	marker := filepath.Join(t.TempDir(), "marker")
	for i := 0; i < 2; i++ {
		cmd := exec.Command("sh", "-c", cmdText)
		cmd.Env = append(os.Environ(), "MARKER="+marker)
		if out, err := cmd.CombinedOutput(); err != nil { t.Fatalf("run %d: %v: %s", i, err, out) }
	}
	got, err := os.ReadFile(marker)
	if err != nil { t.Fatal(err) }
	if string(got) != "x" { t.Fatalf("effect replayed, marker=%q", got) }
}

func TestIndeterminateReconcileFailsClosedBeforeMutation(t *testing.T) {
	raw := strings.Replace(validYAML, `reconcile: test -f "$MARKER"`, `reconcile: exit 2`, 1)
	cfg, err := missionhandoff.Parse([]byte(raw))
	if err != nil { t.Fatal(err) }
	w, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	marker := filepath.Join(t.TempDir(), "marker")
	cmd := exec.Command("sh", "-c", w.Graph.Nodes["apply"].Run)
	cmd.Env = append(os.Environ(), "MARKER="+marker)
	if err := cmd.Run(); err == nil { t.Fatal("indeterminate reconcile executed mutation") }
	if _, err := os.Stat(marker); !os.IsNotExist(err) { t.Fatalf("mutation happened: %v", err) }
}

func TestPlaintextSecretLikeEnvValueRejected(t *testing.T) {
	raw := strings.Replace(validYAML, "    side_effects: [filesystem_write]\n", "    side_effects: [filesystem_write]\n    env: {GITHUB_TOKEN: not-allowed-plaintext}\n", 1)
	cfg, err := missionhandoff.Parse([]byte(raw))
	if err != nil { t.Fatal(err) }
	if _, err := missionhandoff.Compile(cfg); err == nil { t.Fatal("plaintext token-like env persisted") }
}

func TestSecretReferenceIsAllowed(t *testing.T) {
	raw := strings.Replace(validYAML, "    side_effects: [filesystem_write]\n", "    side_effects: [filesystem_write]\n    env: {GITHUB_TOKEN: secret://github/runtime}\n", 1)
	cfg, err := missionhandoff.Parse([]byte(raw))
	if err != nil { t.Fatal(err) }
	w, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	if got := w.Graph.Nodes["apply"].Env["GITHUB_TOKEN"]; got != "secret://github/runtime" { t.Fatalf("ref=%q", got) }
}

func TestReconcileWrapperPreservesSingleQuotesInCommands(t *testing.T) {
	raw := strings.Replace(validYAML, `reconcile: test -f "$MARKER"`, `reconcile: test "$(printf '%s' "it's")" = "it's" && exit 1`, 1)
	cfg, err := missionhandoff.Parse([]byte(raw))
	if err != nil { t.Fatal(err) }
	w, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	cmd := exec.Command("sh", "-c", w.Graph.Nodes["apply"].Run)
	cmd.Env = append(os.Environ(), "MARKER="+filepath.Join(t.TempDir(), "m"))
	if out, err := cmd.CombinedOutput(); err != nil { t.Fatalf("quoted wrapper failed: %v: %s", err, out) }
}

func TestCompileDerivesStableWorkIDAndSpecFingerprint(t *testing.T) {
	cfg, err := missionhandoff.Parse([]byte(validYAML))
	if err != nil { t.Fatal(err) }
	w1, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	w2, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	if w1.ID != w2.ID { t.Fatalf("same mission_id produced different work IDs: %q != %q", w1.ID, w2.ID) }
	fp1, ok1 := w1.Objective.Constraints["mission_spec_sha256"].(string)
	fp2, ok2 := w2.Objective.Constraints["mission_spec_sha256"].(string)
	if !ok1 || !ok2 || fp1 == "" || fp1 != fp2 { t.Fatalf("bad stable fingerprint: %#v %#v", w1.Objective.Constraints, w2.Objective.Constraints) }
	cfg.Objective = "changed objective"
	w3, err := missionhandoff.Compile(cfg)
	if err != nil { t.Fatal(err) }
	if w3.ID != w1.ID { t.Fatalf("mission identity changed with spec: %q != %q", w3.ID, w1.ID) }
	if w3.Objective.Constraints["mission_spec_sha256"] == fp1 { t.Fatal("changed spec kept old fingerprint") }
}
