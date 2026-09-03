package worker

// Execution-time secret REF resolution on the production worker path
// (k-057, ADR-0022).
//
// The law: payloads and audit records may only carry a secret REF
// ("secret://<provider>/<name>"), and the kernel resolves REF -> VALUE at
// execution time, inside the worker's env, never serializing the value
// (packages/secrets/secrets.go is the authoritative statement). v0.3.2
// shipped that law for services/runner; internal/worker (what
// cmd/works-worker actually executes) still passed ReadyItem.Env to
// subprocesses literally, so refs reached the child env unresolved. This
// file closes that gap.
//
// Semantics are inherited verbatim from the v0.3.2 adapter:
// services/runner.NewEnvSecretResolver / ResolveEnv, which is backed by the
// packages/secrets kernel (mapping: secret://<provider>/<name> =>
// SECRET_<PROVIDER>_<NAME>, uppercase, non-alphanumerics to underscores).
// Reusing the runner adapter instead of re-implementing it keeps exactly
// one env-resolution path in the repo; the internal/worker ->
// services/runner import is acyclic (services/runner depends only on
// packages/secrets inside this module).
//
// Design decisions:
//   - ALWAYS ON, no env gate. ADR-0022 is law, not a feature flag; the
//     env-gating noted in v0.3.2 was a deviation. Refs are already opt-in
//     by publishers (a node only sees a secret:// value if the plan put
//     one there), so unconditional resolution adds no attack surface for
//     plans that never use refs.
//   - Fail closed: any resolution failure means the node does NOT execute.
//     The caller returns a failed execResult whose log names the REF only
//     (inert data, safe to persist in evidence), never a value. This
//     follows the existing "sandbox prepare failed" pattern.
//   - Backward-compat interlock: when no value in the env map carries the
//     secret:// prefix (the overwhelmingly common case), the input map is
//     returned IDENTICALLY -- same map header, no copy, no allocation --
//     so every downstream code path (legacy os.Environ append, sandbox
//     scrub, docker RunOptions) is byte-for-byte what it was before this
//     file existed. Proven by TestResolveItemEnv.

import (
	"context"

	"github.com/JonasAbde/works-execution/services/runner"
)

// resolveItemEnv replaces every "secret://..." value in env with its
// resolved value at execution time. It returns a NEW map when any ref was
// present (the caller's map is never mutated -- ReadyItem outlives this
// call and is reachable from evidence/audit paths) and the SAME map when
// no ref is present. On failure it returns a nil map and an error naming
// the offending REF only; resolved values are structurally absent from the
// error path. Nothing is cached: the resolver is constructed per call and
// holds no state.
func resolveItemEnv(ctx context.Context, env map[string]string) (map[string]string, error) {
	if len(env) == 0 {
		return env, nil
	}
	hasRef := false
	for _, v := range env {
		if runner.IsSecretRef(v) {
			hasRef = true
			break
		}
	}
	if !hasRef {
		// Fast path: no refs in this item's env. Return the input map
		// unchanged so behavior is byte-identical to the pre-k057 path.
		return env, nil
	}
	// Scope "" is the global lookup (documented kernel mapping). A nil
	// lookup means the real process env via os.LookupEnv.
	return runner.NewEnvSecretResolver("", nil).ResolveEnv(ctx, "", env)
}
