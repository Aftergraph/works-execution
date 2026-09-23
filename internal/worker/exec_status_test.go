package worker

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// TestRunCommand_TimedOutStatusOnDeadline pins that a command killed by the
// per-node timeout is classified as timed_out, not failed. exec.CommandContext
// kills the process, so cmd.Run() returns an *exec.ExitError — the classifier
// must check the context deadline BEFORE the ExitError or every timeout is
// silently misrecorded as a plain failure downstream.
func TestRunCommand_TimedOutStatusOnDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell command; windows shell path covered separately")
	}
	res := runCommand(context.Background(), "sleep 5", nil, 500*time.Millisecond, nil, "", nil)
	if res.Status != "timed_out" {
		t.Fatalf("status=%q exit=%d, want timed_out (log=%q)", res.Status, res.ExitCode, string(res.CombinedLog))
	}
	if res.ExitCode != -1 {
		t.Fatalf("exit=%d, want -1 on timeout", res.ExitCode)
	}
}

// TestRunCommand_TimedOutEvidenceIsFail pins the evidence mapping: a timeout
// must be recorded in the evidence chain as a failure, never as a skip.
func TestRunCommand_TimedOutEvidenceIsFail(t *testing.T) {
	if got := evidenceResult("timed_out"); got != "fail" {
		t.Fatalf("evidenceResult(timed_out)=%q, want fail", got)
	}
}

// TestRunCommand_OOMKilledEvidenceIsFail pins that a docker OOM kill is a
// terminal failure in the evidence chain. Previously oom_killed fell through
// evidenceResult's switch to "skip", corrupting outcome classification.
func TestRunCommand_OOMKilledEvidenceIsFail(t *testing.T) {
	if got := evidenceResult("oom_killed"); got != "fail" {
		t.Fatalf("evidenceResult(oom_killed)=%q, want fail", got)
	}
}
