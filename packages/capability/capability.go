// Package capability implements the capability-evaluation law that binds
// the frozen shell.contracts/1.0 surface law to policy.token/1.0 grants
// (ADR-0017 + ADR-0025): a command is executable only when the surface
// contract allows it AND the caller's policy token carries a matching scope.
//
// Freeze laws encoded:
//   - shell.contracts/1.0: pulse+local_only can never carry privileged
//     commands; pulse+T3 lives only on COMMAND (validated via packages/shell)
//   - policy.token/1.0: every grant carries token_id, work_id, org, unique
//     scopes, purpose_bindings, budget_line, expiry — all required
//   - identity/1.0: service principals never approve (structural, not advisory)
//   - fail-closed: no token, expired token, or missing scope => denied
package capability

import (
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/shell"
)

// Token is the typed policy.token/1.0 grant. BudgetLine stays opaque
// (works-kernel owns kernel.budget/1.0); this package only requires its
// presence — the budget law is enforced by the ledger, not by capabilities.
type Token struct {
	TokenID         string         `json:"token_id"`
	WorkID          string         `json:"work_id"`
	Org             string         `json:"org"`
	Scopes          []string       `json:"scopes"`
	PurposeBindings []string       `json:"purpose_bindings"`
	BudgetLine      map[string]any `json:"budget_line"`
	Expiry          string         `json:"expiry"`
	DelegatedFrom   string         `json:"delegated_from,omitempty"`
}

// Validate enforces the frozen required-field law of policy.token/1.0.
func (t *Token) Validate() error {
	if t == nil {
		return errors.New("capability token is required")
	}
	if t.TokenID == "" {
		return errors.New("policy.token.token_id is required")
	}
	if t.WorkID == "" {
		return errors.New("policy.token.work_id is required")
	}
	if t.Org == "" {
		return errors.New("policy.token.org is required")
	}
	if len(t.Scopes) == 0 {
		return errors.New("policy.token.scopes must be non-empty")
	}
	if err := uniqueScopes(t.Scopes); err != nil {
		return err
	}
	if len(t.PurposeBindings) == 0 {
		return errors.New("policy.token.purpose_bindings must be non-empty")
	}
	if t.BudgetLine == nil {
		return errors.New("policy.token.budget_line is required")
	}
	if t.Expiry == "" {
		return errors.New("policy.token.expiry is required")
	}
	return nil
}

func uniqueScopes(scopes []string) error {
	seen := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		if seen[s] {
			return fmt.Errorf("policy.token.scopes must be unique: %q duplicated", s)
		}
		seen[s] = true
	}
	return nil
}

// Decision is the outcome of evaluating one command against one surface
// contract and one token. It is evidence-grade: every denial carries the
// exact frozen law that produced it.
type Decision struct {
	Allowed       bool     `json:"allowed"`
	Command       string   `json:"command"`
	Reason        string   `json:"reason"`
	Law           string   `json:"law"` // frozen contract violated or satisfied
	TokenID       string   `json:"token_id,omitempty"`
	Expired       bool     `json:"expired,omitempty"`
	MissingScopes []string `json:"missing_scopes,omitempty"`
}

// Evaluate decides whether *command* may execute on *surface* (system/tier
// per the contract) under *token*, at the given time (RFC3339 expiry check).
//
// Evaluation order is structural, cheapest-fail-first:
//  1. surface contract itself must be valid (shell.contracts/1.0 law)
//  2. command must be in the surface contract's command list (the surface
//     must actually expose what it is being asked to do)
//  3. token must validate (policy.token/1.0 required fields)
//  4. token must not be expired
//  5. token scopes must cover the command ("cmd:<command>" scope form)
//  6. privileged commands from a service principal are always denied
//     (identity/1.0: service_principals_never_approve)
func Evaluate(
	command string,
	surface *shell.SurfaceContract,
	token *Token,
	now string,
) *Decision {
	const lawShell = "contract:shell.contracts/1.0"
	const lawToken = "contract:policy.token/1.0"
	const lawIdentity = "contract:identity/1.0"

	deny := func(law, reason string) *Decision {
		d := &Decision{Allowed: false, Command: command, Reason: reason, Law: law}
		if token != nil {
			d.TokenID = token.TokenID
		}
		return d
	}

	if command == "" {
		return deny(lawShell, "empty command")
	}
	if surface == nil {
		return deny(lawShell, "surface contract is required")
	}
	if err := surface.Validate(); err != nil {
		return deny(lawShell, "surface contract invalid: "+err.Error())
	}

	exposed := false
	for _, c := range surface.Commands {
		if c == command {
			exposed = true
			break
		}
	}
	if !exposed {
		return deny(lawShell, fmt.Sprintf("surface %s does not expose command %q", surface.Surface, command))
	}

	if err := token.Validate(); err != nil {
		return deny(lawToken, "token invalid: "+err.Error())
	}
	if expired(token.Expiry, now) {
		return &Decision{
			Allowed: false, Command: command, Reason: "token expired",
			Law: lawToken, TokenID: token.TokenID, Expired: true,
		}
	}

	scope := "cmd:" + command
	missing := missingScope(token.Scopes, scope)
	if len(missing) > 0 {
		return &Decision{
			Allowed: false, Command: command,
			Reason: "token scopes do not cover command",
			Law:    lawToken, TokenID: token.TokenID,
			MissingScopes: []string{scope},
		}
	}

	// identity/1.0 law: privileged commands are human-only. A service
	// principal can hold scopes, but approve/deny/kill/take/hand_back are
	// never executable by a non-human principal.
	switch command {
	case "approve", "deny", "kill", "take", "hand_back":
		if isServicePrincipal(token) {
			return deny(lawIdentity, "service principals never approve (identity/1.0 privilege_note)")
		}
	}

	return &Decision{
		Allowed: true, Command: command,
		Reason: "surface exposes command and token scopes cover it",
		Law:    lawToken, TokenID: token.TokenID,
	}
}

// isServicePrincipal reports whether the token carries a structural
// service-principal binding. The frozen identity contract expresses this as
// privilege_note = "service_principals_never_approve"; tokens minted for
// service principals carry the marker in their purpose bindings by mint law.
func isServicePrincipal(t *Token) bool {
	for _, p := range t.PurposeBindings {
		if p == "service_principal" {
			return true
		}
	}
	return false
}

func missingScope(scopes []string, want string) []string {
	for _, s := range scopes {
		if s == want {
			return nil
		}
	}
	return []string{want}
}

// expired compares RFC3339 expiry to RFC3339 now. Unparseable expiry fails
// closed (treated as expired — an unverifiable token is not a valid one).
func expired(expiry, now string) bool {
	e, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return true
	}
	if now == "" {
		return time.Now().UTC().After(e)
	}
	n, err := time.Parse(time.RFC3339, now)
	if err != nil {
		return true
	}
	return n.After(e)
}
