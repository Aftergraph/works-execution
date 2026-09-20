package ops_test

import (
	"os"
	"strings"
	"testing"
)

func helperSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../scripts/ops/works-verifier-credential.sh")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWorksVerifierCredentialHelperPinsCanonicalBoundary(t *testing.T) {
	s := helperSource(t)
	for _, want := range []string{
		"ENV_FILE=/etc/works/works.env",
		"SERVICE=works-api.service",
		"BASE_URL=http://127.0.0.1:18191",
		"root_required",
		"canonical_env_file_missing_or_symlinked",
		"env_file_not_root_owned",
		"env_file_permissions_too_open",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("helper missing %q", want)
		}
	}
}

func TestWorksVerifierCredentialHelperNeverPrintsCredential(t *testing.T) {
	s := helperSource(t)
	for _, forbidden := range []string{
		"echo \"$WORKS_VERIFIER_TOKEN\"",
		"printf '%s\\n' \"$WORKS_VERIFIER_TOKEN\"",
		"set -x",
	} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("helper contains credential-leak pattern %q", forbidden)
		}
	}
	if !strings.Contains(s, "credential_value_exposed") {
		t.Fatal("helper lacks explicit non-disclosure result")
	}
}

func TestWorksVerifierCredentialHelperRollsBackAndProvesFailClosedBoundary(t *testing.T) {
	s := helperSource(t)
	for _, want := range []string{
		"cp -a -- \"$backup\" \"$ENV_FILE\"",
		"trap rollback ERR INT TERM",
		"verification_ingest_not_enabled",
		"worker_enrollment_regressed",
		"[[ \"$verify_code\" == 401 ]]",
		"[[ \"$enroll_code\" == 401 ]]",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("helper missing rollback/canary invariant %q", want)
		}
	}
}
