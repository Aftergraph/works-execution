// Package release implements the promotion law over the frozen
// release.rings/1.0 contract (ADR-0013, k-044): a release walks the ring
// ladder internal -> alpha -> beta -> stable one ring at a time, soaked,
// kill-switched, and — for stable — freeze-attested.
//
// Laws encoded (the schema pins the shape; this package is the teeth):
//   - L1 (ring enum law): Ring must be one of the frozen rings enum
//     (internal, alpha, beta, stable). Anything else fails closed.
//   - L2 (version law): Version is required and must match the semver-ish
//     pattern ^v?[0-9]+\.[0-9]+\.[0-9]+$ — full major.minor.patch, no
//     uppercase, no slashes, no shell- or path-ish characters. The version
//     flows into git tag commands downstream; anchoring here is defense in
//     depth, not the transport's job.
//   - L3 (kill-switch law): a release that cannot be stopped cannot ship.
//     KillSwitch must be non-empty for every ring except internal.
//   - L4 (no-ring-skips law): no_ring_skips is a const (true) in the frozen
//     schema, so the enforcement lives here — Advance accepts only a single
//     forward step in RingOrder. Skips error ErrRingSkipped; backwards
//     moves error ErrRingBackwards (downgrades go through Revert, where the
//     reason is mandatory and the event is logged); same-ring no-ops error
//     ErrAlreadyInRing.
//   - L5 (beta soak law): beta_soak_hours is a const (48) in the frozen
//     schema. The soak is time spent in BETA before promotion to STABLE —
//     per the contract's own name, beta_soak_hours — so alpha -> beta has no
//     soak law. A missing beta entry stamp counts as zero soak (fail
//     closed); a negative clock delta counts as zero too.
//   - L6 (freeze law): stable promotion requires freeze evidence — the
//     contracts manifest SHA-256 attestation present and contract tests
//     green. This repo's standing rule: frozen contracts are regression
//     law (see docs/RELEASE-v0.2.0.md). Missing or malformed attestation
//     errors ErrFreezeMissing; red contract tests error ErrContractsRed;
//     no evidence at all fails closed with ErrFreezeEvidenceRequired.
//   - L7 (audit-lineage law): Advance and Revert are pure value transforms
//     — they never mutate the receiver, they return a new Release with a
//     copied SoakStartedAt map, KillSwitch slice, and RevertLog. A revert
//     is an auditable event, not a silent move.
package release

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Rings (release.rings/1.0 frozen enum).
const (
	RingInternal = "internal"
	RingAlpha    = "alpha"
	RingBeta     = "beta"
	RingStable   = "stable"
)

// RingOrder is the promotion ladder. Advance moves exactly one step
// forward along this slice; Revert moves backwards along it.
var RingOrder = []string{RingInternal, RingAlpha, RingBeta, RingStable}

// BetaSoakHours quotes the frozen schema const:
//
//	"beta_soak_hours": { "const": 48 }   // contract:release.rings/1.0
//
// It is the minimum dwell time in the beta ring before promotion to
// stable. It is a const in the schema, so changing it is a contract break
// (major version bump per proto.charter/1.0), not a config value.
const BetaSoakHours = 48

// versionPattern is the L2 version law: optional leading "v", then three
// dot-separated numeric segments (patch required). Anchored on both ends —
// Go's $ is end-of-text (no trailing-newline loophole) — because the
// version later flows into git tag commands.
var versionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// sha256Hex matches the freeze attestation format (lowercase sha256 hex,
// 64 chars — contracts/manifest.sha256).
var sha256Hex = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Release is one release object moving through the rings. SoakStartedAt
// maps a ring to the moment this release entered that ring; the soak law
// (L5) reads it for the beta ring. Now is injectable for deterministic
// tests (default time.Now) and consulted whenever Advance/Revert are
// handed the zero time.Time.
type Release struct {
	Version    string   `json:"version"`
	Ring       string   `json:"ring"`
	KillSwitch []string `json:"kill_switch,omitempty"`
	// SoakStartedAt records ring -> entry time (release.rings/1.0 soak
	// bookkeeping; the schema pins the dwell law, this map pins the clock).
	SoakStartedAt map[string]time.Time `json:"soak_started_at,omitempty"`
	// Now is the injectable clock (tests, replay). Zero-value struct is
	// fine: callers of time default to time.Now.
	Now func() time.Time `json:"-"`
	// RevertLog is the audit trail of backwards moves (L7). Events are
	// appended only on the returned copy, never in place.
	RevertLog []RevertEvent `json:"revert_log,omitempty"`
}

