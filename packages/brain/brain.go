// Package brain implements the pure-domain kernel laws of the Company Brain
// content-addressed namespace (contract:brain.ns/1.0, ADR-0023 — "knowledge
// mounted, not prompted"). It is a leaf domain package: no HTTP, no storage,
// no service imports. The storage/mount layer above it may only construct
// objects through the laws below.
//
// Laws encoded (ADR-0023 §1–§7 + the central law):
//
//   - C1 (central law — authority requires a human): an agent-written object
//     can NEVER become authoritative without a human stamp. Mirrors the
//     frozen schema if/then: authoritative == true implies
//     promotion == "human_stamped", and a human_stamped promotion carries a
//     non-empty human_stamp. PromoteToAuthoritative is the ONLY constructor
//     of authority, and it demands humanID + note.
//   - L1 (§1, namespace): the five collections (missions|decisions|
//     capabilities|evidence|notes) are the only top-level sections; paths are
//     locked by the frozen regex ^/org/[a-f0-9-]+/(...)/[A-Za-z0-9_/-]+$
//     (hardened: no empty segments, no trailing slash).
//   - L2 (§2, no write without evidence): NewObject and NextRevision fail
//     closed on an empty evidence reference — ErrNoEvidence.
//   - L3 (§4, ephemeral is never law): class ephemeral can never become
//     authoritative (ErrEphemeralAuthority) and can only be revised while it
//     has not expired (expired is dead, no revival — ErrExpired).
//   - L4 (§5, append-only revisions): every revision is a NEW revision with
//     revision = prev+1, strictly monotonic, never an in-place edit. Because
//     a stamp binds to content, not to a path, NextRevision structurally
//     resets promotion/authority: an unstamped new revision is not law.
//   - L5 (§6, immutable = one revision ever): class immutable rejects any
//     second revision — ErrImmutable. The single allowed exception is the
//     construction-time human stamp on the one revision (see
//     PromoteToAuthoritative); it changes no content and bumps no revision.
//   - L6 (§7, tombstone): a tombstone is a NEW revision marking the object
//     dead; only mutable_with_revision objects can be tombstoned, a dead
//     object cannot be revised again (ErrTombstoned), and dead things are not
//     law (tombstone forces Authoritative=false; Validate rejects
//     tombstone && authoritative). Old revisions stay readable for audit —
//     this kernel never deletes.
//   - L7 (mount scoping): MatchMount is pure read-view scoping. A mount at
//     /org/<id>/decisions sees /org/<id>/decisions/** only, never crosses
//     org segments, and path-segment boundaries defeat prefix confusion
//     (/org/x/notes does NOT see /org/x/notessecret).
package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Object classes (brain.ns/1.0 frozen enum: class / object_class).
const (
	ClassImmutable = "immutable"
	ClassMutable   = "mutable_with_revision"
	ClassEphemeral = "ephemeral"
)

// Promotion states (brain.ns/1.0 frozen enum).
const (
	PromotionNone         = "none"
	PromotionHumanStamped = "human_stamped"
)

// pathPattern is the frozen brain.ns/1.0 path regex, verbatim.
var pathPattern = regexp.MustCompile(`^/org/[a-f0-9-]+/(missions|decisions|capabilities|evidence|notes)/[A-Za-z0-9_/-]+$`)

// mountPrefixPattern is the frozen grammar relaxed only so a mount may also
// name the collection itself ("/org/<id>/decisions") as its root, not only a
// deep path: /org/<id>/<collection>[/<tail>].
var mountPrefixPattern = regexp.MustCompile(`^/org/([a-f0-9-]+)/(missions|decisions|capabilities|evidence|notes)(/[A-Za-z0-9_/-]*)?$`)

// orgSegmentPattern extracts the org segment for exact-match comparison in
// MatchMount (L7: a mount never crosses orgs).
var orgSegmentPattern = regexp.MustCompile(`^/org/([a-f0-9-]+)/`)

