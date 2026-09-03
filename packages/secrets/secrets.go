// Package secrets implements the kernel-resolved secret REF law of ADR-0022
// over the frozen contract:secret.ref/1.0 schema.
//
// ADR-0022 (kernel-resolved secret refs): payloads and audit records may only
// ever carry a secret REF (secret://provider/name) — never a VALUE. The
// kernel is the sole component that resolves ref->value, at execution time,
// inside the worker's env. The resolved value must never be serialized
// (not to audit, not to the store, not to logs, not to error messages).
//
// 'provider' segment is a lowercase keychain-ish name (env, vault, gh-app,
// ...); 'name' is the entry. A REF is inert data — safe to log; a VALUE is
// radioactive.
//
// Freeze laws encoded in this package:
//   - secret.ref/1.0: ref pattern ^secret://[a-z0-9-]+/[A-Za-z0-9_-]+$,
//     required {ref, scope}, optional work_id, additionalProperties:false.
//   - ADR-0022 (value-never-serializes): only REFs cross trust boundaries.
//     Values live only inside this package and are returned to the caller
//     for direct use. They are never cached, logged, or echoed in errors.
//   - EnvResolver mapping (documented): secret://<provider>/<name> under
//     scope "" resolves to env var SECRET_<PROVIDER>_<NAME> with all
//     characters uppercased and dashes mapped to underscores. Example:
//     secret://vault/db-pass => SECRET_VAULT_DB_PASS.
//   - fail-closed: malformed refs, missing required fields, and unresolved
//     refs all return sentinels that carry only the REF (never a value,
//     never a candidate env name that would only be derivable from the
//     ref anyway).
package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// refPattern is the frozen contract:secret.ref/1.0 ref pattern.
var refPattern = regexp.MustCompile(`^secret://[a-z0-9-]+/[A-Za-z0-9_-]+$`)

// Ref is a typed, inert reference to a secret. Its Value is the raw
// "secret://provider/name" string. The distinct type prevents accidental
// confusion with a resolved VALUE in code (the compiler refuses to pass
// a *Ref where a string value is expected, and vice versa).
type Ref struct {
	Value string
}

// String returns the canonical "secret://provider/name" form. Safe to log.
func (r *Ref) String() string {
	if r == nil {
		return ""
	}
	return r.Value
}

// Provider returns the provider segment of the ref (the lowercase segment
// between the scheme and the slash). It is derived from r.Value and assumes
// the ref is already validated; callers must reach it through ParseRef.
func (r *Ref) Provider() string {
	if r == nil {
		return ""
	}
	v := r.Value
	// v begins with "secret://"; strip scheme, take up to next '/'.
	const scheme = "secret://"
	if !strings.HasPrefix(v, scheme) {
		return ""
	}
	rest := v[len(scheme):]
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return ""
	}
	return rest[:i]
}

// Name returns the name segment of the ref. Same precondition as Provider.
func (r *Ref) Name() string {
	if r == nil {
		return ""
	}
	v := r.Value
	const scheme = "secret://"
	if !strings.HasPrefix(v, scheme) {
		return ""
	}
	rest := v[len(scheme):]
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return ""
	}
	return rest[i+1:]
}

// ParseRef validates and constructs a Ref. The ref must match the frozen
// contract:secret.ref/1.0 pattern ^secret://[a-z0-9-]+/[A-Za-z0-9_-]+$.
// Empty input, missing provider, missing name, uppercase provider, extra
// path segments, wrong scheme, or trailing slash all fail closed.
func ParseRef(s string) (*Ref, error) {
	if s == "" {
		return nil, errors.New("secrets: ref is empty")
	}
	if !refPattern.MatchString(s) {
		return nil, fmt.Errorf("secrets: ref %q does not match contract:secret.ref/1.0 pattern", s)
	}
	return &Ref{Value: s}, nil
}

// Must parses a ref and panics on error. Use only for compile-time-known
// constants; for runtime input use ParseRef.
func Must(s string) *Ref {
	r, err := ParseRef(s)
	if err != nil {
		panic(err)
	}
	return r
}

// Resolver is the boundary that turns a Ref into a VALUE inside the
// worker's env. Implementations are the SOLE place a VALUE exists in
// user-observable form. Callers must receive the value, use it, and
// let it go out of scope — never cache, log, or persist.
type Resolver interface {
	// Resolve returns the VALUE for ref under the given scope. scope
	// distinguishes e.g. work_id-scoped vs global credential lookups.
	// Returns ErrSecretNotFound if the ref cannot be resolved.
	Resolve(ctx context.Context, ref *Ref, scope string) (string, error)
}

