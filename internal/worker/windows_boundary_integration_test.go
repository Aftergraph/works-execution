//go:build windows

package worker

import (
	"context"
	"testing"
	"time"
)

func TestRunCommandWindowsPropagatesNativeExitCode(t *testing.T) {
	res := runCommand(context.Background(), "cmd.exe /c exit 7", nil, 10*time.Second, nil, "", nil)
	if res.Status != "failed" || res.ExitCode != 7 {
		t.Fatalf("status=%q exit=%d, want failed/7; log=%q", res.Status, res.ExitCode, string(res.CombinedLog))
	}
}

func TestRunCommandWindowsDoesNotLeakWorkerEnrollmentSecret(t *testing.T) {
	t.Setenv("WORKS_ENROLL_SECRET", "sentinel-control-secret")
	command := "if ($null -ne $env:WORKS_ENROLL_SECRET) { exit 42 }"
	res := runCommand(context.Background(), command, nil, 10*time.Second, nil, "", nil)
	if res.Status != "succeeded" || res.ExitCode != 0 {
		t.Fatalf("worker-private env crossed execution boundary: status=%q exit=%d log=%q", res.Status, res.ExitCode, string(res.CombinedLog))
	}
}
