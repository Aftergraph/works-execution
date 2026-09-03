package obslaw

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// sha256Hex returns a 64-hex sha256 of input. Useful for crafting
// valid cites_hash values in tests.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hexEncode(h[:])
}

func hexEncode(b []byte) string {
	const hexchars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, by := range b {
		out[i*2] = hexchars[by>>4]
		out[i*2+1] = hexchars[by&0x0f]
	}
	return string(out)
}

// -------- Validate: every schema if/then branch --------

func TestValidate_Event_NoCites_OK(t *testing.T) {
	r := &Record{Kind: KindEvent, Signed: false, Trimmable: true}
	if err := r.Validate(); err != nil {
		t.Fatalf("event with no cites should validate, got %v", err)
	}
}

func TestValidate_Event_Signed_Fails(t *testing.T) {
	r := &Record{Kind: KindEvent, Signed: true, Trimmable: true}
	if err := r.Validate(); !errors.Is(err, ErrEventCannotBeSigned) {
		t.Fatalf("event+signed must return ErrEventCannotBeSigned, got %v", err)
	}
}

func TestValidate_Evidence_Signed_NotTrimmable_OK(t *testing.T) {
	r := &Record{Kind: KindEvidence, Signed: true, Trimmable: false, CitesHash: sha256Hex("upstream")}
	if err := r.Validate(); err != nil {
		t.Fatalf("evidence+signed+notTrimmable should validate, got %v", err)
	}
}

func TestValidate_Evidence_Unsigned_Fails(t *testing.T) {
	r := &Record{Kind: KindEvidence, Signed: false, Trimmable: false}
	if err := r.Validate(); !errors.Is(err, ErrEvidenceMustBeSigned) {
		t.Fatalf("evidence+unsigned must return ErrEvidenceMustBeSigned, got %v", err)
	}
}

func TestValidate_Evidence_Trimmable_Fails(t *testing.T) {
	r := &Record{Kind: KindEvidence, Signed: true, Trimmable: true}
	if err := r.Validate(); !errors.Is(err, ErrEvidenceNotTrimmable) {
		t.Fatalf("evidence+trimmable must return ErrEvidenceNotTrimmable, got %v", err)
	}
}

func TestValidate_UnknownKind_FailsClosed(t *testing.T) {
	cases := []string{"logs", "", "EVENT", "Evidence", "trace", "metric"}
	for _, k := range cases {
		r := &Record{Kind: k, Signed: false, Trimmable: true}
		err := r.Validate()
		if err == nil {
			t.Errorf("kind=%q must fail-closed, got nil", k)
			continue
		}
		if !errors.Is(err, ErrUnknownKind) {
			t.Errorf("kind=%q must wrap ErrUnknownKind, got %v", k, err)
		}
	}
}

func TestValidate_CitesHash_NonHex_Rejected(t *testing.T) {
	// 65 chars, non-hex
	bad := strings.Repeat("z", 65)
	r := &Record{Kind: KindEvidence, Signed: true, Trimmable: false, CitesHash: bad}
	if err := r.Validate(); !errors.Is(err, ErrCitesHashFormat) {
		t.Fatalf("non-hex 65-char cites_hash must return ErrCitesHashFormat, got %v", err)
	}
}

func TestValidate_CitesHash_ShortHex_Rejected(t *testing.T) {
	// 63 chars hex
	bad := strings.Repeat("a", 63)
	r := &Record{Kind: KindEvidence, Signed: true, Trimmable: false, CitesHash: bad}
	if err := r.Validate(); !errors.Is(err, ErrCitesHashFormat) {
		t.Fatalf("63-hex cites_hash must return ErrCitesHashFormat, got %v", err)
	}
}

func TestValidate_CitesHash_UppercaseHex_OK(t *testing.T) {
	// Uppercase hex must be accepted (hex.DecodeString accepts both cases).
	h := sha256Hex("x")
	upper := strings.ToUpper(h)
	r := &Record{Kind: KindEvidence, Signed: true, Trimmable: false, CitesHash: upper}
	if err := r.Validate(); err != nil {
		t.Fatalf("uppercase hex cites_hash should validate, got %v", err)
	}
}

// -------- Constructors: illegal states unrepresentable --------

func TestNewEvent_SignedFalse_Built(t *testing.T) {
	r, err := NewEvent("")
	if err != nil {
		t.Fatalf("NewEvent(\"\") should succeed, got %v", err)
	}
	if r.Signed {
		t.Fatalf("NewEvent must produce Signed=false")
	}
	if !r.Trimmable {
		t.Fatalf("NewEvent must produce Trimmable=true (events are disposable)")
	}
	if r.Kind != KindEvent {
		t.Fatalf("NewEvent Kind=%q, want %q", r.Kind, KindEvent)
	}
}

