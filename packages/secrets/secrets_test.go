// k-046 tests — contract:secret.ref/1.0 + ADR-0022 kernel-resolved secret
// ref law.
//
// Freeze laws under test:
//   - ParseRef fail-closed on every adversarial input
//   - EnvResolver mapping is SECRET_<PROVIDER>_<NAME> uppercased with
//     dashes->underscores, scope "" -> no suffix, scope "x" -> __X suffix
//   - Grant.Redeem expiry/scope enforcement without ever leaking values
//   - RedactStrings exact-value replacement
//   - ADR-0022 leak law: no error string produced by this package may
//     ever contain the sentinel resolved value
package secrets_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

// fakeValue is the ADR-0022 leak-test sentinel. Set it in env, force every
// failure path in this package, and assert no .Error() string ever contains
// it. The string is chosen to be obviously fake (never a real value, never
// an env var name, never a ref segment) so a hit is unambiguous.
const fakeValue = "super-secret-xyz"

func mustRef(t *testing.T, s string) *secrets.Ref {
	t.Helper()
	r, err := secrets.ParseRef(s)
	if err != nil {
		t.Fatalf("ParseRef(%q) unexpected err: %v", s, err)
	}
	return r
}

// -----------------------------------------------------------------------------
// 1. ParseRef adversarial table
// -----------------------------------------------------------------------------

func TestParseRefAdversarial(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		// --- valid refs ---
		{name: "minimal provider+name", in: "secret://a/b", wantErr: false},
		{name: "two-letter provider", in: "secret://gh/x", wantErr: false},
		{name: "dashed provider", in: "secret://vault/db-pass", wantErr: false},
		{name: "numeric name", in: "secret://vault/db1", wantErr: false},
		{name: "underscored name", in: "secret://vault/db_pass", wantErr: false},
		{name: "dashes and underscores in name", in: "secret://vault/db-pass_v2", wantErr: false},

		// --- adversarial: uppercase provider ---
		{name: "uppercase provider", in: "secret://Vault/db-pass", wantErr: true},
		{name: "all-caps provider", in: "secret://VAULT/db", wantErr: true},
		{name: "single uppercase char in provider", in: "secret://aB/c", wantErr: true},

		// --- adversarial: uppercase name (allowed by schema [A-Za-z0-9_-]+) ---
		{name: "uppercase name (allowed)", in: "secret://vault/DB-PASS", wantErr: false},

		// --- adversarial: empty / missing segments ---
		{name: "empty string", in: "", wantErr: true},
		{name: "no provider", in: "secret:///x", wantErr: true},
		{name: "no name", in: "secret://vault/", wantErr: true},
		{name: "only scheme", in: "secret://", wantErr: true},

		// --- adversarial: wrong scheme ---
		{name: "http scheme", in: "http://x", wantErr: true},
		{name: "https scheme", in: "https://vault/db", wantErr: true},
		{name: "no scheme", in: "vault/db", wantErr: true},

		// --- adversarial: extra path segments ---
		{name: "three segments", in: "secret://a/b/c", wantErr: true},
		{name: "four segments", in: "secret://a/b/c/d", wantErr: true},

		// --- adversarial: trailing slash already covered by empty-name case ---

		// --- adversarial: invalid characters ---
		{name: "space in name", in: "secret://vault/db pass", wantErr: true},
		{name: "dot in provider", in: "secret://vault.com/db", wantErr: true},
		{name: "dot in name", in: "secret://vault/db.pass", wantErr: true},
		{name: "colon in name", in: "secret://vault/db:pass", wantErr: true},
		{name: "unicode in provider", in: "secret://váult/db", wantErr: true},

		// --- adversarial: leading/trailing whitespace ---
		{name: "trailing whitespace", in: "secret://vault/db ", wantErr: true},
		{name: "leading whitespace", in: " secret://vault/db", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r, err := secrets.ParseRef(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseRef(%q) succeeded, want error (got ref=%v)", tc.in, r)
				}
				if r != nil {
					t.Fatalf("ParseRef(%q) returned non-nil ref alongside error: %v", tc.in, r)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRef(%q) unexpected err: %v", tc.in, err)
			}
			if r == nil {
				t.Fatalf("ParseRef(%q) returned nil ref without error", tc.in)
			}
			if r.Value != tc.in {
				t.Fatalf("ref.Value = %q, want %q", r.Value, tc.in)
			}
		})
	}
}

