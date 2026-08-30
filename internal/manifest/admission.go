// Package manifest implements admission control for works-execution.
//
// The action-manifest contract (docs/standards/schemas/action-manifest.schema.json)
// declares each Node's required capabilities, side effects, retries, and
// caching. ValidateAndEnrich is the gate that runs before persistence: it
// fills safe defaults for missing fields and rejects nodes whose declared
// side effects or permissions are not on the platform allow-list.
//
// This is the first slice of per-node admission. The full schema validator
// (internal/standards) is reserved for the action registry (slice 5+).
// Here we only enforce the parts that affect the work-graph control plane:
// permissions, side_effects, timeout_seconds, retries.max_attempts,
// retries.backoff, and cache.enabled.
package manifest

import (
	"errors"
	"fmt"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Defaults applied when the caller does not declare a field. Chosen to
// minimise blast radius if a work is admitted with minimal capability info:
// short timeout, conservative retry budget, cache off, read-only permissions.
const (
	DefaultTimeoutSeconds   = 600
	DefaultRetryMaxAttempts = 2
	DefaultBackoff          = "exponential"
	DefaultCacheScope       = "organization"
)

// Allowed permission values, mirroring the action-manifest schema enum.
// "privileged" is intentionally kept on the list so that callers can declare
// it explicitly; the downstream scheduler is responsible for policy review.
var AllowedPermissions = []string{
	"read", "write", "execute", "network", "secrets", "privileged",
}

// Allowed side-effect values, mirroring the action-manifest schema enum.
var AllowedSideEffects = []string{
	"network_egress", "filesystem_write", "deployment",
	"secret_consumption", "external_api_call", "state_mutation",
}

// Allowed backoff values, mirroring the schema enum.
var AllowedBackoffs = []string{"none", "linear", "exponential"}

// Allowed cache.scope values, mirroring the schema enum.
var AllowedCacheScopes = []string{"worker-local", "organization", "global"}

// ErrUndeclaredPermission is returned when a node declares a permission
// outside AllowedPermissions. Wrapped so callers can errors.Is it.
var ErrUndeclaredPermission = errors.New("admission: undeclared permission")

// ErrUndeclaredSideEffect is returned when a node declares a side effect
// outside AllowedSideEffects.
var ErrUndeclaredSideEffect = errors.New("admission: undeclared side effect")

// ErrInvalidCapability is returned for any other per-node capability
// validation failure (bad retry policy, bad backoff, bad cache scope,
// retries.max_attempts out of range, etc.).
var ErrInvalidCapability = errors.New("admission: invalid capability")

// ValidateAndEnrich walks every node in w.Graph and applies the admission
// policy. It mutates w in place to fill defaults, then returns the first
// failure it encounters (or nil). On success w is safe to persist.
//
// The mutation is in-place by design: the caller already owns w and we want
// the persisted record to reflect the defaults we admitted, not the raw
// input. Concurrent callers must serialise access to w themselves.
func ValidateAndEnrich(w *workgraph.Work) error {
	if w == nil {
		return fmt.Errorf("%w: work is nil", ErrInvalidCapability)
	}
	if len(w.Graph.Nodes) == 0 {
		return fmt.Errorf("%w: graph has no nodes", ErrInvalidCapability)
	}
	for id, n := range w.Graph.Nodes {
		if err := validateAndEnrichNode(id, &n); err != nil {
			return err
		}
		w.Graph.Nodes[id] = n
	}
	return nil
}

// validateAndEnrichNode checks one node and fills safe defaults. Returned
// errors are wrapped with the node id so admission failures are easy to
// pinpoint in the API response.
func validateAndEnrichNode(id string, n *workgraph.Node) error {
	if n == nil {
		return fmt.Errorf("%w: node %q is nil", ErrInvalidCapability, id)
	}
	if n.Run == "" {
		return fmt.Errorf("%w: node %q has empty run", ErrInvalidCapability, id)
	}

	// --- timeout_seconds ---
	// 0 means "caller did not declare"; schema allows 1..86400. We default
	// to 600 (10 minutes) which fits most build/test steps in slice 1-3.
	if n.TimeoutS == 0 {
		n.TimeoutS = DefaultTimeoutSeconds
	}
	if n.TimeoutS < 1 || n.TimeoutS > 86400 {
		return fmt.Errorf("%w: node %q timeout_s=%d out of range 1..86400",
			ErrInvalidCapability, id, n.TimeoutS)
	}

	// --- permissions ---
	// Schema requires minItems=1; we default to ["read"] which is the
	// least-privileged capability set.
	if len(n.Permissions) == 0 {
		n.Permissions = []string{"read"}
	}
	for _, p := range n.Permissions {
		if !contains(AllowedPermissions, p) {
			return fmt.Errorf("%w: node %q permission %q not in allow-list %v",
				ErrUndeclaredPermission, id, p, AllowedPermissions)
		}
	}

	// --- side_effects ---
	// Empty list is fine: read-only nodes declare nothing. Anything on the
	// list must be a known side-effect class.
	for _, s := range n.SideEffects {
		if !contains(AllowedSideEffects, s) {
			return fmt.Errorf("%w: node %q side_effect %q not in allow-list %v",
				ErrUndeclaredSideEffect, id, s, AllowedSideEffects)
		}
	}

	// --- retries ---
	// Schema allows retries.max_attempts in [1..5]. If unset we fill
	// {max_attempts: 2, backoff: exponential}.
	if n.Retries == nil {
		n.Retries = &workgraph.RetrySpec{
			MaxAttempts: DefaultRetryMaxAttempts,
			Backoff:     DefaultBackoff,
		}
	} else {
		if n.Retries.MaxAttempts < 1 || n.Retries.MaxAttempts > 5 {
			return fmt.Errorf("%w: node %q retries.max_attempts=%d out of range 1..5",
				ErrInvalidCapability, id, n.Retries.MaxAttempts)
		}
		if n.Retries.Backoff == "" {
			n.Retries.Backoff = DefaultBackoff
		}
		if !contains(AllowedBackoffs, n.Retries.Backoff) {
			return fmt.Errorf("%w: node %q retries.backoff=%q not in allow-list %v",
				ErrInvalidCapability, id, n.Retries.Backoff, AllowedBackoffs)
		}
	}

	// --- cache ---
	// Schema: cache.enabled defaults to false; we materialise an explicit
	// spec so downstream code never has to nil-check.
	if n.CacheSpec == nil {
		n.CacheSpec = &workgraph.CacheSpec{
			Enabled: false,
			Scope:   DefaultCacheScope,
		}
	} else if n.CacheSpec.Scope == "" {
		n.CacheSpec.Scope = DefaultCacheScope
	} else if !contains(AllowedCacheScopes, n.CacheSpec.Scope) {
		return fmt.Errorf("%w: node %q cache.scope=%q not in allow-list %v",
			ErrInvalidCapability, id, n.CacheSpec.Scope, AllowedCacheScopes)
	}

	return nil
}

// contains is a tiny helper; avoids importing slices just for one call.
func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// FormatError returns a human-friendly summary of an admission error. It is
// safe to call on any error returned by ValidateAndEnrich. Returns the
// error's own message when no recognised sentinel is wrapped.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrUndeclaredPermission):
		return "undeclared permission: " + stripPrefix(err.Error(), "admission: ")
	case errors.Is(err, ErrUndeclaredSideEffect):
		return "undeclared side effect: " + stripPrefix(err.Error(), "admission: ")
	case errors.Is(err, ErrInvalidCapability):
		return "invalid capability: " + stripPrefix(err.Error(), "admission: ")
	default:
		return err.Error()
	}
}

// stripPrefix removes a leading prefix from s. Used to keep formatted
// messages short.
func stripPrefix(s, prefix string) string {
	return strings.TrimPrefix(s, prefix)
}
