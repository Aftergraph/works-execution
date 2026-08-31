package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func skipIfNoGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go not on PATH: %v", err)
	}
}

// TestDetect_GoRepo: a directory with go.mod is detected as Go.
func TestDetect_GoRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stack, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if stack != StackGo {
		t.Errorf("stack: want %q, got %q", StackGo, stack)
	}
}

// TestDetect_NonGo: a directory without go.mod is rejected.
func TestDetect_NonGo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Detect(dir)
	if err == nil {
		t.Fatal("expected ErrUnsupportedStack")
	}
	if !strings.Contains(err.Error(), "unsupported stack") {
		t.Errorf("expected unsupported stack error, got %v", err)
	}
}

// TestPlan_Go: returns the standard two-step plan.
func TestPlan_Go(t *testing.T) {
	steps, err := Plan(StackGo, "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
	if steps[0].Name != "go vet" {
		t.Errorf("step 0 name: %q", steps[0].Name)
	}
	if steps[1].Name != "go test" {
		t.Errorf("step 1 name: %q", steps[1].Name)
	}
}

// TestRun_Go_KnownGood: runs the real runner against a tiny
// always-passes Go module. The result must be Failed=false.
func TestRun_Go_KnownGood(t *testing.T) {
	skipIfNoGo(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module works-execution/runner-test\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(`package x
import "testing"
func TestPass(t *testing.T) {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{Workdir: dir, Stack: StackGo})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed {
		t.Errorf("expected Passed, got Failed. steps=%+v", res.Steps)
	}
	if res.GoVersion == "" {
		t.Error("GoVersion empty")
	}
	if len(res.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(res.Steps))
	}
}

// TestRun_Go_KnownBad: a module with a failing test must produce
// Failed=true with FailedStep="go test".
func TestRun_Go_KnownBad(t *testing.T) {
	skipIfNoGo(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module works-execution/runner-bad\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(`package x
import "testing"
func TestFail(t *testing.T) { t.Fatal("intentional") }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{Workdir: dir, Stack: StackGo})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Failed {
		t.Error("expected Failed")
	}
	if res.FailedStep != "go test" {
		t.Errorf("FailedStep: want %q, got %q", "go test", res.FailedStep)
	}
	// vet should have passed (it doesn't know about tests)
	if res.Steps[0].ExitCode != 0 {
		t.Errorf("vet should pass, got exit=%d", res.Steps[0].ExitCode)
	}
	if res.Steps[1].ExitCode == 0 {
		t.Errorf("test should fail, got exit=0")
	}
}

// TestRun_Go_VetFails: a module with a syntax error fails vet.
func TestRun_Go_VetFails(t *testing.T) {
	skipIfNoGo(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module works-execution/runner-vet-bad\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(`package x
func bad( {
`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{Workdir: dir, Stack: StackGo})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Failed {
		t.Error("expected Failed (vet should fail)")
	}
	if res.FailedStep != "go vet" {
		t.Errorf("FailedStep: want %q, got %q", "go vet", res.FailedStep)
	}
}

// TestRun_PlanOverride: a custom plan is honored.
func TestRun_PlanOverride(t *testing.T) {
	skipIfNoGo(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module works-execution/runner-override\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(`package x
var V = 1
`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, Options{
		Workdir: dir,
		Stack:   StackGo,
		PlanOverride: []Step{
			{Name: "echo", Cmd: "echo", Args: []string{"hello"}, Timeout: 5 * time.Second},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed {
		t.Errorf("expected Passed, got Failed")
	}
	if len(res.Steps) != 1 || res.Steps[0].Name != "echo" {
		t.Errorf("expected single echo step, got %+v", res.Steps)
	}
	if !strings.Contains(res.Steps[0].Stdout, "hello") {
		t.Errorf("stdout missing 'hello': %q", res.Steps[0].Stdout)
	}
}

// TestFormatResult: smoke test on the human-readable formatter.
func TestFormatResult(t *testing.T) {
	res := &Result{
		Stack:      StackGo,
		DurationMs: 1234,
		Failed:     true,
		FailedStep: "go test",
		Steps: []StepResult{
			{Name: "go vet", ExitCode: 0, DurationMs: 100},
			{Name: "go test", ExitCode: 1, DurationMs: 1134},
		},
	}
	out := FormatResult(res)
	if !strings.Contains(out, "Stack:      go") {
		t.Errorf("missing Stack line: %s", out)
	}
	if !strings.Contains(out, "Failed:     true (step=\"go test\")") {
		t.Errorf("missing Failed line: %s", out)
	}
	if !strings.Contains(out, "go vet") || !strings.Contains(out, "go test") {
		t.Errorf("missing step names: %s", out)
	}
}

// TestMergeEnv: overrides win, others preserved.
func TestMergeEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin", "FOO=1", "BAR=2"}
	out := mergeEnv(base, map[string]string{"FOO": "x", "BAZ": "y"})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "FOO=x") {
		t.Errorf("FOO not overridden: %s", joined)
	}
	if !strings.Contains(joined, "BAR=2") {
		t.Errorf("BAR not preserved: %s", joined)
	}
	if !strings.Contains(joined, "BAZ=y") {
		t.Errorf("BAZ not added: %s", joined)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Errorf("PATH not preserved: %s", joined)
	}
}

// TestTruncate: short strings pass through, long strings get the
// truncation marker.
func TestTruncate(t *testing.T) {
	short := "hello"
	if truncate(short, 10) != short {
		t.Errorf("short string changed: %q", truncate(short, 10))
	}
	long := strings.Repeat("x", 100)
	out := truncate(long, 10)
	if !strings.Contains(out, "truncated") {
		t.Errorf("long string not truncated: %q", out)
	}
}