func TestNewEvent_WithCitesHash_OK(t *testing.T) {
	r, err := NewEvent(sha256Hex("trace-link"))
	if err != nil {
		t.Fatalf("NewEvent with valid cites_hash should succeed, got %v", err)
	}
	if r.CitesHash == "" {
		t.Fatal("cites_hash should round-trip")
	}
}

func TestNewEvent_BadCitesHash_Rejected(t *testing.T) {
	_, err := NewEvent("not-a-hash")
	if !errors.Is(err, ErrCitesHashFormat) {
		t.Fatalf("NewEvent with bad cites_hash must error, got %v", err)
	}
}

func TestNewEvidence_SignedFalse_Refused(t *testing.T) {
	_, err := NewEvidence(false, "")
	if !errors.Is(err, ErrEvidenceMustBeSigned) {
		t.Fatalf("NewEvidence(signed=false) must return ErrEvidenceMustBeSigned, got %v", err)
	}
}

func TestNewEvidence_SignedTrue_OK(t *testing.T) {
	r, err := NewEvidence(true, sha256Hex("upstream-evidence"))
	if err != nil {
		t.Fatalf("NewEvidence(true, valid) should succeed, got %v", err)
	}
	if !r.Signed {
		t.Fatal("evidence must be Signed=true")
	}
	if r.Trimmable {
		t.Fatal("evidence must be Trimmable=false")
	}
	if r.Kind != KindEvidence {
		t.Fatalf("Kind=%q, want %q", r.Kind, KindEvidence)
	}
}

func TestNewEvidence_TrimmableForcedFalse(t *testing.T) {
	// Caller cannot smuggle Trimmable=true via constructor (Record is
	// not directly mutable from outside, but we sanity-check that
	// even a hand-rolled Trimmable=true record fails Validate).
	r := &Record{Kind: KindEvidence, Signed: true, Trimmable: true}
	if err := r.Validate(); !errors.Is(err, ErrEvidenceNotTrimmable) {
		t.Fatalf("hand-rolled evidence+trimmable must fail, got %v", err)
	}
}

// -------- TrimPolicy: events vs evidence vs unknown --------

func TestTrimPolicy_Event(t *testing.T) {
	p := TrimPolicy(KindEvent)
	if !p["trim"] || !p["sample"] || p["sign"] {
		t.Fatalf("event policy = %v, want trim:true sample:true sign:false", p)
	}
}

func TestTrimPolicy_Evidence(t *testing.T) {
	p := TrimPolicy(KindEvidence)
	if p["trim"] || p["sample"] || !p["sign"] {
		t.Fatalf("evidence policy = %v, want trim:false sample:false sign:true", p)
	}
}

func TestTrimPolicy_Unknown_FailClosed(t *testing.T) {
	p := TrimPolicy("logs")
	if p["trim"] || p["sample"] || p["sign"] {
		t.Fatalf("unknown kind policy = %v, want all false (fail-closed)", p)
	}
}

func TestTrimPolicy_ReturnsFreshMap(t *testing.T) {
	a := TrimPolicy(KindEvent)
	b := TrimPolicy(KindEvent)
	a["trim"] = false // mutate a
	if !b["trim"] {
		t.Fatal("TrimPolicy must return a fresh map per call (callers must not share state)")
	}
}

// -------- Verifier: Sign/Verify roundtrip + tamper + wrong-key --------

