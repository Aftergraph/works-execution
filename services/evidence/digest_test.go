package evidence

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildDigestSet_DefaultAlgorithms(t *testing.T) {
	set, err := BuildDigestSet([]byte("aftergraph"), DigestScopeRawBytes)
	if err != nil {
		t.Fatalf("BuildDigestSet: %v", err)
	}
	if set.Primary.Algorithm != DigestSHA256 {
		t.Fatalf("primary algorithm = %q, want sha256", set.Primary.Algorithm)
	}
	if len(set.Alternatives) != 1 || set.Alternatives[0].Algorithm != DigestBLAKE3 {
		t.Fatalf("alternatives = %#v, want one blake3 digest", set.Alternatives)
	}
	if err := VerifyDigestSet([]byte("aftergraph"), set); err != nil {
		t.Fatalf("VerifyDigestSet: %v", err)
	}
}

func TestVerifyDigestSet_TamperFails(t *testing.T) {
	set, err := BuildDigestSet([]byte("original"), DigestScopeCanonicalObject)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyDigestSet([]byte("tampered"), set); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("got %v, want ErrDigestMismatch", err)
	}
}

func TestVerifyDigestSet_RejectsMixedScopes(t *testing.T) {
	set, err := BuildDigestSet([]byte("x"), DigestScopeRawBytes)
	if err != nil {
		t.Fatal(err)
	}
	set.Alternatives[0].Scope = DigestScopeMerkleRoot
	if err := VerifyDigestSet([]byte("x"), set); err == nil {
		t.Fatal("expected mixed scope rejection")
	}
}

func TestVerifyDigest_RejectsImplicitAlgorithm(t *testing.T) {
	ref := DigestRef{
		Algorithm: "",
		Scope: DigestScopeRawBytes,
		Encoding: "hex",
		Value: strings.Repeat("0", 64),
	}
	if err := VerifyDigest([]byte("x"), ref); err == nil {
		t.Fatal("expected unsupported/implicit algorithm rejection")
	}
}
