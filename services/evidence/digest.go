package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/zeebo/blake3"
)

// DigestAlgorithm identifies a cryptographic content digest algorithm.
// SHA-256 remains the interoperability baseline; BLAKE3 is the
// high-throughput alternate used by Integrity Fabric v1.
type DigestAlgorithm string

const (
	DigestSHA256 DigestAlgorithm = "sha256"
	DigestBLAKE3 DigestAlgorithm = "blake3"
)

// DigestScope states what was hashed. Scope is explicit so callers cannot
// accidentally compare a raw-byte digest with a canonical-object digest.
type DigestScope string

const (
	DigestScopeRawBytes        DigestScope = "raw-bytes"
	DigestScopeCanonicalObject DigestScope = "canonical-object"
	DigestScopeMerkleRoot      DigestScope = "merkle-root"
)

// DigestRef is an algorithm-tagged, scope-tagged digest reference.
// The wire representation deliberately does not infer algorithm from length.
type DigestRef struct {
	Algorithm DigestAlgorithm `json:"algorithm"`
	Scope     DigestScope     `json:"scope"`
	Encoding  string          `json:"encoding"`
	Value     string          `json:"value"`
}

// DigestSet carries one interoperability-primary digest plus zero or more
// alternate digests over the exact same subject bytes. This mirrors the
// algorithm-agility principle used by in-toto DigestSet while retaining an
// explicit primary for legacy consumers.
type DigestSet struct {
	Primary      DigestRef   `json:"primary"`
	Alternatives []DigestRef `json:"alternatives,omitempty"`
}

// IntegrityEnvelope binds the canonicalization contract to a digest set.
// It is intentionally separate from signatures/provenance: a digest proves
// byte identity, not actor identity or verified outcome.
type IntegrityEnvelope struct {
	Canonicalization string    `json:"canonicalization"`
	SubjectScope     DigestScope `json:"subject_scope"`
	Digests          DigestSet `json:"digests"`
}

const BundleCanonicalizationV1 = "aftergraph-json-canonical/1"

var (
	ErrUnsupportedDigestAlgorithm = errors.New("unsupported digest algorithm")
	ErrDigestMismatch              = errors.New("digest mismatch")
	ErrInvalidDigest               = errors.New("invalid digest")
)

func digestHex(data []byte, algorithm DigestAlgorithm) (string, error) {
	switch algorithm {
	case DigestSHA256:
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:]), nil
	case DigestBLAKE3:
		sum := blake3.Sum256(data)
		return hex.EncodeToString(sum[:]), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedDigestAlgorithm, algorithm)
	}
}

// DigestBytes hashes data and returns an explicit digest reference.
func DigestBytes(data []byte, algorithm DigestAlgorithm, scope DigestScope) (DigestRef, error) {
	if scope == "" {
		return DigestRef{}, fmt.Errorf("%w: scope is required", ErrInvalidDigest)
	}
	value, err := digestHex(data, algorithm)
	if err != nil {
		return DigestRef{}, err
	}
	return DigestRef{
		Algorithm: algorithm,
		Scope:     scope,
		Encoding:  "hex",
		Value:     value,
	}, nil
}

// BuildDigestSet computes the Integrity Fabric v1 default set:
// SHA-256 primary for interoperability plus BLAKE3-256 as the native
// high-throughput alternate.
func BuildDigestSet(data []byte, scope DigestScope) (DigestSet, error) {
	primary, err := DigestBytes(data, DigestSHA256, scope)
	if err != nil {
		return DigestSet{}, err
	}
	alternate, err := DigestBytes(data, DigestBLAKE3, scope)
	if err != nil {
		return DigestSet{}, err
	}
	return DigestSet{
		Primary:      primary,
		Alternatives: []DigestRef{alternate},
	}, nil
}

func validateDigestRef(ref DigestRef) error {
	if ref.Algorithm != DigestSHA256 && ref.Algorithm != DigestBLAKE3 {
		return fmt.Errorf("%w: algorithm %q", ErrInvalidDigest, ref.Algorithm)
	}
	switch ref.Scope {
	case DigestScopeRawBytes, DigestScopeCanonicalObject, DigestScopeMerkleRoot:
	default:
		return fmt.Errorf("%w: scope %q", ErrInvalidDigest, ref.Scope)
	}
	if ref.Encoding != "hex" {
		return fmt.Errorf("%w: encoding %q", ErrInvalidDigest, ref.Encoding)
	}
	if len(ref.Value) != 64 || strings.ToLower(ref.Value) != ref.Value {
		return fmt.Errorf("%w: expected 64 lowercase hex characters", ErrInvalidDigest)
	}
	if _, err := hex.DecodeString(ref.Value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDigest, err)
	}
	return nil
}

// VerifyDigest recomputes a digest over data and requires exact algorithm,
// scope, encoding, and value agreement.
func VerifyDigest(data []byte, ref DigestRef) error {
	if err := validateDigestRef(ref); err != nil {
		return err
	}
	expected, err := DigestBytes(data, ref.Algorithm, ref.Scope)
	if err != nil {
		return err
	}
	if expected.Value != ref.Value {
		return ErrDigestMismatch
	}
	return nil
}

// VerifyDigestSet requires every member to refer to the same scope and to
// verify against the supplied bytes. Duplicate algorithms are rejected.
func VerifyDigestSet(data []byte, set DigestSet) error {
	if err := validateDigestRef(set.Primary); err != nil {
		return err
	}
	if err := VerifyDigest(data, set.Primary); err != nil {
		return err
	}
	seen := map[DigestAlgorithm]struct{}{set.Primary.Algorithm: {}}
	for _, ref := range set.Alternatives {
		if ref.Scope != set.Primary.Scope {
			return fmt.Errorf("%w: mixed scopes", ErrInvalidDigest)
		}
		if _, ok := seen[ref.Algorithm]; ok {
			return fmt.Errorf("%w: duplicate algorithm %q", ErrInvalidDigest, ref.Algorithm)
		}
		seen[ref.Algorithm] = struct{}{}
		if err := VerifyDigest(data, ref); err != nil {
			return err
		}
	}
	return nil
}
