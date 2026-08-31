// Signer signs and verifies attestation envelopes.

package provenance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Signer signs canonical attestation bytes with an HMAC-SHA256 key.
// KeyID is embedded alongside the signature so verifiers can pick the
// right key when the builder rotates its signing material.
type Signer struct {
	Key     []byte
	KeyID   string
}

// NewSigner constructs a Signer. key must be non-empty; otherwise we refuse
// to initialize because an empty key would silently sign with an empty MAC.
func NewSigner(key []byte, keyID string) (*Signer, error) {
	if len(key) == 0 {
		return nil, errors.New("provenance: signer key must not be empty")
	}
	return &Signer{Key: key, KeyID: keyID}, nil
}

// Sign returns the hex-encoded HMAC-SHA256 of the canonical attestation
// bytes. The same envelope + same key always yields the same signature;
// consumers verify by recomputing the canonical bytes and the HMAC.
func (s *Signer) Sign(envelope []byte) (string, error) {
	if s == nil {
		return "", errors.New("provenance: nil signer")
	}
	if len(envelope) == 0 {
		return "", errors.New("provenance: empty envelope")
	}
	mac := hmac.New(sha256.New, s.Key)
	mac.Write(envelope)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Verify returns nil iff sig is the hex-encoded HMAC of envelope under s.Key.
// Constant-time comparison guards against timing oracles on the verifier.
func (s *Signer) Verify(envelope []byte, sig string) error {
	if s == nil {
		return errors.New("provenance: nil signer")
	}
	want, err := s.Sign(envelope)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return errors.New("provenance: signature mismatch")
	}
	return nil
}