func TestRefAccessors(t *testing.T) {
	r := mustRef(t, "secret://vault/db-pass")
	if got := r.Provider(); got != "vault" {
		t.Errorf("Provider() = %q, want %q", got, "vault")
	}
	if got := r.Name(); got != "db-pass" {
		t.Errorf("Name() = %q, want %q", got, "db-pass")
	}
	if got := r.String(); got != "secret://vault/db-pass" {
		t.Errorf("String() = %q, want canonical ref", got)
	}
}

func TestRefAccessorsNilSafe(t *testing.T) {
	var r *secrets.Ref
	if got := r.Provider(); got != "" {
		t.Errorf("nil Provider() = %q, want \"\"", got)
	}
	if got := r.Name(); got != "" {
		t.Errorf("nil Name() = %q, want \"\"", got)
	}
	if got := r.String(); got != "" {
		t.Errorf("nil String() = %q, want \"\"", got)
	}
}

func TestMustPanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Must(\"\") did not panic")
		}
	}()
	_ = secrets.Must("")
}

// -----------------------------------------------------------------------------
// 2. EnvResolver mapping table
// -----------------------------------------------------------------------------

func TestEnvNameMapping(t *testing.T) {
	cases := []struct {
		name  string
		ref   string
		scope string
		want  string
	}{
		{name: "vault/db-pass empty scope", ref: "secret://vault/db-pass", scope: "", want: "SECRET_VAULT_DB_PASS"},
		{name: "env/foo empty scope", ref: "secret://env/foo", scope: "", want: "SECRET_ENV_FOO"},
		{name: "gh-app/x scoped", ref: "secret://gh-app/x", scope: "work:abc", want: "SECRET_GH_APP_X__WORK_ABC"},
		{name: "single-letter segments", ref: "secret://a/b", scope: "", want: "SECRET_A_B"},
		{name: "numeric provider", ref: "secret://v1/db", scope: "", want: "SECRET_V1_DB"},
		{name: "underscores pass through", ref: "secret://vault/db_pass", scope: "", want: "SECRET_VAULT_DB_PASS"},
		{name: "scope with dashes", ref: "secret://vault/db", scope: "work-1", want: "SECRET_VAULT_DB__WORK_1"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := mustRef(t, tc.ref)
			got := secrets.EnvName(r, tc.scope)
			if got != tc.want {
				t.Fatalf("EnvName(%q, %q) = %q, want %q", tc.ref, tc.scope, got, tc.want)
			}
		})
	}
}

