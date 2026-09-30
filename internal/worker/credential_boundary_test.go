package worker

import (
	"os"
	"reflect"
	"testing"
)

func TestSanitizedWorkerProcessEnvStripsControlCredentialsAndResolverValues(t *testing.T) {
	t.Setenv("WORKS_SCRATCH_ROOT", t.TempDir())
	in := []string{
		"PATH=C:\\Windows",
		"WORKS_ENROLL_SECRET=enroll-secret-must-not-leak",
		"WORKS_GITHUB_TOKEN=github-secret-must-not-leak",
		"GITHUB_TOKEN=actions-secret-must-not-leak",
		"GH_TOKEN=cli-secret-must-not-leak",
		"SECRET_ENV_UNDECLARED=undeclared-secret-must-not-leak",
		"WORKS_API=https://works.example.invalid",
		"WORKS_WORKER_ID=wrkr_jonas_lenovo",
	}
	got := sanitizedWorkerProcessEnv(in)
	want := []string{
		"PATH=C:\\Windows",
		"WORKS_API=https://works.example.invalid",
		"WORKS_WORKER_ID=wrkr_jonas_lenovo",
		"TMPDIR=" + os.Getenv("WORKS_SCRATCH_ROOT"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized env = %#v, want %#v", got, want)
	}
}

func TestSanitizedWorkerProcessEnvIsCaseInsensitive(t *testing.T) {
	t.Setenv("WORKS_SCRATCH_ROOT", t.TempDir())
	got := sanitizedWorkerProcessEnv([]string{"works_enroll_secret=x", "Gh_ToKeN=y", "sEcReT_ENV_TOKEN=z", "SAFE=z"})
	want := []string{"SAFE=z", "TMPDIR=" + os.Getenv("WORKS_SCRATCH_ROOT")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized env = %#v, want %#v", got, want)
	}
}

func TestSanitizedWorkerProcessEnvOverridesSharedTmpdir(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("WORKS_SCRATCH_ROOT", scratch)
	got := sanitizedWorkerProcessEnv([]string{"TMPDIR=/tmp", "SAFE=z"})
	want := []string{"TMPDIR=" + scratch, "SAFE=z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized env = %#v, want %#v", got, want)
	}
}
