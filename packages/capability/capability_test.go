// k-cap-01 tests — policy.token/1.0 + identity/1.0 + shell.contracts/1.0.
//
// Freeze law under test:
//   - fail-closed: no token / invalid token / expired token / missing scope
//   - surface must actually expose the evaluated command
//   - surface contract itself must satisfy shell.contracts/1.0 conditional law
//   - service principals never execute privileged commands (identity/1.0)
//   - token required-fields law (unique scopes, budget_line present, expiry)
package capability_test

import (
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/capability"
	"github.com/JonasAbde/works-execution/packages/shell"
)

const NOW = "2026-09-01T12:00:00Z"

func worksCommandSurface() *shell.SurfaceContract {
	return &shell.SurfaceContract{
		Surface:  shell.SurfaceCOMMAND,
		System:   shell.SystemWorks,
		Tier:     shell.TierT3Privilege,
		Renders:  []string{"command palette"},
		Commands: []string{"approve", "deny", "kill", "run", "watch"},
		Executor: "works_kernel",
	}
}

func token() *capability.Token {
	return &capability.Token{
		TokenID:         "tok-1",
		WorkID:          "work:" + strings.Repeat("a", 32),
		Org:             "org_01234567",
		Scopes:          []string{"cmd:run", "cmd:watch"},
		PurposeBindings: []string{"mission execution"},
		BudgetLine:      map[string]any{"compute_eur": 5.0},
		Expiry:          "2026-12-01T00:00:00Z",
	}
}

func TestAllowedWhenScopeCoversCommand(t *testing.T) {
	d := capability.Evaluate("run", worksCommandSurface(), token(), NOW)
	if !d.Allowed {
		t.Fatalf("run should be allowed, got: %s (%s)", d.Reason, d.Law)
	}
}

func TestNoTokenFailsClosed(t *testing.T) {
	d := capability.Evaluate("run", worksCommandSurface(), nil, NOW)
	if d.Allowed {
		t.Fatal("command allowed without token — fail-closed broken")
	}
	if d.Law != "contract:policy.token/1.0" {
		t.Fatalf("denial law = %s, want policy.token", d.Law)
	}
}

func TestExpiredTokenFailsClosed(t *testing.T) {
	tk := token()
	tk.Expiry = "2026-01-01T00:00:00Z"
	d := capability.Evaluate("run", worksCommandSurface(), tk, NOW)
	if d.Allowed || !d.Expired {
		t.Fatal("expired token accepted")
	}
}

func TestUnparseableExpiryFailsClosed(t *testing.T) {
	tk := token()
	tk.Expiry = "soon"
	d := capability.Evaluate("run", worksCommandSurface(), tk, NOW)
	if d.Allowed {
		t.Fatal("unparseable expiry treated as valid")
	}
}

func TestMissingScopeDeniedWithEvidence(t *testing.T) {
	tk := token()
	tk.Scopes = []string{"cmd:watch"}
	d := capability.Evaluate("run", worksCommandSurface(), tk, NOW)
	if d.Allowed {
		t.Fatal("missing scope allowed")
	}
	if len(d.MissingScopes) != 1 || d.MissingScopes[0] != "cmd:run" {
		t.Fatalf("missing_scopes evidence wrong: %v", d.MissingScopes)
	}
}

func TestSurfaceMustExposeCommand(t *testing.T) {
	// "mount" is a valid frozen command but not on the COMMAND surface's list
	d := capability.Evaluate("mount", worksCommandSurface(), token(), NOW)
	if d.Allowed {
		t.Fatal("command not on surface accepted")
	}
	if !strings.Contains(d.Reason, "does not expose") {
		t.Fatalf("reason should name the exposure law, got: %s", d.Reason)
	}
	if d.Law != "contract:shell.contracts/1.0" {
		t.Fatalf("law = %s, want shell.contracts/1.0", d.Law)
	}
}

func TestInvalidSurfaceContractDenied(t *testing.T) {
	// pulse+local_only surface carrying kill is invalid per shell.contracts/1.0
	bad := &shell.SurfaceContract{
		Surface:  shell.SurfaceNOW,
		System:   shell.SystemPulse,
		Tier:     shell.TierLocalOnly,
		Renders:  []string{"x"},
		Commands: []string{"kill"},
	}
	d := capability.Evaluate("kill", bad, token(), NOW)
	if d.Allowed {
		t.Fatal("invalid surface contract accepted")
	}
	if d.Law != "contract:shell.contracts/1.0" {
		t.Fatalf("law = %s, want shell.contracts/1.0", d.Law)
	}
}

func TestServicePrincipalNeverApproves(t *testing.T) {
	tk := token()
	tk.Scopes = []string{"cmd:approve", "cmd:deny", "cmd:kill"}
	tk.PurposeBindings = []string{"service_principal"}
	for _, cmd := range []string{"approve", "deny", "kill"} {
		d := capability.Evaluate(cmd, worksCommandSurface(), tk, NOW)
		if d.Allowed {
			t.Fatalf("service principal executed privileged command %q", cmd)
		}
		if d.Law != "contract:identity/1.0" {
			t.Fatalf("%s: law = %s, want identity/1.0", cmd, d.Law)
		}
	}
}

func TestHumanPrincipalCanApproveWithScope(t *testing.T) {
	tk := token()
	tk.Scopes = []string{"cmd:approve"}
	d := capability.Evaluate("approve", worksCommandSurface(), tk, NOW)
	if !d.Allowed {
		t.Fatalf("human with approve scope denied: %s", d.Reason)
	}
}

func TestTokenRequiredFieldsLaw(t *testing.T) {
	tk := token()
	tk.Scopes = []string{"cmd:x", "cmd:x"} // duplicate scopes
	if err := tk.Validate(); err == nil {
		t.Fatal("duplicate scopes accepted — policy.token/1.0 uniqueItems broken")
	}
	tk2 := token()
	tk2.BudgetLine = nil
	if err := tk2.Validate(); err == nil {
		t.Fatal("missing budget_line accepted — policy.token/1.0 required broken")
	}
	tk3 := token()
	tk3.PurposeBindings = nil
	if err := tk3.Validate(); err == nil {
		t.Fatal("missing purpose_bindings accepted")
	}
}

func TestPulseLocalOnlySurfaceBlocksPrivilegedEvenWithScope(t *testing.T) {
	// A pulse local_only surface is structurally incapable of exposing the
	// command, so evaluation must deny at the surface law — even if a token
	// somehow carried the scope.
	tk := token()
	tk.Scopes = []string{"cmd:kill"}
	local := &shell.SurfaceContract{
		Surface:  shell.SurfaceNOW,
		System:   shell.SystemPulse,
		Tier:     shell.TierLocalOnly,
		Renders:  []string{"status"},
		Commands: []string{"watch"},
	}
	d := capability.Evaluate("kill", local, tk, NOW)
	if d.Allowed {
		t.Fatal("privileged command allowed on pulse local_only surface")
	}
	if d.Law != "contract:shell.contracts/1.0" {
		t.Fatalf("law = %s, want shell.contracts/1.0", d.Law)
	}
}