// RevertEvent is one auditable ring downgrade: who moved what from where
// to where, why, and when. A revert without a reason does not exist.
type RevertEvent struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// FreezeEvidence is the operator-supplied attestation required to promote
// into stable (L6): the sha256 of the frozen contracts manifest plus the
// verdict of the contract-test suite.
type FreezeEvidence struct {
	ManifestSHA256    string `json:"manifest_sha256"`
	ContractTestsPass bool   `json:"contract_tests_pass"`
}

// Release law errors (fail-closed — every denial is a named sentinel).
var (
	ErrReleaseRequired        = errors.New("release: release is required")
	ErrUnknownRing            = errors.New("release: ring must be one of internal, alpha, beta, stable (release.rings/1.0)")
	ErrVersionRequired        = errors.New("release: version is required")
	ErrVersionInvalid         = errors.New(`release: version must match ^v?[0-9]+\.[0-9]+\.[0-9]+$ (semver-ish, lowercase, no path or shell characters)`)
	ErrKillSwitchRequired     = errors.New("release: kill_switch must be non-empty outside internal (a release that cannot be stopped cannot ship)")
	ErrRingSkipped            = errors.New("release: ring promotion must move exactly one step forward (no_ring_skips: true)")
	ErrRingBackwards          = errors.New("release: ring downgrade is not an Advance — use Revert, with a reason")
	ErrAlreadyInRing          = errors.New("release: target ring equals current ring (no-op promotion)")
	ErrBetaUndercooked        = errors.New("release: beta soak below beta_soak_hours (release.rings/1.0)")
	ErrFreezeEvidenceRequired = errors.New("release: promoting to stable requires *FreezeEvidence (manifest sha256 attestation + green contract tests) — pass it as the third argument to Advance")
	ErrFreezeEvidenceMultiple = errors.New("release: Advance accepts at most one *FreezeEvidence")
	ErrFreezeMissing          = errors.New("release: freeze attestation missing or malformed (contracts/manifest.sha256 sha256 hex required)")
	ErrContractsRed           = errors.New("release: contract tests are not green — frozen contracts are regression law")
	ErrRevertReasonRequired   = errors.New("release: a revert is an auditable event — reason must be non-empty")
	ErrRevertNotBackwards     = errors.New("release: Revert only moves backwards — forward promotion goes through Advance")
)

// Validate enforces the state laws (L1-L3) on a release as it stands. The
// schema consts no_ring_skips and beta_soak_hours bind transitions, not
// values: any valid ring is a legal resting state; it is Advance that
// re-checks the transition law and re-runs Validate on the result.
func (r *Release) Validate() error {
	if r == nil {
		return ErrReleaseRequired
	}
	if _, ok := ringIndex(r.Ring); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRing, r.Ring)
	}
	if r.Version == "" {
		return ErrVersionRequired
	}
	if !versionPattern.MatchString(r.Version) {
		return fmt.Errorf("%w: %q", ErrVersionInvalid, r.Version)
	}
	if r.Ring != RingInternal && len(r.KillSwitch) == 0 {
		return fmt.Errorf("%w: ring %q", ErrKillSwitchRequired, r.Ring)
	}
	return nil
}

