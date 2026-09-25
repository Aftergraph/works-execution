package worker

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/sandbox"
)

// TestExecutePath_SandboxedWorkerEnvIsolation pins the Hermetic Execution
// Standard (#111) on the PRODUCTION worker path: workerSandboxManifest (what
// Worker.execute derives for every leased node) must produce a subprocess
// environment containing ONLY the node's declared Env keys plus the sandbox
// base (PATH/HOME/LANG). Worker-process credentials — enrollment secret,
// GitHub token, arbitrary inherited env — must never cross the execution
// boundary. Before the sandbox wiring, the production path passed a nil
// manifest and the child inherited the full worker os.Environ().
func TestExecutePath_SandboxedWorkerEnvIsolation(t *testing.T) {
	t.Setenv("WORKS_ENROLL_SECRET", "worker-secret-must-not-leak")
	t.Setenv("WORKS_GITHUB_TOKEN", "github-secret-must-not-leak")
	t.Setenv("WORKS_DB", "/tmp/leaky-control-plane.db")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret-must-not-leak")

	item := ReadyItem{
		WorkID: "wrk_sb", NodeID: "n1",
		Run: "printenv",
		Env: map[string]string{"NODE_DECLARED": "visible"},
	}
	m := workerSandboxManifest(item, "")
	res := runCommand(context.Background(), item.Run, item.Env, 10*time.Second, nil, "", &m)
	if res.Status != "succeeded" {
		t.Fatalf("status=%q exit=%d log=%q", res.Status, res.ExitCode, string(res.CombinedLog))
	}
	out := string(res.CombinedLog)
	if !strings.Contains(out, "NODE_DECLARED=visible") {
		t.Fatalf("node-declared env missing in child: %q", out)
	}
	for _, leak := range []string{"worker-secret-must-not-leak", "github-secret-must-not-leak", "leaky-control-plane.db", "cloud-secret-must-not-leak"} {
		if strings.Contains(out, leak) {
			t.Fatalf("worker-process env leaked into sandboxed child: %q", out)
		}
	}
}

// TestWorkerSandboxManifest_NetworkPolicyFromSideEffects pins the network
// policy derivation: undeclared egress → deny; declared network_egress or
// external_api_call → allow-list policy.
func TestWorkerSandboxManifest_NetworkPolicyFromSideEffects(t *testing.T) {
	deny := workerSandboxManifest(ReadyItem{WorkID: "w", NodeID: "n"}, "")
	if deny.Network != sandbox.NetworkDeny {
		t.Fatalf("no side effects → want deny, got %q", deny.Network)
	}
	for _, se := range [][]string{{"network_egress"}, {"external_api_call"}} {
		got := workerSandboxManifest(ReadyItem{WorkID: "w", NodeID: "n", SideEffects: se}, "")
		if got.Network != sandbox.NetworkAllow {
			t.Fatalf("side effects %v → want allow-list policy, got %q", se, got.Network)
		}
		if len(got.AllowList) == 0 {
			t.Fatalf("allow-list policy requires entries (sandbox validation law)")
		}
	}
}

// TestWorkerSandboxManifest_SourceCheckoutPinsWorkdir pins that a checked-out
// source tree becomes the sandbox workdir (the node's Run executes in the
// repo it was dispatched for), and that without source the node gets an
// isolated workspace rather than the worker's cwd.
func TestWorkerSandboxManifest_SourceCheckoutPinsWorkdir(t *testing.T) {
	m := workerSandboxManifest(ReadyItem{WorkID: "w", NodeID: "n"}, "/src/checkout")
	if m.WorkingDir != "/src/checkout" || m.Filesystem != sandbox.FSShared {
		t.Fatalf("source checkout must pin workdir: got dir=%q fs=%q", m.WorkingDir, m.Filesystem)
	}
	m2 := workerSandboxManifest(ReadyItem{WorkID: "w", NodeID: "n"}, "")
	if m2.WorkingDir != "" || m2.Filesystem != sandbox.FSIsolated {
		t.Fatalf("no source → isolated workspace, got dir=%q fs=%q", m2.WorkingDir, m2.Filesystem)
	}
}

// TestWorkerSandboxManifest_SandboxPathWorksOnThisHost executes a trivial
// command through the derived manifest on the real host, proving the
// production wiring is viable end-to-end (Prepare → exec → Cleanup).
func TestWorkerSandboxManifest_SandboxPathWorksOnThisHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix echo command")
	}
	item := ReadyItem{WorkID: "wrk_smoke", NodeID: "n", Run: "echo sandbox-ok"}
	m := workerSandboxManifest(item, "")
	res := runCommand(context.Background(), item.Run, item.Env, 10*time.Second, nil, "", &m)
	if res.Status != "succeeded" || !strings.Contains(string(res.CombinedLog), "sandbox-ok") {
		t.Fatalf("status=%q exit=%d log=%q", res.Status, res.ExitCode, string(res.CombinedLog))
	}
}