func TestEnvResolverMappingTable(t *testing.T) {
	// Fake env: every cell of the mapping table is set in this fake
	// LookupEnv so we can drive Resolve deterministically.
	fake := func(name string) (string, bool) {
		switch name {
		case "SECRET_VAULT_DB_PASS":
			return "vault-secret-value", true
		case "SECRET_ENV_FOO":
			return "foo-from-env", true
		case "SECRET_GH_APP_X__WORK_ABC":
			return "gh-app-credential", true
		case "SECRET_EMPTY":
			return "", true // present-but-empty
		}
		return "", false
	}
	r := &secrets.EnvResolver{LookupEnv: fake}
	ctx := context.Background()

	cases := []struct {
		name    string
		ref     string
		scope   string
		want    string
		wantErr bool
	}{
		{name: "vault/db-pass", ref: "secret://vault/db-pass", scope: "", want: "vault-secret-value"},
		{name: "env/foo", ref: "secret://env/foo", scope: "", want: "foo-from-env"},
		{name: "gh-app/x scoped", ref: "secret://gh-app/x", scope: "work:abc", want: "gh-app-credential"},
		{name: "present-but-empty => not found", ref: "secret://env/empty", scope: "", wantErr: true},
		{name: "missing => not found", ref: "secret://vault/missing", scope: "", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(ctx, mustRef(t, tc.ref), tc.scope)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) succeeded, want error (got value=%q)", tc.ref, got)
				}
				if !errors.Is(err, secrets.ErrSecretNotFound) {
					t.Fatalf("err = %v, want errors.Is ErrSecretNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q) unexpected err: %v", tc.ref, err)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

func TestEnvResolverNilRef(t *testing.T) {
	r := &secrets.EnvResolver{LookupEnv: func(string) (string, bool) { return "", false }}
	if _, err := r.Resolve(context.Background(), nil, ""); err == nil {
		t.Fatal("Resolve(nil) succeeded")
	}
}

func TestEnvResolverNilLookupEnv(t *testing.T) {
	r := &secrets.EnvResolver{}
	if _, err := r.Resolve(context.Background(), mustRef(t, "secret://vault/db"), ""); err == nil {
		t.Fatal("Resolve with nil LookupEnv succeeded")
	}
}

// -----------------------------------------------------------------------------
// 3. Grant.Validate() and Grant.Redeem() matrix
// -----------------------------------------------------------------------------

func TestGrantValidate(t *testing.T) {
	good := &secrets.Grant{
		Ref:       mustRef(t, "secret://vault/db-pass"),
		Scope:     "global",
		WorkID:    "work:abc",
		GrantedAt: time.Now().Add(-time.Minute),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good grant failed validation: %v", err)
	}

	if err := (*secrets.Grant)(nil).Validate(); err == nil {
		t.Fatal("nil grant validated")
	}
	noRef := &secrets.Grant{Scope: "x"}
	if err := noRef.Validate(); err == nil {
		t.Fatal("grant without ref validated")
	}
	noScope := &secrets.Grant{Ref: mustRef(t, "secret://vault/db-pass")}
	if err := noScope.Validate(); err == nil {
		t.Fatal("grant without scope validated (contract:secret.ref/1.0 required scope)")
	}
}

// fakeResolver is a Resolver stub for the Redeem matrix.
type fakeResolver struct {
	calledWithRef *secrets.Ref
	calledScope   string
	value         string
	err           error
}

func (f *fakeResolver) Resolve(_ context.Context, ref *secrets.Ref, scope string) (string, error) {
	f.calledWithRef = ref
	f.calledScope = scope
	return f.value, f.err
}

func TestRedeemExpiryScopeMatrix(t *testing.T) {
	ref := mustRef(t, "secret://vault/db-pass")
	futureExp := time.Now().Add(time.Hour)
	pastExp := time.Now().Add(-time.Hour)

	cases := []struct {
		name       string
		grant      *secrets.Grant
		workID     string
		resolveVal string
		resolveErr error
		wantErr    error
		wantValue  string
		wantCalled bool
	}{
		{
			name: "happy path: matching work_id, future expiry",
			grant: &secrets.Grant{
				Ref: ref, Scope: "global", WorkID: "work:abc",
				ExpiresAt: futureExp,
			},
			workID:     "work:abc",
			resolveVal: "the-actual-secret",
			wantValue:  "the-actual-secret",
			wantCalled: true,
		},
		{
			name: "happy path: empty work_id matches any caller",
			grant: &secrets.Grant{
				Ref: ref, Scope: "global", WorkID: "",
				ExpiresAt: futureExp,
			},
			workID:     "work:anything",
			resolveVal: "v",
			wantValue:  "v",
			wantCalled: true,
		},
		{
			name: "expired grant",
			grant: &secrets.Grant{
				Ref: ref, Scope: "global", WorkID: "work:abc",
				ExpiresAt: pastExp,
			},
			workID:     "work:abc",
			wantErr:    secrets.ErrGrantExpired,
			wantCalled: false,
		},
		{
			name: "work_id mismatch",
			grant: &secrets.Grant{
				Ref: ref, Scope: "global", WorkID: "work:abc",
				ExpiresAt: futureExp,
			},
			workID:     "work:xyz",
			wantErr:    secrets.ErrGrantScope,
			wantCalled: false,
		},
		{
			name: "resolver error propagates",
			grant: &secrets.Grant{
				Ref: ref, Scope: "global", WorkID: "",
				ExpiresAt: futureExp,
			},
			workID:     "work:abc",
			resolveErr: fmt.Errorf("upstream blow up"),
			wantErr:    fmt.Errorf("upstream blow up"),
			wantCalled: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeResolver{value: tc.resolveVal, err: tc.resolveErr}
			v, err := tc.grant.Redeem(context.Background(), f, tc.workID)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("Redeem succeeded, want err: %v", tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) && err.Error() != tc.wantErr.Error() {
					// errors.Is for wrapped fmt.Errorf may fail; allow either path.
					if !strings.Contains(err.Error(), tc.wantErr.Error()) {
						t.Fatalf("err = %v, want containing %v", err, tc.wantErr)
					}
				}
				if v != "" {
					t.Fatalf("Redeem returned value %q on error", v)
				}
				if tc.wantCalled != (f.calledWithRef != nil) {
					t.Fatalf("resolver.called = %v, want called=%v", f.calledWithRef != nil, tc.wantCalled)
				}
				return
			}
			if err != nil {
				t.Fatalf("Redeem unexpected err: %v", err)
			}
			if v != tc.wantValue {
				t.Fatalf("Redeem value = %q, want %q", v, tc.wantValue)
			}
			if f.calledWithRef == nil || f.calledWithRef.Value != ref.Value {
				t.Fatalf("resolver not called with correct ref: %+v", f.calledWithRef)
			}
			if f.calledScope != tc.grant.Scope {
				t.Fatalf("resolver called with scope %q, want %q", f.calledScope, tc.grant.Scope)
			}
		})
	}
}

