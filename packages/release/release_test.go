// k-044 tests — release.rings/1.0 promotion law (ADR-0013).
//
// Freeze law under test:
//   - L1: ring must be in the frozen enum (anything else fails closed)
//   - L2: version is semver-ish ^v?[0-9]+\.[0-9]+\.[0-9]+$ — adversarial
//     inputs are branch names, uppercase, missing patch, shell/path
//     injection, trailing newline (the version flows into git tag later)
//   - L3: kill switch required outside internal — missing kill switch
//     blocks promotion into alpha even from a valid internal source
//   - L4: no-ring-skips full matrix (4x4 current x target); backwards
//     only via Revert; same-ring is a no-op error; Revert needs a reason
//   - L5: beta soak law — 47.9h fails, exactly 48.0h passes, missing or
//     future stamp fails closed; alpha->beta has no soak law; Revert
//     re-arms the soak clock
//   - L6: freeze law — stable without evidence fails closed; malformed
//     manifest hash => ErrFreezeMissing; red contract tests =>
//     ErrContractsRed; alpha/beta need no evidence
//   - L7: audit lineage — Advance/Revert never mutate the receiver
//     (maps/slices deep-copied); RevertLog appends are events, not hints
package release_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/release"
)

// real freeze attestation of this repo (contracts/manifest.sha256) —
// lowercase sha256 hex, 64 chars.
const (
	validManifest = "2d2f1d27474a908a19aafb9c152be5e27c80987400f21cdfca94080b8bf14a86"
	base          = "2026-09-01T12:00:00Z"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", s, err)
	}
	return ts
}

// rel builds a release resting in ring with a kill switch (external rings
// demand one) and no soak history.
func rel(version, ring string) *release.Release {
	r := &release.Release{Version: version, Ring: ring}
	if ring != release.RingInternal {
		r.KillSwitch = []string{"revert-ring"}
	}
	return r
}

func evidence() *release.FreezeEvidence {
	return &release.FreezeEvidence{ManifestSHA256: validManifest, ContractTestsPass: true}
}

// TestValidateL1RingEnum — the rings enum is the only doorway.
func TestValidateL1RingEnum(t *testing.T) {
	for _, ring := range release.RingOrder {
		r := rel("v1.2.3", ring)
		if err := r.Validate(); err != nil {
			t.Fatalf("valid ring %q rejected: %v", ring, err)
		}
	}
	// A release resting in stable is a legal STATE; no_ring_skips binds
	// transitions, not values.
	stable := &release.Release{Version: "v9.9.9", Ring: "stable", KillSwitch: []string{"x"}}
	if err := stable.Validate(); err != nil {
		t.Fatalf("resting state must be valid: %v", err)
	}
	adversarial := []string{"", "Internal", "ALPHA", "prod", "rc1", "stable ", "internal/alpha"}
	for _, ring := range adversarial {
		r := &release.Release{Version: "v1.0.0", Ring: ring, KillSwitch: []string{"x"}}
		if err := r.Validate(); !errors.Is(err, release.ErrUnknownRing) {
			t.Fatalf("ring %q: want ErrUnknownRing, got %v", ring, err)
		}
	}
	// nil receiver fails closed
	var nilRel *release.Release
	if err := nilRel.Validate(); !errors.Is(err, release.ErrReleaseRequired) {
		t.Fatalf("nil receiver: want ErrReleaseRequired, got %v", err)
	}
}

