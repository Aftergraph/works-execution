package evidence

// Tests for the ADR-0024 obslaw wiring on the evidence side (k-052).

import (
	"errors"
	"reflect"
	"testing"

	"github.com/JonasAbde/works-execution/packages/obslaw"
)

// sampleBundle builds a minimal but law-representative Bundle by hand
// (no store needed: the projection is content-independent by design).
func sampleBundle() *Bundle {
	return &Bundle{
		BundleID: "evb_0123456789abcdef0123456789abcdef",
		WorkID:   "work-1",
		Signatures: []Signature{{
			KeyID:     "test-key",
			Algorithm: "ecdsa-p256",
			Value:     "aaa",
		}},
	}
}

func TestObsLawRecordProjection(t *testing.T) {
	rec, err := sampleBundle().ObsLawRecord()
	if err != nil {
		t.Fatalf("ObsLawRecord: %v", err)
	}
	if rec.Kind != obslaw.KindEvidence {
		t.Errorf("Kind = %q, want %q", rec.Kind, obslaw.KindEvidence)
	}
	if !rec.Signed {
		t.Error("Signed = false, want true (evidence must be signed)")
	}
	if rec.Trimmable {
		t.Error("Trimmable = true, want false (evidence is inalienable)")
	}
	if err := rec.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	// The kernel constructor is the single source of truth: the
	// projection must equal NewEvidence(true, "") exactly.
	want, err := obslaw.NewEvidence(true, "")
	if err != nil {
		t.Fatalf("NewEvidence: %v", err)
	}
	if *rec != *want {
		t.Errorf("projection = %+v, want %+v", *rec, *want)
	}
}

func TestObsLawRecordCitesHashEmptyByDesign(t *testing.T) {
	rec, err := sampleBundle().ObsLawRecord()
	if err != nil {
		t.Fatalf("ObsLawRecord: %v", err)
	}
	if rec.CitesHash != "" {
		t.Fatalf("CitesHash = %q, want empty by design", rec.CitesHash)
	}
	// Documented rationale, pinned as a test: bundle_id carries only a
	// 32-hex truncation of sha256(canonical); the kernel law requires
	// 64 hex for a non-empty cites_hash, and we refuse to weaken it.
	// A 32-hex cites_hash MUST be rejected by the kernel.
	bad := &obslaw.Record{
		Kind:      obslaw.KindEvidence,
		Signed:    true,
		CitesHash: "0123456789abcdef0123456789abcdef",
	}
	if err := bad.Validate(); !errors.Is(err, obslaw.ErrCitesHashFormat) {
		t.Fatalf("32-hex cites_hash error = %v, want ErrCitesHashFormat", err)
	}
}

func TestObsLawRecordNilBundle(t *testing.T) {
	var b *Bundle
	if _, err := b.ObsLawRecord(); err == nil {
		t.Fatal("ObsLawRecord(nil bundle) = nil error, want error")
	}
	if _, err := AttestBundle(nil, testKey(t)); err == nil {
		t.Fatal("AttestBundle(nil) = nil error, want error")
	}
}

func TestAttestBundleVerifyRoundTrip(t *testing.T) {
	key := testKey(t)
	b := sampleBundle()
	att, err := AttestBundle(b, key)
	if err != nil {
		t.Fatalf("AttestBundle: %v", err)
	}
	if att == nil || att.Record == nil || att.Signature == "" {
		t.Fatalf("AttestBundle returned incomplete Attested: %+v", att)
	}
	v, err := obslaw.NewVerifier(key)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if !v.Verify(att.Record, att.Signature) {
		t.Fatal("Verify(AttestBundle(...)) = false, want true")
	}
}

func TestAttestBundleTamperDetected(t *testing.T) {
	key := testKey(t)
	att, err := AttestBundle(sampleBundle(), key)
	if err != nil {
		t.Fatalf("AttestBundle: %v", err)
	}
	v, err := obslaw.NewVerifier(key)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	cases := []struct {
		name string
		mut  func(r *obslaw.Record)
	}{
		{"flip trimmable", func(r *obslaw.Record) { r.Trimmable = true }},
		{"flip signed", func(r *obslaw.Record) { r.Signed = false }},
		{"recategorize to event", func(r *obslaw.Record) { r.Kind = obslaw.KindEvent }},
		{"graft full cites hash", func(r *obslaw.Record) {
			r.CitesHash = "0000000000000000000000000000000000000000000000000000000000000000"
		}},
	}
	for _, tc := range cases {
		tampered := *att.Record // copy; one field flipped per case
		tc.mut(&tampered)
		if v.Verify(&tampered, att.Signature) {
			t.Errorf("%s: Verify = true, want false", tc.name)
		}
	}
	// The pristine record still verifies -- tampering a copy cannot
	// invalidate the original attestation.
	if !v.Verify(att.Record, att.Signature) {
		t.Error("original record fails Verify after copy-mutation tests")
	}
}

func TestAttestBundleWrongKeyRejected(t *testing.T) {
	att, err := AttestBundle(sampleBundle(), testKey(t))
	if err != nil {
		t.Fatalf("AttestBundle: %v", err)
	}
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(i + 1)
	}
	v, err := obslaw.NewVerifier(other)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if v.Verify(att.Record, att.Signature) {
		t.Fatal("Verify with wrong key = true, want false")
	}
}

func TestAttestBundleDoesNotMutate(t *testing.T) {
	key := testKey(t)
	b := sampleBundle()
	bBefore := *b
	sigBefore := append([]Signature(nil), b.Signatures...)

	att, err := AttestBundle(b, key)
	if err != nil {
		t.Fatalf("AttestBundle: %v", err)
	}
	if b.BundleID != bBefore.BundleID || b.WorkID != bBefore.WorkID {
		t.Error("AttestBundle mutated the bundle")
	}
	if !reflect.DeepEqual(b.Signatures, sigBefore) {
		t.Errorf("AttestBundle touched b.Signatures: %v -> %v", sigBefore, b.Signatures)
	}
	// Kernel convention: Sign returns the sig without mutating the
	// Record; Attested keeps the signature OUTSIDE the schema shape.
	recBefore := *att.Record
	recomputed, err := b.ObsLawRecord()
	if err != nil {
		t.Fatalf("ObsLawRecord after attest: %v", err)
	}
	if *att.Record != recBefore {
		t.Error("attested record was mutated")
	}
	if *recomputed != recBefore {
		t.Error("projection is not reproducible after attestation")
	}
}

func TestProduceHotPathLawHookIsTransparent(t *testing.T) {
	// The hook must never fire for a legitimately produced bundle:
	// exercise it directly on a fully populated bundle mirroring what
	// Produce hands back at the hook site.
	if err := checkBundleLaw(sampleBundle()); err != nil {
		t.Fatalf("checkBundleLaw on valid bundle: %v", err)
	}
	if err := checkBundleLaw(nil); err == nil {
		t.Fatal("checkBundleLaw(nil) = nil error, want error")
	}
}

func TestEvidenceLawTeethAgainstDrift(t *testing.T) {
	// Synthetic mutated Record showing the kernel rejects an unsigned
	// "evidence" -- the drift scenario the Produce hook guards.
	mutated := &obslaw.Record{Kind: obslaw.KindEvidence, Signed: false, Trimmable: false}
	if err := mutated.Validate(); err == nil {
		t.Fatal("unsigned evidence record passed Validate; law has no teeth")
	}
	if _, err := obslaw.NewEvidence(false, ""); err == nil {
		t.Fatal("NewEvidence(signed=false) constructed; illegal state representable")
	}
}
