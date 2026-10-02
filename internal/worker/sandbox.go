package worker

import (
	"strings"

	"github.com/JonasAbde/works-execution/internal/sandbox"
)

// workerSandboxManifest derives the Hermetic Execution Standard (#111)
// manifest for one leased node from the ReadyItem the control plane sent.
// The production worker path (Worker.execute) ALWAYS executes through
// sandbox.Prepare with this manifest — the legacy full-os.Environ branch
// in runCommand is reserved for explicit nil-manifest callers (tests,
// legacy seams), never for production leases.
//
// Derivation laws:
//
//   - Environment allow-list is exactly the node's declared Env keys.
//     Nothing else from the worker process environment crosses the
//     execution boundary (the credential-isolation invariant: a leased
//     node must not observe worker credentials or control-plane config).
//   - Network policy is derived from the node's declared side effects:
//     network_egress / external_api_call → allow-list policy, anything
//     else → deny. The scheduler's hard filter already restricts
//     egress-declaring nodes to runners that advertise network, so the
//     worker-side decision records the policy without a second
//     authority check.
//   - Filesystem: a source checkout pins the workdir (the node's Run
//     executes in the repo it was dispatched for); without source the
//     node gets a fresh isolated per-attempt workspace.
//   - ProbeNetwork stays false: V1 records the network policy decision
//     (Prepared.NetworkBlocked) rather than refusing work on hosts that
//     have a default route; syscall-level enforcement is the V2 netns
//     concern documented in internal/sandbox/hermetic.go.
func workerSandboxManifest(item ReadyItem, sourceDir string) sandbox.Manifest {
	net := sandbox.NetworkDeny
	var allow []string
	for _, se := range item.SideEffects {
		if se == "network_egress" || se == "external_api_call" {
			net = sandbox.NetworkAllow
			allow = []string{"declared-by-node"}
		}
	}
	environment := make(map[string]string, len(item.Env))
	for k := range item.Env {
		environment[k] = ""
	}
	fs := sandbox.FSIsolated
	workingDir := ""
	if sourceDir != "" {
		fs = sandbox.FSShared
		workingDir = sourceDir
	}
	return sandbox.Manifest{
		ActionID:    item.WorkID + "/" + item.NodeID,
		Network:     net,
		AllowList:   allow,
		Filesystem:  fs,
		Environment: environment,
		WorkingDir:  workingDir,
	}
}

// hasNetworkSideEffect reports whether the node declared egress, kept
// here so evidence/debug code can mirror the manifest decision.
func hasNetworkSideEffect(item ReadyItem) bool {
	for _, se := range item.SideEffects {
		if se == "network_egress" || se == "external_api_call" {
			return true
		}
	}
	return false
}

// envKeysOf is a deterministic helper for logging: the sorted env keys of
// a ReadyItem (values are never logged).
func envKeysOf(item ReadyItem) string {
	keys := make([]string, 0, len(item.Env))
	for k := range item.Env {
		keys = append(keys, k)
	}
	// small n; insertion-sort keeps the helper dependency-free
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return strings.Join(keys, ",")
}