// TestValidateL2VersionLaw — semver-ish, anchored, injection-hostile.
func TestValidateL2VersionLaw(t *testing.T) {
	ok := []string{"v1.2.3", "1.2.3", "v0.0.1", "v10.20.30", "v2026.9.3"}
	bad := []string{
		"",                 // required
		"main",             // branch name is not a version
		"v1.2",             // patch required
		"v1",               // minor+patch required
		"V1.2.3",           // uppercase rejected
		"v1.2.3;rm -rf",    // shell injection, destined for git tag
		"v1.2.3\n",         // trailing newline (Go $ has no \Z loophole)
		"v1.2.3\t",         // trailing tab
		"../v1.2.3",        // path traversal
		"v1.2/3",           // slash
		"v1.2.3-beta",      // prerelease suffix outside the law
		"v1.2.3-rc1+build", // build metadata outside the law
		"version1.2.3",     // prefix junk
		"v1.2.3 main",      // command chaining on the same line
	}
	for _, v := range ok {
		r := &release.Release{Version: v, Ring: release.RingInternal}
		if err := r.Validate(); err != nil {
			t.Fatalf("version %q rejected: %v", v, err)
		}
	}
	for _, v := range bad {
		r := &release.Release{Version: v, Ring: release.RingInternal}
		err := r.Validate()
		if err == nil {
			t.Fatalf("adversarial version %q accepted", v)
		}
		want := release.ErrVersionRequired
		if v != "" {
			want = release.ErrVersionInvalid
		}
		if !errors.Is(err, want) {
			t.Fatalf("version %q: want %v, got %v", v, want, err)
		}
	}
}

// TestValidateL3KillSwitchLaw — no kill switch, no ship.
func TestValidateL3KillSwitchLaw(t *testing.T) {
	// internal may rest without a kill switch
	if err := rel("v1.2.3", release.RingInternal).Validate(); err != nil {
		t.Fatalf("internal without kill switch must be valid: %v", err)
	}
	for _, ring := range []string{release.RingAlpha, release.RingBeta, release.RingStable} {
		r := &release.Release{Version: "v1.2.3", Ring: ring}
		if err := r.Validate(); !errors.Is(err, release.ErrKillSwitchRequired) {
			t.Fatalf("ring %q nil kill switch: want ErrKillSwitchRequired, got %v", ring, err)
		}
		empty := &release.Release{Version: "v1.2.3", Ring: ring, KillSwitch: []string{}}
		if err := empty.Validate(); !errors.Is(err, release.ErrKillSwitchRequired) {
			t.Fatalf("ring %q empty kill switch: want ErrKillSwitchRequired, got %v", ring, err)
		}
	}
}

// TestAdvanceKillSwitchBlocksAlpha — the re-validation after the move is
// real: a valid internal source with no kill switch cannot enter alpha.
func TestAdvanceKillSwitchBlocksAlpha(t *testing.T) {
	r := rel("v1.2.3", release.RingInternal) // validates fine
	next, err := r.Advance(release.RingAlpha, mustParse(t, base))
	if !errors.Is(err, release.ErrKillSwitchRequired) {
		t.Fatalf("kill-switch-less alpha entry: want ErrKillSwitchRequired, got %v", err)
	}
	if next != nil {
		t.Fatal("refused promotion must return nil release")
	}
}

// TestAdvanceL4NoSkipMatrix — every pair on the ladder, exactly one step
// forward or nothing.
func TestAdvanceL4NoSkipMatrix(t *testing.T) {
	now := mustParse(t, base)
	for _, from := range release.RingOrder {
		for _, to := range release.RingOrder {
			r := rel("v1.2.3", from)
			// kill switch supplied for every walk so the matrix tests
			// L4 transitions, not L3 (kill switch has its own tests)
			r.KillSwitch = []string{"revert-ring"}
			// soak the beta ring long enough for a lawful beta->stable
			r.SoakStartedAt = map[string]time.Time{release.RingBeta: now.Add(-49 * time.Hour)}
			var ev []*release.FreezeEvidence
			if to == release.RingStable {
				ev = append(ev, evidence())
			}
			next, err := r.Advance(to, now, ev...)
			switch {
			case to == from:
				if !errors.Is(err, release.ErrAlreadyInRing) {
					t.Fatalf("%s->%s: want ErrAlreadyInRing, got %v", from, to, err)
				}
			case indexOf(to) < indexOf(from):
				if !errors.Is(err, release.ErrRingBackwards) {
					t.Fatalf("%s->%s: want ErrRingBackwards, got %v", from, to, err)
				}
			case indexOf(to) > indexOf(from)+1:
				if !errors.Is(err, release.ErrRingSkipped) {
					t.Fatalf("%s->%s: want ErrRingSkipped, got %v", from, to, err)
				}
			default: // exactly one step forward
				if err != nil {
					t.Fatalf("lawful %s->%s rejected: %v", from, to, err)
				}
				if next.Ring != to {
					t.Fatalf("%s->%s: landed in %q", from, to, next.Ring)
				}
				if !next.SoakStartedAt[to].Equal(now) {
					t.Fatalf("%s->%s: entry stamp not set to now", from, to)
				}
			}
			if next == nil && err == nil {
				t.Fatalf("%s->%s: nil with nil error", from, to)
			}
		}
	}
	// unknown target ring fails closed
	r := rel("v1.2.3", release.RingBeta)
	if _, err := r.Advance("prod", now); !errors.Is(err, release.ErrUnknownRing) {
		t.Fatalf("unknown target: want ErrUnknownRing, got %v", err)
	}
}