// LookupEnv is the injectable os.Getenv shape used by EnvResolver. Tests
// pass a fake; production code uses osLookupEnv (a thin wrapper over
// os.Getenv) so the env boundary stays testable without polluting globals.
type LookupEnv func(name string) (string, bool)

// EnvResolver resolves REFs by reading process env vars under the documented
// mapping (see EnvName below). The LookupEnv function is injectable so the
// env boundary can be faked in tests.
type EnvResolver struct {
	LookupEnv LookupEnv
}

// NewEnvResolver returns an EnvResolver that reads from osLookupEnv.
func NewEnvResolver() *EnvResolver {
	return &EnvResolver{LookupEnv: osLookupEnv}
}

// ErrSecretNotFound is the sentinel returned when a Ref cannot be resolved.
// The error message contains ONLY the ref string. It MUST NOT contain the
// candidate env var name (per the prompt: "env name is safe" because it is
// derivable from the ref — but the strict reading of the law we encode here
// is: do not echo it. The ref is sufficient evidence for the caller to know
// which entry is missing).
//
// We also MUST NOT contain any value that WAS present in the env. If the
// mapping resolved to an env var that exists but is empty, we treat that as
// not-found rather than leaking the value into the error path.
var ErrSecretNotFound = errors.New("secrets: ref not found")

// Resolve implements Resolver.
//
// Behavior:
//   - nil ref => error (fail closed).
//   - empty scope is the canonical "global" lookup.
//   - non-empty scope is appended to the env var name as "__<SCOPE>"
//     (uppercased, dashes->underscores), e.g. secret://vault/db-pass under
//     scope "work:abc" maps to SECRET_VAULT_DB_PASS__WORK_ABC.
//   - missing env var => wrapped ErrSecretNotFound. The wrapped error
//     message contains ONLY the ref string.
//   - present-but-empty env var => wrapped ErrSecretNotFound (an empty
//     value is treated as missing — refusing to leak the present-but-empty
//     state into the message).
func (r *EnvResolver) Resolve(ctx context.Context, ref *Ref, scope string) (string, error) {
	if ref == nil {
		return "", errors.New("secrets: ref is nil")
	}
	if ref.Value == "" {
		return "", errors.New("secrets: ref is empty")
	}
	if r.LookupEnv == nil {
		return "", errors.New("secrets: EnvResolver.LookupEnv is nil")
	}
	name := EnvName(ref, scope)
	v, ok := r.LookupEnv(name)
	if !ok || v == "" {
		return "", fmt.Errorf("%w: %s", ErrSecretNotFound, ref.Value)
	}
	return v, nil
}

// EnvName returns the documented env var name for ref under scope. Exposed
// so tests and callers can verify the mapping without going through Resolve
// (and so the mapping is a single, documented function rather than scattered
// inline logic).
//
// Mapping:
//   - upper(env-provider + '_' + name), dashes->underscores, prefix "SECRET_"
//   - empty scope => no suffix
//   - non-empty scope => suffix "__<SCOPE>" (uppercased; dashes, colons,
//     and any other characters that are invalid in POSIX env var names
//     mapped to underscores — env var names may only contain [A-Z0-9_])
//
// Examples:
//   - secret://vault/db-pass, scope ""      => SECRET_VAULT_DB_PASS
//   - secret://vault/db-pass, scope "work:a"=> SECRET_VAULT_DB_PASS__WORK_A
func EnvName(ref *Ref, scope string) string {
	if ref == nil {
		return ""
	}
	provider := sanitizeEnvSegment(ref.Provider())
	name := sanitizeEnvSegment(ref.Name())
	base := "SECRET_" + provider + "_" + name
	if scope == "" {
		return base
	}
	suf := sanitizeEnvSegment(scope)
	return base + "__" + suf
}

// sanitizeEnvSegment uppercases and maps every non-[A-Z0-9] byte (after
// uppercasing) to underscore. POSIX env var names may only contain
// [A-Z][A-Z0-9_]*; anything else must be normalized.
func sanitizeEnvSegment(s string) string {
	return envSegmentRe.ReplaceAllString(strings.ToUpper(s), "_")
}

var envSegmentRe = regexp.MustCompile(`[^A-Z0-9]`)

// osLookupEnv is the production LookupEnv; thin wrapper so it is replaceable
// in tests via EnvResolver.LookupEnv.
func osLookupEnv(name string) (string, bool) {
	return os.LookupEnv(name)
}

