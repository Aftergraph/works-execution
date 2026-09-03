package obslaw

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Verifier signs and verifies Records using HMAC-SHA256 over canonical JSON.
//
// The kernel uses this for the evidence side of the boundary: every
// evidence record flowing out of services/evidence/ is paired with a
// Verifier-produced Attested. Events do not pass through Sign/Verify
// (they are not claims), but events may be hashed by a content-addressed
// store; that store is not this package's concern.
//
// A later integration PR applies this Verifier to services/evidence
// bundles; this slice delivers the standalone law package only.
//
// Threat model: HMAC-SHA256 is sufficient for the V1 in-cluster trust
// boundary where the signer and verifier share Key. A future slice may
// swap in a real asymmetric attestation (Sigstore / KMS) — the wire
// shape Attested{Record, Signature} is forward-compatible with that
// because it carries Signature as an opaque string.
type Verifier struct {
	Key []byte
}

// NewVerifier is a tiny convenience for callers that want a non-nil
// Verifier with a minimum-length key. Length 0 keys are accepted (HMAC
// tolerates them) but rejected here for safety.
func NewVerifier(key []byte) (*Verifier, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("obslaw: verifier key must be at least 32 bytes, got %d", len(key))
	}
	return &Verifier{Key: append([]byte(nil), key...)}, nil
}

// Sign produces a base64-encoded HMAC-SHA256 signature over the canonical
// JSON of r. It does NOT mutate r — the signature lives in Attested, not
// on Record (see Attested doc). The canonicalisation is the same shape
// used elsewhere in this repo (RFC 8785 spirit: sort object keys at every
// level, no Unicode escapes).
//
// Returns ErrInvalidRecord if r fails Validate — the law refuses to
// attest an illegal record.
func (v *Verifier) Sign(r *Record) (string, error) {
	if r == nil {
		return "", errors.New("obslaw: cannot sign nil record")
	}
	if err := r.Validate(); err != nil {
		return "", fmt.Errorf("obslaw: refusing to sign invalid record: %w", err)
	}
	if len(v.Key) == 0 {
		return "", errors.New("obslaw: verifier key is empty")
	}
	canon, err := canonicalizeRecord(r)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, v.Key)
	mac.Write(canon)
	sum := mac.Sum(nil)
	return base64.RawStdEncoding.EncodeToString(sum), nil
}

// Verify reports whether sig is a valid HMAC-SHA256 signature over the
// canonical JSON of r, using v.Key. It is constant-time over the
// signature bytes (subtle.ConstantTimeCompare) and returns false for any
// precondition failure (nil r, nil/empty key, invalid record,
// non-canonical JSON).
func (v *Verifier) Verify(r *Record, sig string) bool {
	if r == nil || sig == "" {
		return false
	}
	if len(v.Key) == 0 {
		return false
	}
	if err := r.Validate(); err != nil {
		return false
	}
	canon, err := canonicalizeRecord(r)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, v.Key)
	mac.Write(canon)
	want := mac.Sum(nil)
	got, err := base64.RawStdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(want, got) == 1
}

// Attest is Sign+wrap: produces the Attested pair (Record, Signature).
// Attested is what downstream code stores/transmits.
func (v *Verifier) Attest(r *Record) (*Attested, error) {
	sig, err := v.Sign(r)
	if err != nil {
		return nil, err
	}
	return &Attested{Record: r, Signature: sig}, nil
}

// canonicalizeRecord renders r as canonical JSON: object keys sorted
// lexicographically at the top level, no indentation, no HTML escaping.
// This matches the spirit of RFC 8785 and is consistent with the
// canonicaliser used in services/evidence/bundle.go.
func canonicalizeRecord(r *Record) ([]byte, error) {
	// Mirror the JSON shape: kind, signed, trimmable, cites_hash.
	// We hand-roll rather than reflect over struct tags so the wire
	// shape is independent of struct-tag mistakes and field reorders.
	m := map[string]any{
		"kind":       r.Kind,
		"signed":     r.Signed,
		"trimmable":  r.Trimmable,
		"cites_hash": r.CitesHash,
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(m[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// CanonicalJSON exposes the canonical-JSON rendering for a record so
// callers (e.g. tests, debug dumps) can verify byte-stability.
func CanonicalJSON(r *Record) ([]byte, error) {
	return canonicalizeRecord(r)
}

// sha256Sum is exported only for tests that want to check the canonical
// payload is stable across calls. Kept unexported in spirit; the constant
// is here to suppress an unused-import warning if Go's staticcheck
// complains about crypto/sha256 being only referenced transitively.
var _ = sha256.New