func indexOf(ring string) int {
	for i, r := range release.RingOrder {
		if r == ring {
			return i
		}
	}
	return -1
}

// TestAdvanceL5BetaSoakLaw — beta_soak_hours: 48 (frozen const) is dwell
// in BETA before STABLE. alpha->beta carries no soak law.
func TestAdvanceL5BetaSoakLaw(t *testing.T) {
	now := mustParse(t, base)
	stableOK := func(soakStart *time.Time) (*release.Release, error) {
		r := rel("v1.2.3", release.RingBeta)
		if soakStart != nil {
			r.SoakStartedAt = map[string]time.Time{release.RingBeta: *soakStart}
		}
		return r.Advance(release.RingStable, now, evidence())
	}
	at48 := now.Add(-48 * time.Hour)                     // exactly at the boundary
	justUnder := now.Add(-47*time.Hour - 54*time.Minute) // 47.9h
	over := now.Add(-50 * time.Hour)                     // comfortable
	future := now.Add(1 * time.Hour)                     // clock skew, fails closed

	tests := []struct {
		name    string
		start   *time.Time
		wantErr bool
	}{
		{name: "exactly 48.0h passes", start: &at48},
		{name: "50h passes", start: &over},
		{name: "47.9h fails", start: &justUnder, wantErr: true},
		{name: "missing stamp fails closed", start: nil, wantErr: true},
		{name: "future stamp fails closed", start: &future, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, err := stableOK(tt.start)
			if tt.wantErr {
				if !errors.Is(err, release.ErrBetaUndercooked) {
					t.Fatalf("want ErrBetaUndercooked, got %v", err)
				}
				if next != nil {
					t.Fatal("undercooked promotion must return nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("lawful promotion rejected: %v", err)
			}
			if next.Ring != release.RingStable {
				t.Fatalf("want stable, got %q", next.Ring)
			}
		})
	}
	// 47.9h error message must show the hours ("beta soak 47.9h of 48h")
	if _, err := stableOK(&justUnder); err == nil || !strings.Contains(err.Error(), "47.9h of 48h") {
		t.Fatalf("soak message: want '47.9h of 48h', got %v", err)
	}
	// alpha -> beta has NO soak law: one minute of alpha dwell is enough
	alpha := rel("v1.0.0", release.RingAlpha)
	alpha.SoakStartedAt = map[string]time.Time{release.RingAlpha: now.Add(-time.Minute)}
	if next, err := alpha.Advance(release.RingBeta, now); err != nil || next.Ring != release.RingBeta {
		t.Fatalf("alpha->beta must not soak: next=%v err=%v", next, err)
	}
}

// TestAdvanceL6FreezeGate — stable without attestation does not happen.
func TestAdvanceL6FreezeGate(t *testing.T) {
	now := mustParse(t, base)
	soaked := func() *release.Release {
		r := rel("v1.2.3", release.RingBeta)
		r.SoakStartedAt = map[string]time.Time{release.RingBeta: now.Add(-49 * time.Hour)}
		return r
	}
	// nil evidence + stable => fail closed with operator-facing message
	_, err := soaked().Advance(release.RingStable, now)
	if !errors.Is(err, release.ErrFreezeEvidenceRequired) {
		t.Fatalf("nil evidence: want ErrFreezeEvidenceRequired, got %v", err)
	}
	if !strings.Contains(err.Error(), "FreezeEvidence") {
		t.Fatalf("nil evidence error must tell the operator what to pass: %v", err)
	}
	// explicit nil is the same refusal
	_, err = soaked().Advance(release.RingStable, now, nil)
	if !errors.Is(err, release.ErrFreezeEvidenceRequired) {
		t.Fatalf("explicit nil: want ErrFreezeEvidenceRequired, got %v", err)
	}
	// malformed attestation
	badHash := soaked()
	_, err = badHash.Advance(release.RingStable, now, &release.FreezeEvidence{ManifestSHA256: "not-a-hash", ContractTestsPass: true})
	if !errors.Is(err, release.ErrFreezeMissing) {
		t.Fatalf("bad manifest: want ErrFreezeMissing, got %v", err)
	}
	// uppercase hex is not the manifest format (sha256sum emits lowercase)
	_, err = soaked().Advance(release.RingStable, now, &release.FreezeEvidence{ManifestSHA256: strings.ToUpper(validManifest), ContractTestsPass: true})
	if !errors.Is(err, release.ErrFreezeMissing) {
		t.Fatalf("uppercase manifest: want ErrFreezeMissing, got %v", err)
	}
	// red contract tests
	_, err = soaked().Advance(release.RingStable, now, &release.FreezeEvidence{ManifestSHA256: validManifest, ContractTestsPass: false})
	if !errors.Is(err, release.ErrContractsRed) {
		t.Fatalf("red tests: want ErrContractsRed, got %v", err)
	}
	// two conflicting evidence objects is ambiguous input — fail closed
	_, err = soaked().Advance(release.RingStable, now, evidence(), evidence())
	if !errors.Is(err, release.ErrFreezeEvidenceMultiple) {
		t.Fatalf("double evidence: want ErrFreezeEvidenceMultiple, got %v", err)
	}
	// good evidence promotes
	if _, err := soaked().Advance(release.RingStable, now, evidence()); err != nil {
		t.Fatalf("attested stable promotion rejected: %v", err)
	}
	// alpha/beta promotions need no evidence at all
	interior := rel("v1.2.3", release.RingInternal)
	interior.KillSwitch = []string{"revert"}
	if next, err := interior.Advance(release.RingAlpha, now); err != nil || next.Ring != release.RingAlpha {
		t.Fatalf("internal->alpha without evidence must pass: next=%v err=%v", next, err)
	}
}

// TestCheckFreezeDirect — the gate is a law with its own teeth.
func TestCheckFreezeDirect(t *testing.T) {
	if err := release.CheckFreeze(validManifest, true); err != nil {
		t.Fatalf("valid attestation rejected: %v", err)
	}
	if err := release.CheckFreeze("", true); !errors.Is(err, release.ErrFreezeMissing) {
		t.Fatalf("empty manifest: want ErrFreezeMissing, got %v", err)
	}
	if err := release.CheckFreeze(validManifest[:63], true); !errors.Is(err, release.ErrFreezeMissing) {
		t.Fatalf("short manifest: want ErrFreezeMissing, got %v", err)
	}
	if err := release.CheckFreeze(validManifest, false); !errors.Is(err, release.ErrContractsRed) {
		t.Fatalf("red tests: want ErrContractsRed, got %v", err)
	}
	// format law checked before verdict law, but red verdict still bites
	// with a valid hash (order: attestation present, then green).
	if err := release.CheckFreeze("  "+validManifest+"  ", true); err != nil {
		t.Fatalf("whitespace-wrapped valid manifest rejected: %v", err)
	}
}

// TestAdvanceL7ValueCopyAudit — the receiver is untouched; the returned
// release shares no mutable state with it.
func TestAdvanceL7ValueCopyAudit(t *testing.T) {
	now := mustParse(t, base)
	orig := rel("v1.2.3", release.RingAlpha)
	orig.SoakStartedAt = map[string]time.Time{release.RingAlpha: now.Add(-3 * time.Hour)}
	before := map[string]any{
		"ring": orig.Ring, "version": orig.Version,
		"soakLen": len(orig.SoakStartedAt), "ksLen": len(orig.KillSwitch),
	}

	next, err := orig.Advance(release.RingBeta, now)
	if err != nil {
		t.Fatalf("lawful advance rejected: %v", err)
	}
	// receiver unchanged
	if orig.Ring != release.RingAlpha {
		t.Fatalf("L7 violated: receiver ring mutated to %q", orig.Ring)
	}
	if _, stamped := orig.SoakStartedAt[release.RingBeta]; stamped {
		t.Fatal("L7 violated: receiver soak map got the target's stamp")
	}
	if len(orig.SoakStartedAt) != before["soakLen"] || len(orig.KillSwitch) != before["ksLen"] {
		t.Fatalf("L7 violated: receiver collections grew: %+v", before)
	}
	// returned value is correct
	if next.Ring != release.RingBeta || !next.SoakStartedAt[release.RingBeta].Equal(now) {
		t.Fatalf("advanced release wrong: ring=%q stamp=%v", next.Ring, next.SoakStartedAt[release.RingBeta])
	}
	// no aliasing: mutating the copy's state cannot reach the receiver
	next.KillSwitch[0] = "poisoned"
	next.SoakStartedAt[release.RingAlpha] = time.Time{}
	if orig.KillSwitch[0] != "revert-ring" {
		t.Fatal("L7 violated: kill switch slice aliases receiver")
	}
	if !orig.SoakStartedAt[release.RingAlpha].Equal(now.Add(-3 * time.Hour)) {
		t.Fatal("L7 violated: soak map aliases receiver")
	}
	// failed transitions return nil, never a half-moved value
	if _, err := orig.Advance(release.RingStable, now); !errors.Is(err, release.ErrRingSkipped) {
		t.Fatalf("alpha->stable: want ErrRingSkipped, got %v", err)
	}
}

// TestRevert — backwards moves only here, always with a logged reason.
func TestRevert(t *testing.T) {
	stableRel := func() *release.Release {
		r := rel("v1.2.3", release.RingStable)
		r.Now = clockAt(mustParse(t, base))
		r.SoakStartedAt = map[string]time.Time{release.RingBeta: mustParse(t, base).Add(-49 * time.Hour)}
		return r
	}
	// reason required (empty and whitespace-only)
	for _, reason := range []string{"", "   ", "\t"} {
		if _, err := stableRel().Revert(release.RingBeta, reason); !errors.Is(err, release.ErrRevertReasonRequired) {
			t.Fatalf("reason %q: want ErrRevertReasonRequired, got %v", reason, err)
		}
	}
	// backwards via Revert works and is auditable
	next, err := stableRel().Revert(release.RingBeta, "regression in payment sync (INC-442)")
	if err != nil {
		t.Fatalf("lawful revert rejected: %v", err)
	}
	if next.Ring != release.RingBeta {
		t.Fatalf("revert landed in %q", next.Ring)
	}
	if len(next.RevertLog) != 1 {
		t.Fatalf("want 1 revert event, got %d", len(next.RevertLog))
	}
	ev := next.RevertLog[0]
	if ev.From != release.RingStable || ev.To != release.RingBeta || ev.Reason != "regression in payment sync (INC-442)" {
		t.Fatalf("revert event wrong: %+v", ev)
	}
	if ev.At.IsZero() {
		t.Fatal("revert event must carry a timestamp")
	}
	// receiver untouched (L7)
	orig := stableRel()
	if _, err := orig.Revert(release.RingBeta, "audit me"); err != nil {
		t.Fatalf("revert rejected: %v", err)
	}
	if len(orig.RevertLog) != 0 {
		t.Fatal("L7 violated: receiver RevertLog mutated")
	}
	if len(orig.SoakStartedAt) != 1 {
		t.Fatal("L7 violated: receiver soak map mutated by revert")
	}
	// forward via Revert is refused — Revert only goes backwards
	alpha := rel("v1.0.0", release.RingAlpha)
	if _, err := alpha.Revert(release.RingBeta, "this is not a revert"); !errors.Is(err, release.ErrRevertNotBackwards) {
		t.Fatalf("forward revert: want ErrRevertNotBackwards, got %v", err)
	}
	// same ring
	if _, err := stableRel().Revert(release.RingStable, "nonsense"); !errors.Is(err, release.ErrAlreadyInRing) {
		t.Fatalf("same-ring revert: want ErrAlreadyInRing, got %v", err)
	}
	// unknown ring
	if _, err := stableRel().Revert("prod", "why"); !errors.Is(err, release.ErrUnknownRing) {
		t.Fatalf("unknown ring revert: want ErrUnknownRing, got %v", err)
	}
	// invalid source fails closed before anything else
	naked := &release.Release{Version: "v1.0.0", Ring: "beta"} // no kill switch
	if _, err := naked.Revert(release.RingAlpha, "escape"); !errors.Is(err, release.ErrKillSwitchRequired) {
		t.Fatalf("invalid source: want ErrKillSwitchRequired, got %v", err)
	}
}

// TestRevertReArmsSoakClock — governance theatre check: a release pulled
// back from stable to beta must soak a fresh beta_soak_hours before it
// may promote again.
func TestRevertReArmsSoakClock(t *testing.T) {
	now := mustParse(t, base)
	stable := rel("v1.2.3", release.RingStable)
	stable.Now = clockAt(now)
	// stale beta stamp from long ago — the pre-revert world
	stable.SoakStartedAt = map[string]time.Time{release.RingBeta: now.Add(-100 * time.Hour)}

	back, err := stable.Revert(release.RingBeta, "INC-442")
	if err != nil {
		t.Fatalf("revert rejected: %v", err)
	}
	// re-entry stamp is the revert moment
	if !back.SoakStartedAt[release.RingBeta].Equal(now) {
		t.Fatalf("revert must re-stamp beta entry: got %v", back.SoakStartedAt[release.RingBeta])
	}
	// instant re-promotion is refused: only 1h has passed since re-entry
	_, err = back.Advance(release.RingStable, now.Add(time.Hour), evidence())
	if !errors.Is(err, release.ErrBetaUndercooked) {
		t.Fatalf("instant re-promotion: want ErrBetaUndercooked, got %v", err)
	}
	// and 48h after the revert it is lawful again
	if next, err := back.Advance(release.RingStable, now.Add(48*time.Hour), evidence()); err != nil || next.Ring != release.RingStable {
		t.Fatalf("post-soak re-promotion rejected: next=%v err=%v", next, err)
	}
}

// TestNowInjection — the clock is the operator's, not the runtime's:
// zero time.Time on Advance/Revert resolves through Release.Now.
func TestNowInjection(t *testing.T) {
	fixed := mustParse(t, "2026-10-04T08:30:00Z")
	r := rel("v1.2.3", release.RingAlpha)
	r.Now = clockAt(fixed)
	next, err := r.Advance(release.RingBeta, time.Time{}) // zero now -> use r.Now
	if err != nil {
		t.Fatalf("advance with injected clock rejected: %v", err)
	}
	if got := next.SoakStartedAt[release.RingBeta]; !got.Equal(fixed) {
		t.Fatalf("stamp %v must be the injected clock %v", got, fixed)
	}
	// explicit now wins over the injected field
	later := fixed.Add(72 * time.Hour)
	next2, err := next.Advance(release.RingStable, later, evidence())
	if err != nil {
		t.Fatalf("explicit-now advance rejected: %v", err)
	}
	if got := next2.SoakStartedAt[release.RingStable]; !got.Equal(later) {
		t.Fatalf("explicit now must win: %v", got)
	}
	// Revert stamps with the injected clock too
	back, err := next2.Revert(release.RingBeta, "postmortem")
	if err != nil {
		t.Fatalf("revert rejected: %v", err)
	}
	if !back.RevertLog[len(back.RevertLog)-1].At.Equal(fixed) {
		t.Fatalf("revert At must use r.Now: %v", back.RevertLog[len(back.RevertLog)-1].At)
	}
}

func clockAt(t time.Time) func() time.Time {
	return func() time.Time { return t }
}