// Law errors (fail-closed sentinels).
var (
	ErrNoEvidence         = errors.New("brain: no write without an evidence reference (ADR-0023 §2)")
	ErrImmutable          = errors.New("brain: immutable objects have exactly one revision (ADR-0023 §6)")
	ErrExpired            = errors.New("brain: ephemeral object is expired — dead, no revival (ADR-0023 §4)")
	ErrTombstoned         = errors.New("brain: object is tombstoned — dead, not law (ADR-0023 §7)")
	ErrBadPath            = errors.New("brain: path violates the brain.ns/1.0 namespace law (ADR-0023 §1)")
	ErrBadClass           = errors.New("brain: class must be immutable|mutable_with_revision|ephemeral")
	ErrNoHumanStamp       = errors.New("brain: authority without a human stamp is a law violation (brain.ns/1.0 if/then, ADR-0023 central law)")
	ErrEphemeralAuthority = errors.New("brain: ephemeral knowledge can never become authoritative (ADR-0023 §4)")
)

// Object is one revision of a brain.ns/1.0 namespace object. The content hash
// is the content address (L1: content-addressed namespace); ExpiresAt is used
// by the ephemeral class only.
type Object struct {
	Path          string         `json:"path"`
	Class         string         `json:"class"`
	Revision      int            `json:"revision"`
	Content       map[string]any `json:"content"`
	ContentHash   string         `json:"content_hash"`
	Authoritative bool           `json:"authoritative"`
	Promotion     string         `json:"promotion"`
	HumanStamp    string         `json:"human_stamp,omitempty"`
	Tombstone     bool           `json:"tombstone"`
	EvidenceRef   string         `json:"evidence_ref"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"` // ephemeral class only
}

// Validate enforces the frozen shape of contract:brain.ns/1.0 including the
// central law (authoritative ⇒ human_stamped ⇒ non-empty human_stamp).
func (o *Object) Validate() error {
	if o == nil {
		return errors.New("brain: object is required")
	}
	// L1: namespace path law (frozen regex + segment hardening).
	if !pathPattern.MatchString(o.Path) || !cleanSegments(o.Path) {
		return fmt.Errorf("%w: %q", ErrBadPath, o.Path)
	}
	switch o.Class {
	case ClassImmutable, ClassMutable, ClassEphemeral:
	default:
		return fmt.Errorf("%w: %q at %s", ErrBadClass, o.Class, o.Path)
	}
	switch o.Promotion {
	case PromotionNone, PromotionHumanStamped:
	default:
		return fmt.Errorf("brain: promotion must be none|human_stamped, got %q at %s", o.Promotion, o.Path)
	}
	if o.Revision < 1 {
		return fmt.Errorf("brain: revision must be >= 1, got %d at %s", o.Revision, o.Path)
	}
	if o.EvidenceRef == "" {
		return fmt.Errorf("%w: %s rev %d", ErrNoEvidence, o.Path, o.Revision)
	}
	// L1: content address must exist and must match its bytes.
	if o.Content == nil {
		return fmt.Errorf("brain: content is required at %s rev %d", o.Path, o.Revision)
	}
	want, err := ContentHashOf(o.Content)
	if err != nil {
		return err
	}
	if o.ContentHash == "" {
		return fmt.Errorf("brain: content_hash is required at %s rev %d", o.Path, o.Revision)
	}
	if o.ContentHash != want {
		return fmt.Errorf("brain: content_hash mismatch at %s rev %d: recorded %s, content hashes to %s",
			o.Path, o.Revision, o.ContentHash, want)
	}
	// C1: the central law — mirrors the schema if/then exactly, plus the
	// non-empty human_stamp obligation it implies.
	if o.Authoritative && o.Promotion != PromotionHumanStamped {
		return fmt.Errorf("%w: %s rev %d is authoritative but promotion is %q", ErrNoHumanStamp, o.Path, o.Revision, o.Promotion)
	}
	if o.Promotion == PromotionHumanStamped && o.HumanStamp == "" {
		return fmt.Errorf("%w: %s rev %d carries promotion human_stamped with an empty human_stamp", ErrNoHumanStamp, o.Path, o.Revision)
	}
	// L3: ephemeral can never be law.
	if o.Authoritative && o.Class == ClassEphemeral {
		return fmt.Errorf("%w: %s rev %d", ErrEphemeralAuthority, o.Path, o.Revision)
	}
	// L6: dead things are not law.
	if o.Tombstone && o.Authoritative {
		return fmt.Errorf("%w: %s rev %d is tombstoned and cannot be authoritative", ErrTombstoned, o.Path, o.Revision)
	}
	// ExpiresAt is reserved for the ephemeral class (frozen shape).
	if o.ExpiresAt != nil && o.Class != ClassEphemeral {
		return fmt.Errorf("brain: expires_at is ephemeral only, set on %s-class object %s", o.Class, o.Path)
	}
	if o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() {
		return fmt.Errorf("brain: created_at and updated_at are required at %s rev %d", o.Path, o.Revision)
	}
	if o.UpdatedAt.Before(o.CreatedAt) {
		return fmt.Errorf("brain: updated_at precedes created_at at %s rev %d", o.Path, o.Revision)
	}
	return nil
}

