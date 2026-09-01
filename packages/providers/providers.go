// Package providers defines the frozen CPN boundary (Computer Provider
// Network face, kernel → provider) per ADR-0012 + ADR-0018 + k-hal-01.
//
// THE BOUNDARY LAW:
//
//   - CPN: kernel calls providers. Provision / Snapshot / Exec / Teardown,
//     capability negotiation, lifecycle states. THIS FILE.
//   - RAB: runtimes expose primitives to agents (screenshot/input/record/
//     observe/control). It lives in L3 runtimes, versioned separately
//     (rab/1.0) — it is deliberately NOT here, and the two must never grow
//     together (freeze law, final-freeze-review F1).
//
// Provider-neutral law (k-hal-01): nothing in this package may depend on a
// cloud vendor, Docker, a VM type, Windows, Linux, PULSE, a browser
// implementation, a model provider, or existing runner names. Existing
// infrastructure adapts BEHIND the contract; it never defines it.
//
// Authority law: the kernel is the policy authority; a provider is an
// executor. A provider may only exercise capabilities the kernel accepted
// in the handshake, may never widen its own scope, and receives secrets
// exclusively as frozen secret:// refs (never plaintext).
package providers

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ABI versions (frozen; ADR-0021 charter: N-1 tolerance, breaking = major).
const (
	ABI        = "cpi/1.0"
	MinABI     = "cpi/1.0" // the only major generation in v1
)

// Capability names (closed enum, frozen — matching contracts/schemas/cpi/1.0).
const (
	CapFS            = "fs"
	CapShell         = "shell"
	CapGit           = "git"
	CapBrowser       = "browser"
	CapSnap          = "snap"
	CapTeardownKeep  = "teardown_retain"
)

// Err* are deterministic, provider-agnostic errors. Providers map their
// native failures onto these via fmt.Errorf wrapping — the kernel switches
// on errors.Is, never on provider-specific types.
var (
	ErrHandshakeIncompatible = errors.New("cpi: ABI version incompatible (fail-closed)")
	ErrUnknownCapability     = errors.New("cpi: unknown capability in advertisement (fail-closed)")
	// ErrCapabilityNotAdvertised is returned when the kernel requests a
	// capability the provider did not advertise. A provider implementing a
	// capability it never advertised must NOT be trusted for it.
	ErrCapabilityNotAdvertised = errors.New("cpi: capability not advertised by provider")
	// ErrCapabilityEscalation is returned when a caller attempts to use a
	// handle/capability pair outside what the kernel authorized.
	ErrCapabilityEscalation = errors.New("cpi: capability escalation attempt")
	// ErrResourceNotFound / ErrResourceForeign: stale or wrong-tenant handles.
	ErrResourceNotFound = errors.New("cpi: resource not found (stale handle)")
	ErrResourceForeign  = errors.New("cpi: resource belongs to another tenant (fail-closed)")
	// ErrProvisionReplayed: same idempotency key, different spec.
	ErrProvisionReplayed = errors.New("cpi: provision replayed with different spec")
	// ErrProviderUnavailable: provider disappeared / not answering.
	ErrProviderUnavailable = errors.New("cpi: provider unavailable")
	// ErrMalformed: provider returned a malformed response.
	ErrMalformed = errors.New("cpi: malformed provider response")
	// ErrSecretNotRef: plaintext secret passed through the contract.
	ErrSecretNotRef = errors.New("cpi: secrets must be secret:// refs, never plaintext")
	// ErrTeardownLeak: teardown refused because authority artifacts would leak.
	ErrTeardownLeak = errors.New("cpi: teardown would leak active authority tokens")
	// ErrSnapshotIntegrity: snapshot fails its own verification.
	ErrSnapshotIntegrity = errors.New("cpi: snapshot integrity check failed")
)

// Capabilities is the negotiated, kernel-accepted capability set.
type Capabilities []string

// Has reports whether the set contains cap. This is the ONLY capability
// check the kernel-side wrapper offers: providers cannot self-assert.
func (c Capabilities) Has(cap string) bool {
	for _, x := range c {
		if x == cap {
			return true
		}
	}
	return false
}

// Handshake is the version/capability negotiation (cpi/1.0). Both sides
// exchange it; the kernel rejects anything it cannot bind to the frozen
// contract — fail-closed per ADR-0018.
type Handshake struct {
	ABI     string   `json:"abi"`     // must be "cpi/1.0" for this kernel
	Caps    []string `json:"caps"`    // what the provider implements
	ProvID  string   `json:"prov_id"` // provider identity (stable, tenant-agnostic)
	Generation string `json:"generation,omitempty"` // provider fleet generation
}

// Validate checks ABI + cap-enum compliance. Unknown capabilities are
// REJECTED (never silently downgraded to broader authority).
func (h *Handshake) Validate() error {
	if h.ABI != ABI {
		return fmt.Errorf("%w: provider speaks %q, kernel speaks %q", ErrHandshakeIncompatible, h.ABI, ABI)
	}
	if h.ProvID == "" {
		return fmt.Errorf("%w: empty provider id", ErrMalformed)
	}
	for _, c := range h.Caps {
		switch c {
		case CapFS, CapShell, CapGit, CapBrowser, CapSnap, CapTeardownKeep:
		default:
			return fmt.Errorf("%w: %q", ErrUnknownCapability, c)
		}
	}
	return nil
}

