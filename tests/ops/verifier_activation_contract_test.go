package ops_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func activationScriptPath(t *testing.T) string {
	t.Helper()
	p := filepath.Clean(filepath.Join("..", "..", "scripts", "ops", "works-verifier-credential.sh"))
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("activation helper missing: %v", err)
	}
	return p
}

func TestSentinelVerifierActivationScriptSyntax(t *testing.T) {
	p := activationScriptPath(t)
	if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

func TestSentinelVerifierActivationSafetyContract(t *testing.T) {
	p := activationScriptPath(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)

	required := []string{
		`[[ "${EUID:-$(id -u)}" -eq 0 ]]`,
		`ENV_FILE=/etc/works/works.env`,
		`SERVICE=works-api.service`,
		`BASE_URL=http://127.0.0.1:18191`,
		`[[ -f "$ENV_FILE" && ! -L "$ENV_FILE" ]]`,
		`[[ "$(stat -c '%u:%g' "$ENV_FILE")" == "0:0" ]]`,
		`canonical_env_file_redirected`,
		`backup="$(mktemp /run/works.env.before-verifier.XXXXXX)"`,
		`trap rollback ERR INT TERM HUP`,
		`token="$(openssl rand -hex 32)"`,
		`[[ "$verify_code" == 401 ]]`,
		`[[ "$enroll_code" == 401 ]]`,
		`"credential_value_exposed":false`,
	}
	for _, needle := range required {
		if !strings.Contains(s, needle) {
			t.Errorf("missing safety contract fragment %q", needle)
		}
	}

	forbidden := []string{
		"set -x",
		`echo "$TOKEN"`,
		`printf '%s\\n' "$TOKEN"`,
		"GITHUB_TOKEN",
		"GH_TOKEN",
	}
	for _, needle := range forbidden {
		if strings.Contains(s, needle) {
			t.Errorf("forbidden credential-leak or unrelated-token fragment %q", needle)
		}
	}
}

func TestSentinelVerifierActivationOnlyMutatesCanonicalCredentialKey(t *testing.T) {
	p := activationScriptPath(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)

	if got := strings.Count(s, "WORKS_VERIFIER_TOKEN"); got < 2 {
		t.Fatalf("expected verifier token contract to be explicit, got %d occurrences", got)
	}
	for _, key := range []string{
		"WORKS_ENROLL_SECRET=",
		"WORKS_RAB_CONTROL_TOKEN=",
		"WORKS_PLATFORM_BRIDGE_SECRET=",
		"WORKS_GITHUB_TOKEN=",
	} {
		if strings.Contains(s, key) {
			t.Fatalf("activation helper must not assign unrelated credential %s", key)
		}
	}
}
