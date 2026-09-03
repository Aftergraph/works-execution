package evidence

// ADR-0024 evidence/event boundary wiring (k-052, evidence side).
//
// This file projects the Bundle type onto the frozen obslaw kernel schema
// (packages/obslaw) WITHOUT touching the bundle's own HMAC signing. The
// existing Signatures field and Verify() remain the content-integrity
// mechanism; obslaw attestation is a *category* assertion layered beside
// them, never replacing them.

import (
	"errors"
	"fmt"

	"github.com/JonasAbde/works-execution/packages/obslaw"
)

// ObsLawRecord projects b onto the ADR-0024 obslaw schema shape as an
// evidence record.
//
// The projection is deliberately constant in kind: a Bundle is by
// definition the evidence side of the boundary, so it maps to
// obslaw.NewEvidence(signed=true, cites_hash="") -- the kernel's
// illegal-states-unrepresentable constructor (it refuses signed=false
// and forces trimmable=false). The bundle *content*'s signature presence
// is enforced by Produce/Verify (HMAC over canonical JSON); the law
// Record attests the category only.
//
// CitesHash is intentionally empty. The kernel law requires a full
// 64-hex sha256 digest for a non-empty cites_hash, but bundle_id is
// "evb_" + the first 32 hex chars of sha256(canonical) -- a deliberate
// truncation in the bundle-id design -- and the full digest is not
// retained on the Bundle struct after Produce returns. Rather than
// weaken the kernel by handing it a 32-hex truncation (or re-deriving
// bytes here, which would couple the projection to the placeholder
// substitution rules), we pass the empty string, which the schema
// explicitly allows ("when set" is the only time the 64-hex rule bites).
// A future slice that stores the full digest on Bundle can populate
// cites_hash without any kernel change.
func (b *Bundle) ObsLawRecord() (*obslaw.Record, error) {
	if b == nil {
		return nil, errors.New("evidence: nil bundle has no obslaw record")
	}
	rec, err := obslaw.NewEvidence(true, "")
	if err != nil {
		return nil, fmt.Errorf("evidence: obslaw evidence projection: %w", err)
	}
	return rec, nil
}

// AttestBundle produces a law-level obslaw.Attested pair for b via the
// kernel Verifier (HMAC-SHA256 over CanonicalJSON of the Record).
//
// This is IN ADDITION TO the bundle's own Signatures: callers who want a
// schema-level attestation call AttestBundle and keep the result beside
// the bundle; Produce does not fold it into b.Signatures (that would
// change the wire shape for existing consumers, violating the zero-
// behavior-change rule for this slice).
//
// Mutation semantics mirror the kernel convention (see
// packages/obslaw/verifier.go Sign): the signature lives OUTSIDE the
// Record, inside Attested, and neither ObsLawRecord nor Sign mutates
// anything -- b is not touched at all.
//
// key must be at least 32 bytes (obslaw.NewVerifier enforces this).
func AttestBundle(b *Bundle, key []byte) (*obslaw.Attested, error) {
	rec, err := b.ObsLawRecord()
	if err != nil {
		return nil, err
	}
	v, err := obslaw.NewVerifier(key)
	if err != nil {
		return nil, fmt.Errorf("evidence: obslaw attester: %w", err)
	}
	att, err := v.Attest(rec)
	if err != nil {
		return nil, fmt.Errorf("evidence: obslaw attest: %w", err)
	}
	return att, nil
}

// checkBundleLaw is the hot-path fail-fast assertion wired into Produce
// (see the call site in bundle.go for the hook-choice comment). It
// re-derives the projection and validates it against the kernel law;
// any error means the bundle being handed to the caller would violate
// ADR-0024 if it escaped, so Produce returns it as a build failure
// instead.
func checkBundleLaw(b *Bundle) error {
	rec, err := b.ObsLawRecord()
	if err != nil {
		return err
	}
	if err := rec.Validate(); err != nil {
		return fmt.Errorf("evidence: obslaw boundary law violated: %w", err)
	}
	return nil
}