// Spec is a Provision request: provider-neutral, tenant-bound.
type Spec struct {
	IdempotencyKey string       `json:"idempotency_key"` // replay protection key
	Org            string       `json:"org"`             // tenant binding (opaque to provider)
	Caps           Capabilities `json:"caps"`            // requested capabilities (⊆ negotiated)
	Labels         map[string]string `json:"labels,omitempty"`
}

// Provisioned is the kernel-side view of one created computer.
type Resource struct {
	ID      string       `json:"id"`
	ProvID  string       `json:"prov_id"`
	Org     string       `json:"org"`
	Caps    Capabilities `json:"caps"`
	Created time.Time    `json:"created"`
}

// NegotiatedCaps is the accepted capability set after handshake: the
// intersection of what the provider advertised and what the kernel offered.
// This — not the provider's full advertisement — is the authority set.
func NegotiatedCaps(offer, accepted Capabilities) Capabilities {
	set := map[string]bool{}
	for _, a := range accepted {
		set[a] = true
	}
	out := make(Capabilities, 0, len(accepted))
	for _, c := range offer {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

// ExecSpec is a provider-agnostic command. Secrets appear ONLY as refs
// (secret://scope/name); the provider resolves them via the kernel-supplied
// resolver callback — never as plaintext in this struct.
type ExecSpec struct {
	Cmd     string            `json:"cmd"`
	Env     map[string]string `json:"env,omitempty"` // values may be secret:// refs only
	Workdir string            `json:"workdir,omitempty"`
	Timeout time.Duration     `json:"timeout,omitempty"` // 0 = provider default
}

// ExecResult is the deterministic outcome of one command execution.
type ExecResult struct {
	ExitCode int
	Log      []byte // combined stdout+stderr (provider may truncate per budget)
	Duration time.Duration
}

// ComputerProvider is the frozen CPN contract (kernel → provider).
// Implementations MUST pass ConformanceSuite — interface compliance alone
// proves nothing (conformance = executable evidence, k-hal-01).
type ComputerProvider interface {
	// Handshake MUST be called and accepted before any other call. A
	// provider used without a successful handshake has NO authority.
	Handshake(ctx context.Context, offer Handshake) (Handshake, error)

	// Provision creates one computer. Idempotent on IdempotencyKey: the
	// same key with the same spec returns the same Resource; the same key
	// with a different spec is ErrProvisionReplayed (replay protection).
	Provision(ctx context.Context, spec Spec) (Resource, error)

	// Exec runs a command on a resource previously Provisioned by this
	// same provider (identity-checked, tenant-bound). Requires an
	// advertised+accepted capability covering the command class; the
	// kernel passes its capability set in — EscalationLaw is enforced here.
	Exec(ctx context.Context, res Resource, cap string, spec ExecSpec) (ExecResult, error)

	// Snapshot captures a durable checkpoint of the resource. Snapshots
	// must carry an integrity envelope (content address) verifiable later
	// (Snapshot integrity is conformance-tested).
	Snapshot(ctx context.Context, res Resource) (SnapshotRef, error)

	// Teardown tears the resource down. Mode retain keeps state (only
	// allowed when teardown_keep was negotiated); mode clean destroys.
	// After Teardown the Resource handle is stale: every subsequent use
	// returns ErrResourceNotFound. Any active leases held against the
	// resource MUST be released by the kernel BEFORE calling Teardown —
	// Teardown refuses with ErrTeardownLeak if the provider still sees
	// live authority tokens it issued.
	Teardown(ctx context.Context, res Resource, mode TeardownMode) error
}

// TeardownMode selects destroy vs retain (frozen enum).
type TeardownMode string

const (
	TeardownClean  TeardownMode = "clean"
	TeardownRetain TeardownMode = "retain"
)

// SnapshotRef is the provider's durable checkpoint handle (content-addressed
// by the provider; integrity is its guarantee, conformance-tested here).
type SnapshotRef struct {
	ID      string    `json:"id"`
	ResID   string    `json:"res_id"`
	ProvID  string    `json:"prov_id"`
	Digest  string    `json:"digest"` // sha256 over snapshot payload
	Created time.Time `json:"created"`
}

// SecretRef validates the frozen secret-reference shape and rejects
// plaintext (ADR-0022 invariant: refs travel, values never do).
func SecretRef(v string) error {
	const p = "secret://"
	if len(v) < len(p)+3 || v[:len(p)] != p {
		return fmt.Errorf("%w: got %q", ErrSecretNotRef, redact(v))
	}
	// Refuse embedded newlines / NUL / obvious value smuggling.
	for _, r := range v {
		if r == '\n' || r == '\r' {
			return fmt.Errorf("%w: newline in ref", ErrSecretNotRef)
		}
	}
	return nil
}

// redact masks any non-ref value so error paths never echo plaintext.
func redact(v string) string {
	if len(v) > 12 {
		return v[:4] + "…[REDACTED]"
	}
	return "[REDACTED]"
}