func TestRedeemNilResolver(t *testing.T) {
	g := &secrets.Grant{
		Ref: mustRef(t, "secret://vault/db"), Scope: "global",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := g.Redeem(context.Background(), nil, ""); err == nil {
		t.Fatal("Redeem with nil resolver succeeded")
	}
}

func TestRedeemInvalidGrant(t *testing.T) {
	// Missing scope fails validation BEFORE expiry/resolver are touched.
	g := &secrets.Grant{
		Ref:       mustRef(t, "secret://vault/db"),
		ExpiresAt: time.Now().Add(-time.Hour), // would otherwise be expired
	}
	if err := g.Validate(); err == nil {
		t.Fatal("Validate accepted grant with empty scope")
	}
}

// -----------------------------------------------------------------------------
// 4. MaskValue / RedactStrings
// -----------------------------------------------------------------------------

func TestRedactStringsExactValueReplacement(t *testing.T) {
	payload := map[string]string{
		"username":   "alice",
		"password":   fakeValue,              // exact match
		"connection": "host=db " + fakeValue, // substring match
		"no_match":   "harmless",
		"two_in_one": fakeValue + "|" + fakeValue,
	}
	got := secrets.RedactStrings(payload, []string{fakeValue})
	if !strings.Contains(got["password"], "***#1") {
		t.Errorf("password not redacted: %q", got["password"])
	}
	if strings.Contains(got["password"], fakeValue) {
		t.Fatalf("password still contains sentinel value: %q", got["password"])
	}
	if !strings.Contains(got["connection"], "***#1") {
		t.Errorf("connection not redacted: %q", got["connection"])
	}
	if strings.Contains(got["connection"], fakeValue) {
		t.Fatalf("connection still contains sentinel: %q", got["connection"])
	}
	if got["username"] != "alice" {
		t.Errorf("username mutated: %q", got["username"])
	}
	if got["no_match"] != "harmless" {
		t.Errorf("no_match mutated: %q", got["no_match"])
	}
	// two_in_one should become ***#1|***#1
	if got["two_in_one"] != "***#1|***#1" {
		t.Errorf("two_in_one = %q, want %q", got["two_in_one"], "***#1|***#1")
	}
}

func TestMaskValueStableIndexing(t *testing.T) {
	values := []string{"first", "second"}
	if got := secrets.MaskValue("first then second then first", values); got != "***#1 then ***#2 then ***#1" {
		t.Fatalf("MaskValue stable index wrong: %q", got)
	}
}

func TestMaskValueEmptyInputs(t *testing.T) {
	if got := secrets.MaskValue("", []string{"x"}); got != "" {
		t.Errorf("empty input mutated: %q", got)
	}
	if got := secrets.MaskValue("hello", nil); got != "hello" {
		t.Errorf("nil values mutated: %q", got)
	}
	if got := secrets.MaskValue("hello", []string{""}); got != "hello" {
		t.Errorf("empty value in list mutated: %q", got)
	}
}

func TestRedactStringsEmptyPayload(t *testing.T) {
	// Should not panic on empty/nil payload.
	_ = secrets.RedactStrings(nil, []string{"x"})
	got := secrets.RedactStrings(map[string]string{}, []string{"x"})
	if len(got) != 0 {
		t.Errorf("empty payload produced output: %v", got)
	}
}

// -----------------------------------------------------------------------------
// 5. ADR-0022 leak law: no error string contains the sentinel resolved value
// -----------------------------------------------------------------------------

// TestNoErrorContainsResolvedValue is the canonical ADR-0022 leak test.
// We set the sentinel fake value in env under the documented mapping, then
// drive every error path in this package and assert no .Error() string
// contains the sentinel. This is the "belt-and-braces" guarantee in code
// form: the primary law is "values never enter payloads", this catches any
// regression where an error path accidentally constructs a message from a
// value that WAS present in the env.
func TestNoErrorContainsResolvedValue(t *testing.T) {
	// Set up env so EnvResolver would have a value to leak if it ever did.
	t.Setenv("SECRET_VAULT_DB_PASS", fakeValue)

	// 1. ParseRef rejects every adversarial input; none of those errors
	//    should mention the sentinel (they couldn't, but prove it).
	adversarial := []string{
		"",
		"secret://Vault/db",
		"secret:///x",
		"http://x",
		"secret://a/b/c",
		"secret://vault/",
		"secret://v/db pass",
		" secret://vault/db",
		"secret://vault/db ",
	}
	for _, s := range adversarial {
		if _, err := secrets.ParseRef(s); err != nil {
			if strings.Contains(err.Error(), fakeValue) {
				t.Fatalf("ParseRef(%q).Error() leaked sentinel: %v", s, err)
			}
		}
	}

	// 2. EnvResolver.Resolve against a present env returns the value —
	//    we deliberately DO NOT log it. We only check error messages.
	r := secrets.NewEnvResolver()

	// missing ref => ErrSecretNotFound; message should contain ONLY the ref.
	_, err := r.Resolve(context.Background(), mustRef(t, "secret://vault/missing-xyz"), "")
	if err == nil {
		t.Fatal("Resolve(missing) succeeded")
	}
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("missing ref err = %v, want ErrSecretNotFound", err)
	}
	if strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("missing-ref error leaked sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "secret://vault/missing-xyz") {
		t.Fatalf("missing-ref error should mention the ref: %v", err)
	}

	// present-but-empty value => ErrSecretNotFound; ensure empty env doesn't leak.
	t.Setenv("SECRET_VAULT_EMPTY_KEY", "")
	_, err = r.Resolve(context.Background(), mustRef(t, "secret://vault/empty-key"), "")
	if err == nil {
		t.Fatal("Resolve(present-but-empty) succeeded")
	}
	if strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("present-but-empty error leaked sentinel: %v", err)
	}
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("present-but-empty err = %v, want ErrSecretNotFound", err)
	}

	// 3. EnvResolver with nil LookupEnv => error must not echo sentinel.
	badR := &secrets.EnvResolver{}
	_, err = badR.Resolve(context.Background(), mustRef(t, "secret://vault/db-pass"), "")
	if err != nil && strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("nil-LookupEnv error leaked sentinel: %v", err)
	}

	// 4. EnvResolver with nil ref => error must not echo sentinel.
	_, err = r.Resolve(context.Background(), nil, "")
	if err != nil && strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("nil-ref error leaked sentinel: %v", err)
	}

	// 5. Grant validation failures: no-ref, no-scope, nil-grant must not
	//    echo sentinel.
	if err := (*secrets.Grant)(nil).Validate(); err != nil && strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("nil-grant Validate leaked sentinel: %v", err)
	}
	if err := (&secrets.Grant{Scope: "global"}).Validate(); err != nil && strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("no-ref Validate leaked sentinel: %v", err)
	}
	if err := (&secrets.Grant{Ref: mustRef(t, "secret://vault/db")}).Validate(); err != nil && strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("no-scope Validate leaked sentinel: %v", err)
	}

	// 6. Grant.Redeem: expired + scope-mismatch + nil-resolver all must
	//    not echo sentinel.
	expiredGrant := &secrets.Grant{
		Ref: mustRef(t, "secret://vault/db-pass"), Scope: "global", WorkID: "work:a",
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	_, err = expiredGrant.Redeem(context.Background(), &fakeResolver{value: fakeValue}, "work:a")
	if err == nil {
		t.Fatal("Redeem(expired) succeeded")
	}
	if !errors.Is(err, secrets.ErrGrantExpired) {
		t.Fatalf("Redeem(expired) err = %v, want ErrGrantExpired", err)
	}
	if strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("Redeem(expired) leaked sentinel: %v", err)
	}

	mismatchGrant := &secrets.Grant{
		Ref: mustRef(t, "secret://vault/db-pass"), Scope: "global", WorkID: "work:a",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	_, err = mismatchGrant.Redeem(context.Background(), &fakeResolver{value: fakeValue}, "work:b")
	if err == nil {
		t.Fatal("Redeem(scope mismatch) succeeded")
	}
	if !errors.Is(err, secrets.ErrGrantScope) {
		t.Fatalf("Redeem(scope) err = %v, want ErrGrantScope", err)
	}
	if strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("Redeem(scope) leaked sentinel: %v", err)
	}

	// 7. nil resolver => error must not echo sentinel.
	g := &secrets.Grant{
		Ref: mustRef(t, "secret://vault/db-pass"), Scope: "global",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	_, err = g.Redeem(context.Background(), nil, "")
	if err != nil && strings.Contains(err.Error(), fakeValue) {
		t.Fatalf("Redeem(nil resolver) leaked sentinel: %v", err)
	}

	// 8. RedactStrings with the fake value as a key payload should NEVER
	//    round-trip the value back out.
	out := secrets.RedactStrings(map[string]string{"k": fakeValue}, []string{fakeValue})
	for _, v := range out {
		if strings.Contains(v, fakeValue) {
			t.Fatalf("RedactStrings leaked sentinel: %q", v)
		}
	}

	// 9. Belt-and-braces: sweep every error path's .Error() for the
	//    sentinel across a constructed map of all failure messages.
	_, gNilResErr := g.Redeem(context.Background(), nil, "")
	allErrors := map[string]error{
		"ParseRef empty":          mustErr(secrets.ParseRef("")),
		"ParseRef bad scheme":     mustErr(secrets.ParseRef("http://x")),
		"ParseRef trailing slash": mustErr(secrets.ParseRef("secret://vault/")),
		"EnvResolver missing":     errMissingFromRealEnv(),
		"EnvResolver nil lookup":  errNilLookup(),
		"Grant nil Validate":      (*secrets.Grant)(nil).Validate(),
		"Grant no ref Validate":   (&secrets.Grant{Scope: "global"}).Validate(),
		"Grant no scope Validate": (&secrets.Grant{Ref: mustRef(t, "secret://vault/db")}).Validate(),
		"Redeem nil resolver":     gNilResErr,
	}
	for label, e := range allErrors {
		if e == nil {
			continue
		}
		if strings.Contains(e.Error(), fakeValue) {
			t.Fatalf("[%s] error leaked sentinel %q: %v", label, fakeValue, e)
		}
	}
}

func mustErr(_ *secrets.Ref, err error) error {
	return err
}

func errMissingFromRealEnv() error {
	r := secrets.NewEnvResolver()
	_, err := r.Resolve(context.Background(), mustRefForTest("secret://vault/__never_present__"), "")
	return err
}

func errNilLookup() error {
	_, err := (&secrets.EnvResolver{}).Resolve(context.Background(), mustRefForTest("secret://vault/x"), "")
	return err
}

func mustRefForTest(s string) *secrets.Ref {
	r, err := secrets.ParseRef(s)
	if err != nil {
		panic(err)
	}
	return r
}
