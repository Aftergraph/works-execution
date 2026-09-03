// Package runner implements the Runner Identity standard (k-impl-002).
//
// Each works-worker mints or restores a Runner identity record at startup.
// The record follows docs/standards/schemas/runner-identity.schema.json and
// carries a SPIFFE ID of the form:
//
//	spiffe://works-execution/ns/<tenant>/sa/<service-account>
//
// This package is the canonical mint/validate/build layer. The HTTP layer
// (services/api/runner_register.go) and the CLI (cmd/works-runner-id) both
// use it; the registry is the single source of truth for identity shape.
package runner

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// TrustClass is the trust tier advertised by a runner. Matches the enum in
// runner-identity.schema.json.
type TrustClass string

const (
	TrustUntrusted  TrustClass = "untrusted"
	TrustStandard   TrustClass = "standard"
	TrustPrivileged TrustClass = "privileged"
)

// LifecycleState mirrors the schema enum.
type LifecycleState string

const (
	StatePending  LifecycleState = "pending"
	StateActive   LifecycleState = "active"
	StateDraining LifecycleState = "draining"
	StateRetired  LifecycleState = "retired"
)

// Capabilities mirrors the capabilities sub-object in the schema. Only the
// fields we actually advertise are typed; unknown fields are preserved by
// BuildIdentity callers via the Labels map.
type Capabilities struct {
	OS         []string `json:"os"`
	Arch       []string `json:"arch"`
	CPUMilli   int      `json:"cpu_milli,omitempty"`
	MemoryMiB  int      `json:"memory_mib,omitempty"`
	GPU        int      `json:"gpu,omitempty"`
	Toolchains []string `json:"toolchains,omitempty"`
	Labels     []string `json:"labels,omitempty"`
}

// Identity is the in-memory shape of a Runner Identity Record. JSON tags
// match the schema property names exactly.
type Identity struct {
	RunnerID        string         `json:"runner_id"`
	SpiffeID        string         `json:"spiffe_id,omitempty"`
	TrustClass      TrustClass     `json:"trust_class"`
	Capabilities    Capabilities   `json:"capabilities"`
	LifecycleState  LifecycleState `json:"lifecycle_state"`
	EnrolledAt      time.Time      `json:"enrolled_at"`
	LastHeartbeatAt *time.Time     `json:"last_heartbeat_at,omitempty"`
}

// SPIFFE trust domain. Anything else is rejected by ValidateSPIFFE.
const trustDomain = "works-execution"

// Patterns copied verbatim from runner-identity.schema.json. Any change here
// must also be reflected in the schema (and vice versa).
var (
	// RunnerIDPattern is the character+length contract for every runner id
	// the registry accepts. Exported so the API enrollment layer can enforce
	// the SAME charset on /v1/workers/enroll — closing the k-064 finding B
	// gap where a verified identity could hold a shape the registry rejects,
	// stranding it in k-058's legacy-pass class.
	RunnerIDPatternSource = `^wrkr_[a-z0-9_-]{1,64}$`
	RunnerIDPattern       = regexp.MustCompile(RunnerIDPatternSource)

	runnerIDPattern = RunnerIDPattern
	spiffePattern   = regexp.MustCompile(`^spiffe://[a-z][a-z0-9.-]*/ns/[a-z0-9-]+/sa/[a-z0-9_-]+$`)
)

// MintRunnerID returns a fresh runner_id matching `^wrkr_[a-z0-9_-]{1,64}$`.
// The ID encodes an 8-byte random suffix in hex (16 chars) prefixed with
// "wrkr_". Cryptographic randomness is used because the ID is part of the
// SPIFFE trust chain and must not be guessable.
func MintRunnerID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read failing on a working OS is effectively impossible; fall
		// back to a time-based suffix so we still produce a valid id rather
		// than panicking on the worker startup path.
		ts := time.Now().UTC().UnixNano()
		return fmt.Sprintf("wrkr_%016x", ts)
	}
	return "wrkr_" + hex.EncodeToString(b[:])
}

