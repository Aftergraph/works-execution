package ops_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sourceRootActivationScriptPath(t *testing.T) string {
	t.Helper()
	p := filepath.Clean(filepath.Join("..", "..", "scripts", "ops", "works-source-root.sh"))
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("source-root activation helper missing: %v", err)
	}
	return p
}

func TestWorksSourceRootActivationScriptSyntax(t *testing.T) {
	p := sourceRootActivationScriptPath(t)
	if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

func TestWorksSourceRootActivationSafetyContract(t *testing.T) {
	p := sourceRootActivationScriptPath(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)

	required := []string{
		"ENV_FILE=/etc/works/works.env",
		"SERVICE=works-worker.service",
		"WORKER_UNIT_GLOB='works-worker*.service'",
		"SOURCE_ROOT=/var/lib/works",
		"SOURCE_PARENT=/var/lib/works/works-sources",
		"MIN_FREE_KB=1048576",
		"MIN_FREE_INODES=10000",
		"worker_binary_missing_source_root_contract:$unit",
		"worker_env_file_not_canonical:$unit",
		"source_root_mount_noexec",
		"works.env.before-source-root",
		"trap rollback ERR INT TERM HUP",
		"WORKS_SOURCE_ROOT=%s",
		"systemctl restart \"$unit\"",
		"\"$new_pid\" != \"$old_pid\"",
		"runtime_source_root \"$unit\"",
		"fleet_units",
		"fleet_verified",
		"fleet_source_root_readback_failed",
		"no_active_worker_units",
		"\"credential_value_exposed\":false",
		"--state=active",
	}
	for _, needle := range required {
		if !strings.Contains(s, needle) {
			t.Errorf("missing safety contract fragment %q", needle)
		}
	}

	forbidden := []string{
		"set -x",
		"/tmp/works-sources",
		"source \"$ENV_FILE\"",
		"echo \"$WORKS_ENROLL_SECRET\"",
		"echo \"$WORKS_GITHUB_TOKEN\"",
	}
	for _, needle := range forbidden {
		if strings.Contains(s, needle) {
			t.Errorf("forbidden fragment %q", needle)
		}
	}
}

func TestWorksSourceRootActivationOnlyMutatesSourceRootKey(t *testing.T) {
	p := sourceRootActivationScriptPath(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)

	if got := strings.Count(s, "WORKS_SOURCE_ROOT"); got < 4 {
		t.Fatalf("expected explicit WORKS_SOURCE_ROOT contract, got %d occurrences", got)
	}
	for _, key := range []string{
		"WORKS_ENROLL_SECRET=",
		"WORKS_RAB_CONTROL_TOKEN=",
		"WORKS_PLATFORM_BRIDGE_SECRET=",
		"WORKS_GITHUB_TOKEN=",
		"WORKS_VERIFIER_TOKEN=",
	} {
		if strings.Contains(s, key) {
			t.Fatalf("source-root helper must not assign unrelated key %s", key)
		}
	}
}

// TestWorksSourceRootActivationCoversFleet pins the production incident from
// 2026-09-23: three workers (works-worker.service, works-worker-2.service,
// works-worker-3.service) shared one binary and one env file, but the
// activator only restarted $SERVICE. The other two kept checking out to host
// tmpfs after a "successful" activation. The helper must discover, contract
// check, restart, and runtime-verify every active works-worker unit.
func TestWorksSourceRootActivationCoversFleet(t *testing.T) {
	p := sourceRootActivationScriptPath(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, needle := range []string{
		"WORKER_UNIT_GLOB='works-worker*.service'",
		"systemctl list-units --type=service --state=active",
		"for unit in $FLEET; do",
		"service_contract \"$unit\"",
		"restart_fleet",
		"fleet_verified || fail \"fleet_source_root_readback_failed\"",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("missing fleet coverage fragment %q", needle)
		}
	}
	// The fleet loops must iterate over discovered units, not the single
	// canonical $SERVICE. A glob that matches only works-worker.service
	// would reproduce the incident.
	if strings.Contains(s, "systemctl restart \"$SERVICE\"") {
		t.Errorf("activator must not restart only $SERVICE; fleet units share the env file")
	}
}
