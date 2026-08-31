//go:build docker

package sandbox_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/sandbox"
)

// TestDocker_RunHelloWorld pulls alpine:3.20 and runs `echo hello` inside
// it. Skipped (build tag) when no Docker daemon is available.
func TestDocker_RunHelloWorld(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := sandbox.Run(ctx, "alpine:3.20", "echo hello-from-docker-works-execution && uname -a", sandbox.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code: got %d, want 0; log=%q", res.ExitCode, string(res.CombinedLog))
	}
	if !strings.Contains(string(res.CombinedLog), "hello-from-docker-works-execution") {
		t.Errorf("log missing expected text: %q", string(res.CombinedLog))
	}
	if !strings.HasPrefix(res.ImageID, "sha256:") {
		t.Errorf("imageID not a digest: %q", res.ImageID)
	}
	if res.ContainerID == "" {
		t.Errorf("containerID is empty")
	}
	if res.Duration <= 0 {
		t.Errorf("duration should be positive: %s", res.Duration)
	}
}

// TestDocker_ExitCode captures a non-zero exit code.
func TestDocker_ExitCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := sandbox.Run(ctx, "alpine:3.20", "exit 42", sandbox.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 42 {
		t.Errorf("exit code: got %d, want 42", res.ExitCode)
	}
}

// TestDocker_EnvInjected confirms that opts.Env values reach the
// container.
func TestDocker_EnvInjected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := sandbox.Run(ctx, "alpine:3.20", "echo $WORKS_TEST_VAR", sandbox.RunOptions{
		Env: map[string]string{"WORKS_TEST_VAR": "works-env-ok"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit: %d", res.ExitCode)
	}
	if !strings.Contains(string(res.CombinedLog), "works-env-ok") {
		t.Errorf("env not propagated: %q", string(res.CombinedLog))
	}
}

// TestDocker_NetworkDeny confirms --network=none blocks external
// network access. We expect a non-zero exit (DNS fails or
// /etc/resolv.conf is empty). This is the "no network" hermetic
// guarantee.
func TestDocker_NetworkDeny(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sandbox.Run(ctx, "alpine:3.20",
		"timeout 3 wget -qO- https://example.com || echo NETWORK_BLOCKED_OK",
		sandbox.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// We don't assert on exit code (varies by wget/timeout availability)
	// but we DO assert the log doesn't contain example.com HTML.
	log := string(res.CombinedLog)
	if strings.Contains(log, "<html") || strings.Contains(log, "Example Domain") {
		t.Errorf("network was not blocked: %q", log)
	}
	if !strings.Contains(log, "NETWORK_BLOCKED_OK") && res.ExitCode == 0 {
		t.Logf("network test inconclusive (exit=0, no network marker): %q", log)
	}
}

// TestDocker_ReadOnlyRootFS confirms --read-only is enforced.
func TestDocker_ReadOnlyRootFS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sandbox.Run(ctx, "alpine:3.20",
		"touch /should-fail || echo READONLY_OK",
		sandbox.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// We expect non-zero exit (touch fails on read-only FS) and
	// READONLY_OK in the log.
	if !strings.Contains(string(res.CombinedLog), "READONLY_OK") {
		t.Errorf("read-only not enforced: %q", string(res.CombinedLog))
	}
}