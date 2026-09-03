package obslaw

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
)

// Kind enumerates the two categories the law recognises. Anything outside
// this set is rejected fail-closed by Validate.
const (
	KindEvent    = "event"
	KindEvidence = "evidence"
)

// Sentinel errors returned by Validate and the constructors. Callers can
// errors.Is on these to discriminate between category errors.
var (
	// ErrUnknownKind is returned when Kind is neither "event" nor "evidence".
	// The schema is fail-closed on the kind enum.
	ErrUnknownKind = errors.New("obslaw: unknown kind; must be event or evidence")

	// ErrEvidenceMustBeSigned is returned when an evidence record carries
	// signed=false. An unsigned object cannot attest to truth.
	ErrEvidenceMustBeSigned = errors.New("obslaw: evidence must be signed (signed=true)")

	// ErrEvidenceNotTrimmable is returned when an evidence record carries
	// trimmable=true. Deleting evidence is tampering, not hygiene.
	ErrEvidenceNotTrimmable = errors.New("obslaw: evidence must not be trimmable (trimmable=false)")

	// ErrEventCannotBeSigned is returned when an event record carries
	// signed=true. Observability that pretends to attest is a category
	// error: the boundary exists precisely so a signed event cannot be
	// confused with a real claim about what happened.
	ErrEventCannotBeSigned = errors.New("obslaw: event cannot be signed (signed=false)")

	// ErrCitesHashFormat is returned when CitesHash is non-empty but is
	// not exactly 64 lowercase/uppercase hex characters. A malformed
	// hash pointer would silently break hash-chain stitches downstream.
	ErrCitesHashFormat = errors.New("obslaw: cites_hash must be 64 hex characters when set")
)

// citesHashLen is the expected length of a sha256 hex digest.
const citesHashLen = 64

// Record is the frozen schema shape for a single observability/evidence
// unit. It deliberately carries NO signature field: Record is the schema
// shape, and adding an extra property would violate
// additionalProperties:false at the JSON-schema level. Signatures live
// beside the record in Attested.
//
// Field semantics:
//
//	Kind       — "event" or "evidence" (the schema enum).
//	Signed     — true iff an attestation exists (evidence only).
//	Trimmable  — true iff the record may be discarded by retention
//	             policy (events only; evidence is forever).
//	CitesHash  — optional 64-hex sha256 of a referenced record. For
//	             evidence citing other evidence this is a hash-chain
//	             stitch (integrity-bearing). Events may also carry a
//	             cites_hash; it is documented as informational in that
//	             case (events are not attestations, so the pointer is
//	             a helpful trace link, not a trust claim).
type Record struct {
	Kind      string
	Signed    bool
	Trimmable bool
	CitesHash string
}

// Validate enforces the schema's allOf if/then branches against r.
// It returns nil iff r is a legal member of the obs.evidence.rules/1.0
// schema.
//
// Branch table (mirrors contracts/schemas/obs.evidence.rules.schema.json):
//
//	kind=evidence -> signed MUST be true AND trimmable MUST be false
//	kind=event    -> signed MUST be false (trimmable unconstrained by schema;
//	                 we accept either; events are by convention trimmable=true
//	                 but the schema does not pin that — see TrimPolicy)
//	kind=<other>  -> ErrUnknownKind (fail-closed)
//
// CitesHash, when non-empty, MUST be exactly 64 hex characters.
func (r *Record) Validate() error {
	switch r.Kind {
	case KindEvidence:
		if !r.Signed {
			return ErrEvidenceMustBeSigned
		}
		if r.Trimmable {
			return ErrEvidenceNotTrimmable
		}
	case KindEvent:
		if r.Signed {
			return ErrEventCannotBeSigned
		}
	default:
		return fmt.Errorf("%w: got %q", ErrUnknownKind, r.Kind)
	}
	if r.CitesHash != "" {
		if len(r.CitesHash) != citesHashLen {
			return fmt.Errorf("%w: got length %d, want %d", ErrCitesHashFormat, len(r.CitesHash), citesHashLen)
		}
		if _, err := hex.DecodeString(r.CitesHash); err != nil {
			return fmt.Errorf("%w: %v", ErrCitesHashFormat, err)
		}
	}
	return nil
}

// NewEvent constructs an observability record. Events are unsigned (the
// law: signed=false is part of the event contract) and trimmable by
// convention (the schema leaves trimmable unconstrained for events, but
// events are by definition operationally disposable). CitesHash is optional
// — informational when set on an event, not a trust claim.
func NewEvent(citesHash string) (*Record, error) {
	r := &Record{
		Kind:      KindEvent,
		Signed:    false,
		Trimmable: true,
		CitesHash: citesHash,
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// NewEvidence constructs an evidence record. signed=false is REFUSED at
// construction with ErrEvidenceMustBeSigned — you cannot accidentally
// build an evidence-shaped lie. trimmable is forced false regardless of
// any future caller intent, because evidence cannot be trimmed. The
// signed parameter is kept on the signature for symmetry with NewEvent
// and to make the contract visible at the call site, but its only legal
// value is true.
func NewEvidence(signed bool, citesHash string) (*Record, error) {
	if !signed {
		return nil, ErrEvidenceMustBeSigned
	}
	r := &Record{
		Kind:      KindEvidence,
		Signed:    true,
		Trimmable: false,
		CitesHash: citesHash,
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// TrimPolicy is the policy the law projects for a given kind. It answers
// the question "may this kind be trimmed / sampled / signed?" so runbooks,
// UIs, and downstream code can ask the law instead of hard-coding the
// matrix.
//
// The returned map is a fresh map per call so callers may mutate it
// freely (handy for tests; cheap to allocate).
func TrimPolicy(kind string) map[string]bool {
	switch kind {
	case KindEvent:
		return map[string]bool{
			"trim":   true,
			"sample": true,
			"sign":   false,
		}
	case KindEvidence:
		return map[string]bool{
			"trim":   false,
			"sample": false,
			"sign":   true,
		}
	default:
		// Unknown kind: fail-closed. Nothing is permitted.
		return map[string]bool{
			"trim":   false,
			"sample": false,
			"sign":   false,
		}
	}
}

// Attested binds a Record to its signature. Signatures live beside the
// record, not as a field on Record, because Record is the frozen schema
// shape and an extra Signature field would violate
// additionalProperties:false.
type Attested struct {
	Record    *Record
	Signature string
}

// Constant-time comparison guard. Imported here once so tests can also
// reference it if they want to confirm we're not using bytes.Equal.
var _ = subtle.ConstantTimeCompare