// Advance moves the release exactly one ring forward (L4), applying the
// beta soak law (L5) on beta -> stable, the freeze law (L6) on any
// promotion into stable, and re-validating the result (L3 fail-closed —
// prior state is never trusted). It never mutates the receiver: the
// returned Release is a value copy with Ring updated and
// SoakStartedAt[target] stamped (L7).
//
// The optional FreezeEvidence argument is required exactly when
// target == stable; nil evidence on a stable promotion fails closed.
// A zero time.Time means "use the clock": r.Now if set, time.Now
// otherwise.
func (r *Release) Advance(target string, now time.Time, evidence ...*FreezeEvidence) (*Release, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	ti, ok := ringIndex(target)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownRing, target)
	}
	ci, _ := ringIndex(r.Ring) // Validate guarantees membership
	switch {
	case ti == ci:
		return nil, fmt.Errorf("%w: %q", ErrAlreadyInRing, target)
	case ti < ci:
		return nil, fmt.Errorf("%w: %s -> %s", ErrRingBackwards, r.Ring, target)
	case ti > ci+1:
		return nil, fmt.Errorf("%w: %s -> %s", ErrRingSkipped, r.Ring, target)
	}

	if now.IsZero() {
		now = r.now()
	}

	// L5: beta soak law. beta_soak_hours: 48 (frozen const) is the dwell
	// time in BETA before STABLE. A missing entry stamp fails closed as
	// zero soak; so does a stamp in the future relative to now.
	if r.Ring == RingBeta && target == RingStable {
		entered, stamped := r.SoakStartedAt[RingBeta]
		soak := time.Duration(0)
		if stamped {
			soak = now.Sub(entered)
		}
		if soak < BetaSoakHours*time.Hour {
			return nil, fmt.Errorf("%w: beta soak %.1fh of %dh", ErrBetaUndercooked, soak.Hours(), BetaSoakHours)
		}
	}

	// L6: freeze gate on stable promotion.
	if target == RingStable {
		if len(evidence) > 1 {
			return nil, ErrFreezeEvidenceMultiple
		}
		if len(evidence) == 0 || evidence[0] == nil {
			return nil, ErrFreezeEvidenceRequired
		}
		if err := CheckFreeze(evidence[0].ManifestSHA256, evidence[0].ContractTestsPass); err != nil {
			return nil, err
		}
	}

	out := r.clone()
	out.Ring = target
	out.SoakStartedAt[target] = now

	// Fail closed rather than trust prior state: the moved release must
	// satisfy the state laws on its own — entering alpha without a kill
	// switch is a refusal even though the internal source validated.
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// Revert moves the release backwards along RingOrder — the only lawful
// way to downgrade (L4). It requires a non-empty reason and appends an
// auditable RevertEvent to the returned copy's RevertLog (L7): a revert
// is a logged event, never a silent move.
//
// Reverting re-arms the soak clock: the target ring gets a fresh
// SoakStartedAt entry, so a release pulled back from stable to beta must
// soak another full beta_soak_hours before it may promote again. A
// downgrade that instantly re-promotes would be governance theatre.
func (r *Release) Revert(target, reason string) (*Release, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, ErrRevertReasonRequired
	}
	ti, ok := ringIndex(target)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownRing, target)
	}
	ci, _ := ringIndex(r.Ring)
	switch {
	case ti == ci:
		return nil, fmt.Errorf("%w: %q", ErrAlreadyInRing, target)
	case ti > ci:
		return nil, fmt.Errorf("%w: %s -> %s", ErrRevertNotBackwards, r.Ring, target)
	}

	at := r.now()
	out := r.clone()
	out.Ring = target
	out.SoakStartedAt[target] = at
	out.RevertLog = append(out.RevertLog, RevertEvent{
		From:   r.Ring,
		To:     target,
		Reason: reason,
		At:     at,
	})
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// CheckFreeze is the stable-promotion gate (L6): the manifest SHA-256
// freeze attestation must be present and well-formed (64 lowercase hex —
// the contracts/manifest.sha256 shape) and the contract tests must be
// green. Missing or malformed attestation => ErrFreezeMissing; red tests
// => ErrContractsRed.
func CheckFreeze(manifestSha256 string, contractTestsPass bool) error {
	if !sha256Hex.MatchString(strings.TrimSpace(manifestSha256)) {
		return fmt.Errorf("%w: %q", ErrFreezeMissing, manifestSha256)
	}
	if !contractTestsPass {
		return ErrContractsRed
	}
	return nil
}

// now resolves the release clock: the injectable Now if provided,
// time.Now otherwise (zero-value structs stay usable).
func (r *Release) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// clone produces the independent copy that keeps Advance/Revert pure
// (L7): map and slices are deep-copied so mutating the result can never
// alias the receiver's state.
func (r *Release) clone() *Release {
	out := *r
	out.SoakStartedAt = make(map[string]time.Time, len(r.SoakStartedAt)+1)
	for ring, t := range r.SoakStartedAt {
		out.SoakStartedAt[ring] = t
	}
	if r.KillSwitch != nil {
		out.KillSwitch = append([]string(nil), r.KillSwitch...)
	}
	if r.RevertLog != nil {
		out.RevertLog = append([]RevertEvent(nil), r.RevertLog...)
	}
	return &out
}

// ringIndex locates a ring on the promotion ladder.
func ringIndex(ring string) (int, bool) {
	for i, r := range RingOrder {
		if r == ring {
			return i, true
		}
	}
	return -1, false
}
