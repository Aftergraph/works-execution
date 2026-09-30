package ops_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func worksAPIDeployScriptPath(t *testing.T) string {
	t.Helper()
	p := filepath.Clean(filepath.Join("..", "..", "scripts", "ops", "deploy-works-api-once.sh"))
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("WORKS API deployment script missing: %v", err)
	}
	return p
}

func TestWorksAPIDeployScriptSyntax(t *testing.T) {
	p := worksAPIDeployScriptPath(t)
	if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

func TestWorksAPIDeployScriptSafetyContract(t *testing.T) {
	p := worksAPIDeployScriptPath(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)

	required := []string{
		"MARKER='[deploy-works-api]'",
		"LOCK_WAIT_SECONDS=180",
		`flock -w "$LOCK_WAIT_SECONDS" "$DEPLOY_LOCK"`,
		`flock -w "$LOCK_WAIT_SECONDS" 9`,
		"candidate_revision_mismatch",
		"candidate_tree_modified",
		"integrity_fabric_live_smoke_failed",
		"idempotent_integrity_fabric_live_smoke_failed",
		`write_receipt false "$pid"`,
		`write_receipt true "$pid"`,
		`"schema": "aftergraph.works-api-deployment/2"`,
		`"verified_at": "$verified_at"`,
		`"changed": $changed`,
		`"deployment_receipt_write_failed"`,
		"rollback=FAILED",
	}
	for _, needle := range required {
		if !strings.Contains(s, needle) {
			t.Errorf("missing deployment safety contract fragment %q", needle)
		}
	}

	forbidden := []string{
		"flock -n",
		"set -x",
	}
	for _, needle := range forbidden {
		if strings.Contains(s, needle) {
			t.Errorf("forbidden deployment fragment %q", needle)
		}
	}
}