// cleanSegments hardens the frozen regex: no empty segments ("//"), no
// trailing slash, no dot-dot traversal (the regex already bans dots; kept
// explicit so the law survives any future loosening of the grammar).
func cleanSegments(p string) bool {
	if p == "" || strings.HasSuffix(p, "/") {
		return false
	}
	if strings.Contains(p, "//") || strings.Contains(p, "..") {
		return false
	}
	return true
}

// ContentHashOf returns the sha256 hex of the canonical JSON encoding of
// content. Canonicalisation: encoding/json marshals Go maps with
// lexicographically sorted keys, so equal content always hashes equal
// regardless of construction order (ADR-0023 §1, content-addressed).
//
// Hash lesson (agent-workforce): nil-valued keys must be rejected — a nil
// round-trips through JSON differently across layers (Go null vs a key that
// disappears) and would silently change the content address. Any nil value,
// nested in maps or slices, fails closed.
func ContentHashOf(content map[string]any) (string, error) {
	if content == nil {
		return "", errors.New("brain: content is required (nil map is not a content address)")
	}
	if err := rejectNilValues(content, "content"); err != nil {
		return "", err
	}
	b, err := json.Marshal(content) // sorted keys => canonical
	if err != nil {
		return "", fmt.Errorf("brain: content is not JSON round-trip safe: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// rejectNilValues walks JSON-container values and fails closed on any nil.
func rejectNilValues(v any, where string) error {
	switch t := v.(type) {
	case nil:
		return fmt.Errorf("brain: nil value at %s — it would silently change the content hash", where)
	case map[string]any:
		for k, val := range t {
			if err := rejectNilValues(val, where+"."+k); err != nil {
				return err
			}
		}
	case []any:
		for i, val := range t {
			if err := rejectNilValues(val, fmt.Sprintf("%s[%d]", where, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// NewObject writes the first revision (always Revision 1) of a new path.
//
// The create path structurally cannot produce authority: Authoritative=false
// and Promotion=none are hard-coded here — authority exists only through
// PromoteToAuthoritative (C1). Fails closed on an empty evidence reference
// (L2) and on any contract violation.
func NewObject(path, class string, content map[string]any, evidenceRef string, now time.Time) (*Object, error) {
	if evidenceRef == "" {
		return nil, fmt.Errorf("%w: refusing to create %s", ErrNoEvidence, path)
	}
	hash, err := ContentHashOf(content)
	if err != nil {
		return nil, err
	}
	o := &Object{
		Path:          path,
		Class:         class,
		Revision:      1,
		Content:       content,
		ContentHash:   hash,
		Authoritative: false,
		Promotion:     PromotionNone,
		Tombstone:     false,
		EvidenceRef:   evidenceRef,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	// Note: an ephemeral object is created with ExpiresAt unset; the writing
	// service sets it immediately (it is ephemeral-only by frozen shape).
	// NextRevision fails closed on an ephemeral without an expiry.
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return o, nil
}

// NextRevision appends a new revision to prev (L4: strictly monotonic, never
// in-place; CreatedAt carries the lineage birth forward, UpdatedAt is now).
//
// Law table (ADR-0023 §4–§7):
//   - immutable: ErrImmutable — one revision ever.
//   - ephemeral: ErrExpired unless now is strictly before prev.ExpiresAt;
//     a missing expiry also fails closed (expired is dead, no revival).
//   - mutable_with_revision: ErrTombstoned once prev is a tombstone.
//
// Every result is promotion=none and non-authoritative: a stamp binds to
// content, so new content is never law until re-promoted (C1, L4).
func NextRevision(prev *Object, content map[string]any, evidenceRef string, now time.Time) (*Object, error) {
	if prev == nil {
		return nil, errors.New("brain: prev object is required for a revision")
	}
	if err := prev.Validate(); err != nil {
		return nil, err
	}
	if evidenceRef == "" {
		return nil, fmt.Errorf("%w: refusing to revise %s", ErrNoEvidence, prev.Path)
	}
	switch prev.Class {
	case ClassImmutable:
		return nil, fmt.Errorf("%w: refusing revision %d of immutable %s", ErrImmutable, prev.Revision+1, prev.Path)
	case ClassEphemeral:
		if prev.ExpiresAt == nil || !now.Before(*prev.ExpiresAt) {
			return nil, fmt.Errorf("%w: %s", ErrExpired, prev.Path)
		}
	default: // ClassMutable
		if prev.Tombstone {
			return nil, fmt.Errorf("%w: refusing revision %d of dead %s", ErrTombstoned, prev.Revision+1, prev.Path)
		}
	}
	hash, err := ContentHashOf(content)
	if err != nil {
		return nil, err
	}
	next := &Object{
		Path:          prev.Path,
		Class:         prev.Class,
		Revision:      prev.Revision + 1,
		Content:       content,
		ContentHash:   hash,
		Authoritative: false,
		Promotion:     PromotionNone,
		Tombstone:     false,
		EvidenceRef:   evidenceRef,
		CreatedAt:     prev.CreatedAt,
		UpdatedAt:     now,
		ExpiresAt:     prev.ExpiresAt,
	}
	if err := next.Validate(); err != nil {
		return nil, err
	}
	return next, nil
}

// PromoteToAuthoritative is the ONLY constructor of authority (C1). It
// refuses ephemeral objects (L3), demands a non-empty humanID and note — the
// human decision record — and fails closed on already-dead objects.
//
//   - mutable_with_revision: produces a NEW revision via NextRevision (L4)
//     carrying the stamp; the promoted revision is the authoritative one.
//   - immutable: stamps the single revision in place on a COPY (L5's one
//     allowed construction-time exception): revision, content, and
//     content_hash are untouched — an immutable object's bytes can become
//     law exactly once, by exactly one human stamp, never by an agent edit.
//   - immutable already authoritative: refused (no re-promote; the stamp is
//     as unique as the revision).
//
// The note is a law input, not storage: brain.ns/1.0 has no note field, so
// the mounting/service layer MUST persist the note as evidence bound to the
// resulting revision.
func PromoteToAuthoritative(o *Object, humanID, note string, now time.Time) (*Object, error) {
	if o == nil {
		return nil, errors.New("brain: object is required for promotion")
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Class == ClassEphemeral {
		return nil, fmt.Errorf("%w: cannot promote %s", ErrEphemeralAuthority, o.Path)
	}
	if humanID == "" {
		return nil, fmt.Errorf("%w: promote of %s carries no human_id", ErrNoHumanStamp, o.Path)
	}
	if note == "" {
		return nil, fmt.Errorf("%w: promote of %s carries no note (the human decision must be recorded)", ErrNoHumanStamp, o.Path)
	}
	if o.Tombstone {
		return nil, fmt.Errorf("%w: cannot promote %s rev %d", ErrTombstoned, o.Path, o.Revision)
	}
	if o.Class == ClassImmutable {
		if o.Authoritative {
			return nil, fmt.Errorf("brain: immutable %s rev %d is already authoritative — no re-promote (ADR-0023 §6)", o.Path, o.Revision)
		}
		stamped := *o // copy: the caller's prev stays readable for audit (L6/§7 spirit)
		stamped.Authoritative = true
		stamped.Promotion = PromotionHumanStamped
		stamped.HumanStamp = humanID
		stamped.UpdatedAt = now
		if err := stamped.Validate(); err != nil {
			return nil, err
		}
		return &stamped, nil
	}
	next, err := NextRevision(o, o.Content, o.EvidenceRef, now)
	if err != nil {
		return nil, err
	}
	next.Authoritative = true
	next.Promotion = PromotionHumanStamped
	next.HumanStamp = humanID
	if err := next.Validate(); err != nil {
		return nil, err
	}
	return next, nil
}

// Tombstone appends a new revision marking the object dead (L6, ADR-0023 §7).
// Only mutable_with_revision objects qualify: immutable law is un-dying by
// definition (one revision ever) and ephemeral objects die by expiry. The
// tombstone revision is never authoritative — dead things are not law. Old
// revisions stay readable for audit; nothing here ever deletes.
func Tombstone(o *Object, evidenceRef string, now time.Time) (*Object, error) {
	if o == nil {
		return nil, errors.New("brain: object is required for a tombstone")
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	switch o.Class {
	case ClassImmutable:
		return nil, fmt.Errorf("%w: refusing to tombstone immutable %s", ErrImmutable, o.Path)
	case ClassEphemeral:
		return nil, fmt.Errorf("brain: only mutable_with_revision objects can be tombstoned; %s is ephemeral and dies by expiry (ADR-0023 §4, §7): %w", o.Path, ErrTombstoned)
	}
	if o.Tombstone {
		return nil, fmt.Errorf("%w: %s rev %d is already dead", ErrTombstoned, o.Path, o.Revision)
	}
	next, err := NextRevision(o, o.Content, evidenceRef, now)
	if err != nil {
		return nil, err
	}
	next.Tombstone = true
	next.Authoritative = false // dead things are not law — forced, even if prev was authoritative
	next.Promotion = PromotionNone
	next.HumanStamp = ""
	if err := next.Validate(); err != nil {
		return nil, err
	}
	return next, nil
}

// MatchMount answers: does the read-view mount at mountPrefix see the object
// at path? (L7 — mounts are READ views; this never gates writes, evidence
// does that.) Laws: the org segment must match exactly (a mount never crosses
// organizations); matching happens on whole path segments only, so a mount at
// /org/x/notes sees /org/x/notes/** but never /org/x/notessecret; malformed
// paths or prefixes on either side are simply not seen (fail closed).
func MatchMount(path, mountPrefix string) bool {
	pm := mountPrefixPattern.FindStringSubmatch(mountPrefix)
	if pm == nil || !cleanSegments(mountPrefix) {
		return false
	}
	if !pathPattern.MatchString(path) || !cleanSegments(path) {
		return false
	}
	om := orgSegmentPattern.FindStringSubmatch(path)
	if om == nil || om[1] != pm[1] {
		return false // never crosses orgs
	}
	if path == mountPrefix {
		return true
	}
	// Segment boundary: the prefix must be followed by "/" to contain the
	// path — this is what rejects /org/x/notes vs /org/x/notessecret.
	return strings.HasPrefix(path, mountPrefix+"/")
}