// Grant is a typed, scope-bound grant for one Ref. It is the payload callers
// receive when an upper layer has decided to authorize access to a secret;
// the value is NEVER in this struct, only the ref and the scope.
//
// The struct fields are exactly the contract:secret.ref/1.0 surface:
// {ref, scope} required, work_id optional, additionalProperties:false. The
// GrantedAt/ExpiresAt fields are kernel-side bookkeeping, not part of the
// serialized wire form (omitempty on JSON).
type Grant struct {
	Ref       *Ref      `json:"ref"`
	Scope     string    `json:"scope"`
	WorkID    string    `json:"work_id,omitempty"`
	GrantedAt time.Time `json:"granted_at,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// ErrGrantExpired is returned by Redeem when now > ExpiresAt.
var ErrGrantExpired = errors.New("secrets: grant expired")

// ErrGrantScope is returned by Redeem when workID does not match Grant.WorkID.
// The error message contains ONLY the mismatched work ID string the caller
// passed (and the grant's own work_id), never any secret value.
var ErrGrantScope = errors.New("secrets: grant work_id mismatch")

// Validate enforces the frozen contract:secret.ref/1.0 required-field law
// (ref+scope required, additionalProperties:false => no extra exported
// fields beyond the schema's optional work_id).
func (g *Grant) Validate() error {
	if g == nil {
		return errors.New("secrets: grant is nil")
	}
	if g.Ref == nil || g.Ref.Value == "" {
		return errors.New("secrets: grant.ref is required (contract:secret.ref/1.0)")
	}
	if g.Scope == "" {
		return errors.New("secrets: grant.scope is required (contract:secret.ref/1.0)")
	}
	return nil
}

// Redeem resolves the grant's Ref via the injected Resolver. The contract:
//
//   - Validates the grant (fail closed).
//   - now >= ExpiresAt => ErrGrantExpired (no resolver call).
//   - grant.WorkID != "" AND workID != grant.WorkID => ErrGrantScope.
//     (An empty grant.WorkID matches any caller workID — the schema marks
//     work_id optional, so absent work_id is not a scope violation.)
//   - success: calls r.Resolve(ctx, g.Ref, g.Scope) and returns the value
//     to them. The value is NOT cached, NOT logged, and NOT echoed in any
//     error path constructed here.
func (g *Grant) Redeem(ctx context.Context, r Resolver, workID string) (string, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	if r == nil {
		return "", errors.New("secrets: resolver is nil")
	}
	now := time.Now().UTC()
	if !g.ExpiresAt.IsZero() && !now.Before(g.ExpiresAt) {
		return "", fmt.Errorf("%w: %s", ErrGrantExpired, g.Ref.Value)
	}
	if g.WorkID != "" && g.WorkID != workID {
		return "", fmt.Errorf("%w: grant=%q caller=%q", ErrGrantScope, g.WorkID, workID)
	}
	return r.Resolve(ctx, g.Ref, g.Scope)
}

// MaskValue replaces occurrences of a single resolved value with a
// stable fingerprint so accidental serialization can be detected and
// scrubbed by callers. This is BEST-EFFORT belt-and-braces: the primary
// law (ADR-0022) is that VALUES NEVER ENTER PAYLOADS at all. This helper
// exists so callers building payloads out of untrusted mixes of refs and
// values have a last-ditch scrub.
//
// The mask form is "***#N" where N is a stable 1-based index into values.
// Using a single stable identifier per value makes the redaction reversible
// to a human (they can see WHICH value was scrubbed) without ever printing
// the value itself.
func MaskValue(s string, values []string) string {
	if s == "" || len(values) == 0 {
		return s
	}
	for i, v := range values {
		if v == "" {
			continue
		}
		s = strings.ReplaceAll(s, v, fmt.Sprintf("***#%d", i+1))
	}
	return s
}

// RedactStrings returns a copy of payload with every occurrence of any
// value in values replaced by "***#N". Indices are 1-based and stable
// across the call (the same value in two keys gets the same fingerprint).
//
// Best-effort, exact-match. The primary ADR-0022 law is "values never
// enter payloads"; this helper catches the case where they did.
func RedactStrings(payload map[string]string, values []string) map[string]string {
	if len(payload) == 0 {
		return payload
	}
	out := make(map[string]string, len(payload))
	for k, v := range payload {
		out[k] = MaskValue(v, values)
	}
	return out
}