func mustVerifier(t *testing.T, key []byte) *Verifier {
	t.Helper()
	v, err := NewVerifier(key)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestVerifier_SignVerify_Roundtrip(t *testing.T) {
	v := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	r, err := NewEvidence(true, sha256Hex("upstream"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := v.Sign(r)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !v.Verify(r, sig) {
		t.Fatal("Verify must accept a fresh signature")
	}
}

func TestVerifier_Sign_DoesNotMutateRecord(t *testing.T) {
	v := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	r, err := NewEvidence(true, sha256Hex("upstream"))
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot via JSON — Record has no Signature field, but we also
	// snapshot a copy of the value to be paranoid.
	beforeJSON, _ := json.Marshal(r)
	beforeCopy := *r
	_, err = v.Sign(r)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	afterJSON, _ := json.Marshal(r)
	if !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("Sign mutated Record JSON:\nbefore=%s\nafter =%s", beforeJSON, afterJSON)
	}
	if beforeCopy != *r {
		t.Fatalf("Sign mutated Record fields: before=%+v after=%+v", beforeCopy, *r)
	}
}

func TestVerifier_Tamper_FlipBool_Fails(t *testing.T) {
	v := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	r, err := NewEvidence(true, sha256Hex("upstream"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := v.Sign(r)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Verify(r, sig) {
		t.Fatal("baseline must verify")
	}
	// Flip trimmable: now illegal (evidence must be non-trimmable)
	// AND signature will no longer match.
	r.Trimmable = true
	if v.Verify(r, sig) {
		t.Fatal("Verify must fail after flipping Trimmable to true")
	}
}

func TestVerifier_Tamper_FlipKind_Fails(t *testing.T) {
	v := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	r, err := NewEvidence(true, sha256Hex("upstream"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := v.Sign(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Kind = KindEvent // category flip
	if v.Verify(r, sig) {
		t.Fatal("Verify must fail after flipping Kind to event")
	}
}

func TestVerifier_WrongKey_Fails(t *testing.T) {
	v1 := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	v2 := mustVerifier(t, []byte("fedcba9876543210fedcba9876543210"))
	r, err := NewEvidence(true, sha256Hex("upstream"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := v1.Sign(r)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Verify(r, sig) {
		t.Fatal("Verify with wrong key must fail")
	}
}

func TestVerifier_BadSignature_Fails(t *testing.T) {
	v := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	r, err := NewEvidence(true, sha256Hex("upstream"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Verify(r, "not-base64") {
		t.Fatal("Verify with non-base64 sig must fail")
	}
	// Valid base64, wrong bytes.
	bad := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0xAA}, 32))
	if v.Verify(r, bad) {
		t.Fatal("Verify with wrong bytes must fail")
	}
}

func TestVerifier_RefusesInvalidRecord(t *testing.T) {
	v := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	r := &Record{Kind: KindEvidence, Signed: false, Trimmable: false}
	if _, err := v.Sign(r); !errors.Is(err, ErrEvidenceMustBeSigned) {
		t.Fatalf("Sign must refuse unsigned evidence, got %v", err)
	}
}

func TestVerifier_NilRecord(t *testing.T) {
	v := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	if _, err := v.Sign(nil); err == nil {
		t.Fatal("Sign(nil) must error")
	}
	if v.Verify(nil, "anything") {
		t.Fatal("Verify(nil, ...) must be false")
	}
}

func TestVerifier_Attested_Roundtrip(t *testing.T) {
	v := mustVerifier(t, []byte("0123456789abcdef0123456789abcdef"))
	r, err := NewEvidence(true, sha256Hex("upstream"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := v.Attest(r)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	if a.Record != r {
		t.Fatal("Attested must hold pointer to original Record")
	}
	if a.Signature == "" {
		t.Fatal("Attested.Signature must be populated")
	}
	if !v.Verify(a.Record, a.Signature) {
		t.Fatal("Attested must roundtrip through Verify")
	}
}

// -------- Canonical-JSON byte-stability --------

func TestCanonicalJSON_StableOrder(t *testing.T) {
	r, err := NewEvidence(true, sha256Hex("x"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := CanonicalJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical JSON not stable: %s vs %s", a, b)
	}
	// Keys must be alphabetical.
	s := string(a)
	if !strings.HasPrefix(s, `{"cites_hash":`) {
		t.Fatalf("expected cites_hash first (alphabetical), got %s", s)
	}
}

func TestCanonicalJSON_VerifierUsesSameShape(t *testing.T) {
	// Independently re-derive the expected canonical bytes and confirm
	// Verifier.Sign matches the same payload (via a fresh HMAC of the
	// manually-built bytes).
	key := []byte("0123456789abcdef0123456789abcdef")
	v, _ := NewVerifier(key)
	r, err := NewEvidence(true, sha256Hex("upstream"))
	if err != nil {
		t.Fatal(err)
	}
	canon, err := CanonicalJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	expectedMac := hmac.New(sha256.New, key)
	expectedMac.Write(canon)
	expectedSig := base64.RawStdEncoding.EncodeToString(expectedMac.Sum(nil))

	gotSig, err := v.Sign(r)
	if err != nil {
		t.Fatal(err)
	}
	if gotSig != expectedSig {
		t.Fatalf("Verifier.Sign does not match independent HMAC over CanonicalJSON:\n got=%s\nwant=%s", gotSig, expectedSig)
	}
}
