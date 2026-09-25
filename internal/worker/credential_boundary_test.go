package worker

import (
	"reflect"
	"testing"
)

func TestSanitizedWorkerProcessEnvStripsControlCredentials(t *testing.T) {
	in := []string{
		"PATH=C:\\Windows",
		"WORKS_ENROLL_SECRET=enroll-secret-must-not-leak",
		"WORKS_TOKEN=oidc-minted-bearer-must-not-leak",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=github-oidc-request-token-must-not-leak",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://token.actions.invalid/?sig=must-not-leak",
		"WORKS_GITHUB_TOKEN=github-secret-must-not-leak",
		"GITHUB_TOKEN=actions-secret-must-not-leak",
		"GH_TOKEN=cli-secret-must-not-leak",
		"WORKS_API=https://works.example.invalid",
		"WORKS_WORKER_ID=wrkr_jonas_lenovo",
	}
	got := sanitizedWorkerProcessEnv(in)
	want := []string{
		"PATH=C:\\Windows",
		"WORKS_API=https://works.example.invalid",
		"WORKS_WORKER_ID=wrkr_jonas_lenovo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized env = %#v, want %#v", got, want)
	}
}

func TestSanitizedWorkerProcessEnvIsCaseInsensitive(t *testing.T) {
	got := sanitizedWorkerProcessEnv([]string{"works_enroll_secret=x", "works_token=t", "actions_id_token_request_token=o", "Gh_ToKeN=y", "SAFE=z"})
	want := []string{"SAFE=z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized env = %#v, want %#v", got, want)
	}
}