// ValidateSPIFFE returns nil if s is a syntactically valid SPIFFE ID under
// the works-execution trust domain with ns/<tenant>/sa/<id> structure.
// The shape matches the schema regex; ValidateSPIFFE additionally enforces
// the trust domain so callers can't smuggle IDs minted by an unrelated
// authority.
func ValidateSPIFFE(s string) error {
	if s == "" {
		return fmt.Errorf("spiffe: empty id")
	}
	if !spiffePattern.MatchString(s) {
		return fmt.Errorf("spiffe: id %q does not match pattern spiffe://<trust-domain>/ns/<tenant>/sa/<sa>", s)
	}
	// Strip the "spiffe://" prefix and split off the trust domain.
	rest := strings.TrimPrefix(s, "spiffe://")
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return fmt.Errorf("spiffe: id %q missing path", s)
	}
	domain := rest[:slash]
	if domain != trustDomain {
		return fmt.Errorf("spiffe: trust domain %q != %q", domain, trustDomain)
	}
	return nil
}

// BuildSPIFFE constructs a SPIFFE ID from the given tenant and service-account.
// Both are normalised to lower-case. The result is not validated; call
// ValidateSPIFFE on it before persisting.
func BuildSPIFFE(tenant, sa string) string {
	return fmt.Sprintf("spiffe://%s/ns/%s/sa/%s",
		trustDomain,
		strings.ToLower(tenant),
		strings.ToLower(sa),
	)
}

// BuildIdentity produces a fully-populated Identity ready to be marshalled
// to JSON and POSTed to the API. The returned struct matches the schema
// required fields; optional fields are left at their zero values.
//
// `tenant` becomes the SPIFFE namespace and MUST be non-empty; the runner_id
// MUST already match the schema pattern (use MintRunnerID otherwise).
func BuildIdentity(runnerID, tenant string, caps Capabilities) (*Identity, error) {
	if !runnerIDPattern.MatchString(runnerID) {
		return nil, fmt.Errorf("identity: runner_id %q does not match schema pattern", runnerID)
	}
	if tenant == "" {
		return nil, fmt.Errorf("identity: tenant required")
	}
	if len(caps.OS) == 0 || len(caps.Arch) == 0 {
		return nil, fmt.Errorf("identity: capabilities.os and capabilities.arch are required")
	}
	// sa defaults to runner_id for single-tenant workers.
	id := &Identity{
		RunnerID:       runnerID,
		SpiffeID:       BuildSPIFFE(tenant, runnerID),
		TrustClass:     TrustStandard,
		Capabilities:   caps,
		LifecycleState: StatePending,
		EnrolledAt:     time.Now().UTC(),
	}
	if err := ValidateSPIFFE(id.SpiffeID); err != nil {
		return nil, fmt.Errorf("identity: built invalid SPIFFE ID: %w", err)
	}
	return id, nil
}

// Validate runs schema-shaped checks against an existing Identity. It is
// used by the API handler before storing and by tests.
func (id *Identity) Validate() error {
	if !runnerIDPattern.MatchString(id.RunnerID) {
		return fmt.Errorf("identity: runner_id %q invalid", id.RunnerID)
	}
	if id.SpiffeID != "" {
		if err := ValidateSPIFFE(id.SpiffeID); err != nil {
			return err
		}
	}
	switch id.TrustClass {
	case TrustUntrusted, TrustStandard, TrustPrivileged:
	default:
		return fmt.Errorf("identity: trust_class %q invalid", id.TrustClass)
	}
	switch id.LifecycleState {
	case StatePending, StateActive, StateDraining, StateRetired:
	default:
		return fmt.Errorf("identity: lifecycle_state %q invalid", id.LifecycleState)
	}
	if id.EnrolledAt.IsZero() {
		return fmt.Errorf("identity: enrolled_at required")
	}
	if len(id.Capabilities.OS) == 0 || len(id.Capabilities.Arch) == 0 {
		return fmt.Errorf("identity: capabilities.os and capabilities.arch required")
	}
	return nil
}
