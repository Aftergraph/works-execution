package ops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubOIDCActivationScriptIsRollbackSafeAndExact(t *testing.T) {
	p := filepath.Join("..", "..", "scripts", "ops", "works-github-oidc-enrollment.sh")
	b, err := os.ReadFile(p)
	if err != nil { t.Fatal(err) }
	s := string(b)
	required := []string{
		"ENV_FILE=/etc/works/works.env",
		"SERVICE=works-api.service",
		"BASE_URL=http://127.0.0.1:18191",
		"WORKS_GITHUB_OIDC_AUDIENCE",
		"WORKS_GITHUB_OIDC_REPOSITORY_ID",
		"WORKS_GITHUB_OIDC_WORKFLOW_REF",
		"WORKS_GITHUB_OIDC_WORKFLOW_SHA",
		"[[ \"$WORKFLOW_SHA\" =~ ^[0-9a-f]{40}$ ]]",
		"cp -a -- \"$ENV_FILE\" \"$backup\"",
		"trap rollback ERR INT TERM HUP",
		"systemctl restart \"$SERVICE\"",
		"unexpected_oidc_probe_status",
		"github_oidc_enrollment_not_enabled",
	}
	for _, want := range required {
		if !strings.Contains(s, want) { t.Fatalf("activation script missing %q", want) }
	}
	if strings.Contains(s, "WORKS_ENROLL_SECRET=") {
		t.Fatal("OIDC activation must not install a shared WORKS_ENROLL_SECRET")
	}
}
