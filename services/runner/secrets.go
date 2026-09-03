package runner

// Execution-time secret REF resolution (k-impl-051, ADR-0022).
//
// The law this file enforces: only REFS cross trust boundaries. A Step.Env
// value of the form "secret://<provider>/<name>" is inert plan data that the
// control plane, the evidence bundle and the audit log may all carry. The
// VALUE it points at exists in exactly one place: the environment of the
// child process that runStep spawns, created at execution time and dropped
// when the function returns.
//
// Consequences for every line below:
//   - ResolveEnv builds a NEW map. It never writes a value back into
//     step.Env, because the Step (and the plan it came from) outlives the
//     step and is reachable from evidence/audit paths.
//   - Every error this package returns names the REF (inert, safe to log)
//     and never a value. Values are not masked out of errors as a
//     last-ditch scrub; they are structurally absent from the error path.
//   - Resolution happens per step, immediately before exec. Nothing is
//     cached on Options, on the resolver, or in any package-level variable.
//
// Backward compatibility interlock: Options.SecretResolver is nil by
// default, and with a nil resolver runStep behaves exactly as it did
// before this file existed - a "secret://..." string passes through into
// the child env literally. Callers opt in; nobody inherits new behavior by
// upgrading.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

// SecretRefPrefix marks a Step.Env value as a secret REF rather than a
// value. It is the frozen contract:secret.ref/1.0 scheme.
const SecretRefPrefix = "secret://"

// ErrSecretRefUnresolved is the runner-side sentinel wrapped around every
// resolution failure so callers can errors.Is their way back to "a secret
// REF did not resolve" without string matching. Its message never carries a
// value.
var ErrSecretRefUnresolved = errors.New("runner: secret ref unresolved")

// SecretResolver turns the secret REFs appearing as Step.Env values into
// their values, at execution time, for one step's env map.
//
// Contract:
//   - ResolveEnv returns a new map with every REF value replaced by its
//     resolved value; non-REF values are copied untouched.
//   - On any failure it returns a nil map and an error naming the offending
//     REF. Implementations MUST NOT place a resolved value in the error,
//     the same law packages/secrets encodes for ErrSecretNotFound.
//   - Implementations MUST NOT cache resolved values past the call.
type SecretResolver interface {
	ResolveEnv(ctx context.Context, scope string, env map[string]string) (map[string]string, error)
}

// envSecretResolver is the SecretResolver adapter over the frozen
// packages/secrets EnvResolver kernel. It owns no state beyond the default
// scope and the injected env lookup, so it resolves nothing ahead of time
// and holds no values between calls.
type envSecretResolver struct {
	// defaultScope is used when the caller passes an empty scope.
	defaultScope string
	// lookup is the (possibly faked) env boundary handed to the kernel.
	lookup secrets.LookupEnv
}

// NewEnvSecretResolver returns a SecretResolver backed by
// packages/secrets.EnvResolver, i.e. the documented mapping
// secret://<provider>/<name> => SECRET_<PROVIDER>_<NAME> (uppercase,
// non-alphanumerics to underscores), with a non-empty scope appended as
// "__<SCOPE>".
//
// scope is the default lookup scope; an explicit scope passed to ResolveEnv
// (Options.SecretScope) wins over it. lookup may be nil, which means the
// real process env via os.LookupEnv. Tests inject a fake.
func NewEnvSecretResolver(scope string, lookup func(string) (string, bool)) SecretResolver {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &envSecretResolver{
		defaultScope: scope,
		lookup:       secrets.LookupEnv(lookup),
	}
}

// ResolveEnv implements SecretResolver. See the interface contract.
func (r *envSecretResolver) ResolveEnv(ctx context.Context, scope string, env map[string]string) (map[string]string, error) {
	effective := scope
	if effective == "" {
		effective = r.defaultScope
	}
	kernel := &secrets.EnvResolver{LookupEnv: r.lookup}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if !IsSecretRef(v) {
			// Literal value: not our business, pass it through.
			out[k] = v
			continue
		}
		ref, err := secrets.ParseRef(v)
		if err != nil {
			// ParseRef's message echoes the offending ref string, which is
			// inert data by definition - a value would not parse-fail.
			return nil, fmt.Errorf("%w: env key %q: %w", ErrSecretRefUnresolved, k, err)
		}
		resolved, err := kernel.Resolve(ctx, ref, effective)
		if err != nil {
			// The kernel sentinel's message is the ref only. Nothing here
			// touches the value: on this path there is no value.
			return nil, fmt.Errorf("%w: env key %q: %w", ErrSecretRefUnresolved, k, err)
		}
		out[k] = resolved
	}
	return out, nil
}

// IsSecretRef reports whether an env value is a secret REF rather than a
// plain value. Only the scheme prefix is checked here; shape validation is
// the kernel's job (ParseRef) so this stays the single cheap gate used on
// the hot path.
func IsSecretRef(v string) bool {
	return strings.HasPrefix(v, SecretRefPrefix)
}
