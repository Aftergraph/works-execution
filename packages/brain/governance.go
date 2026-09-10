package brain

// WORKS Brain governance seam (V4 memory-acc-brain separation binding,
// docs/MEMORY-ACC-BRAIN-V1.md in after-graph-governance).
//
// The Brain holds durable organizational knowledge ONLY — settled evidence,
// quittance, promoted know-how — under the human-stamped promotion law. It
// never absorbs live personal context and never substitutes for mission
// authority (Memory != Authority).
//
// This file is the seam, not the store: Admit is the one gate every write
// path must pass before constructing a brain Object. The promotion
// mechanics themselves stay in brain.go (PromoteToAuthoritative is still
// the ONLY constructor of authority); the tests pin that law here so the
// binding gates it too. Service/HTTP wiring is integrator work.
//
// The marker-key tripwires below are shape guards, not semantic
// detectors: they catch laundered claim keys at any depth of the content
// tree at the seam. The kind + evidence gate above them is the law.

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// KnowledgeKind declares what the caller claims the content is. The set is
// closed: only durable organizational knowledge admits. Anything else —
// live personal context, predictions, capsule deltas, authority material —
// fails closed with ErrIneligibleKind.
type KnowledgeKind string

const (
	KindSettledEvidence KnowledgeKind = "settled_evidence"
	KindQuittance       KnowledgeKind = "quittance"
	KindPromotedKnowHow KnowledgeKind = "promoted_know_how"
)

// Governance seam errors (fail-closed sentinels).
var (
	ErrIneligibleKind        = errors.New("brain: kind is not durable organizational knowledge (settled_evidence|quittance|promoted_know_how)")
	ErrPersonalContext       = errors.New("brain: live personal context is never absorbed into the Brain")
	ErrAuthoritySubstitution = errors.New("brain: the Brain confers no mission authority (Memory != Authority)")
	ErrQuittanceForgery      = errors.New("brain: quittance-kind admission without quittance evidence is forgery")
	ErrEvidenceShape         = errors.New("brain: evidence reference must have the shape evidence:<layer>/<id>")
)

// personalMarkers are content keys (matched at any depth, any case) that
// carry live personal context. Runtime-scoped working state (live/session
// context) belongs to Runtime Memory for the live execution only — it
// never persists as Brain truth (binding, Runtime Memory row).
var personalMarkers = map[string]struct{}{
	"live_personal_context": {},
	"personal_context":      {},
	"live_context":          {},
	"session_context":       {},
}

// authorityMarkers are content keys (matched at any depth, any case) that claim mission authority.
// Authority truth lives in AIE grants, enforced by trust-gateway; the Brain
// renders and remembers, it never decides what a principal may do
// (HUMAN-GOVERNANCE-V1: delegation, mission envelopes, grants; binding
// invariant Memory != Authority).
var authorityMarkers = map[string]struct{}{
	"authority":         {},
	"grant":             {},
	"grants":            {},
	"mission_authority": {},
	"mission_envelope":  {},
	"delegation":        {},
	"permission":        {},
	"permissions":       {},
	"consent":           {},
	"approval":          {},
}

// evidenceSchemePrefix is the scheme every cited evidence reference must
// carry; the canonical in-repo shape is evidence:<layer>/<id> (e.g.
// evidence:bundle/b-777, evidence:quittance/q-001).
const evidenceSchemePrefix = "evidence:"

// quittanceLayer is the evidence layer the mission-receipt layer writes
// (services/evidence: quittance is the mission receipt). The quittance
// kind admits only this layer — the tightened form of the old
// evidence:quittance/ prefix check, which also let an empty id through.
const quittanceLayer = "quittance"

// parseEvidenceRef splits a canonical evidence reference of the shape
// evidence:<layer>/<id>, requiring a non-empty layer and a non-empty id.
// It reports shape only: whether the cited receipt exists is verified by
// the receipt (services/evidence) layer, never by the Brain.
func parseEvidenceRef(ref string) (layer, id string, ok bool) {
	rest, found := strings.CutPrefix(ref, evidenceSchemePrefix)
	if !found {
		return "", "", false
	}
	layer, id, found = strings.Cut(rest, "/")
	if !found || layer == "" || id == "" {
		return "", "", false
	}
	return layer, id, true
}

// Admission is one claimed write to the Brain: what the content is said to
// be, the content itself, and the settled evidence backing it.
type Admission struct {
	Kind        KnowledgeKind
	Content     map[string]any
	EvidenceRef string
}

// Admit gates one admission against the separation binding. Fail-closed:
// the first violated law wins and nothing is constructed.
func Admit(a Admission) error {
	if a.Content == nil {
		return errors.New("brain: admission content is required (nil map admits nothing)")
	}
	switch a.Kind {
	case KindSettledEvidence, KindQuittance, KindPromotedKnowHow:
	default:
		return fmt.Errorf("%w: got %q", ErrIneligibleKind, string(a.Kind))
	}
	if a.EvidenceRef == "" {
		return fmt.Errorf("%w: refusing %s admission", ErrNoEvidence, string(a.Kind))
	}
	// Shape gate (all kinds): bare tokens ("garbage"), scheme-less paths,
	// and empty layers/ids ("evidence:quittance/") deny here — the seam
	// cannot cite what it cannot name. Residual: shape is cited, not
	// verified — existence-of-receipt verification stays with the
	// receipt (services/evidence) layer; the Brain never substitutes
	// for it (Memory != Authority).
	layer, _, ok := parseEvidenceRef(a.EvidenceRef)
	if !ok {
		return fmt.Errorf("%w: refusing %s admission citing %q", ErrEvidenceShape, string(a.Kind), a.EvidenceRef)
	}
	if a.Kind == KindQuittance && layer != quittanceLayer {
		return fmt.Errorf("%w: quittance admission cites %q", ErrQuittanceForgery, a.EvidenceRef)
	}
	// Marker tripwires scan the whole content tree, not just the top
	// level: a claim key nested inside a map, slice, array, or struct
	// smuggles the same live context / authority material past a
	// top-level-only check, so every string-kind map key and every
	// struct field name at any depth (inside slices/arrays too,
	// through pointers/interfaces, whatever the concrete Go type) is
	// matched case-insensitively against the marker sets. The scan is
	// deterministic (sorted keys): the first personal-context violation
	// wins, else the first authority violation, each reported with its
	// dot/bracket path (e.g. note.grant, items[0].delegation). Values
	// (prose) never trip the seam — only keys claim.
	if path, key, ok := scanMarkers(a.Content, "", personalMarkers); ok {
		return fmt.Errorf("%w: key %q at %s", ErrPersonalContext, key, path)
	}
	if path, key, ok := scanMarkers(a.Content, "", authorityMarkers); ok {
		return fmt.Errorf("%w: key %q at %s", ErrAuthoritySubstitution, key, path)
	}
	return nil
}

// scanMarkers depth-first searches content for the first key (matched
// case-insensitively) present in markers, walking the full concrete
// value tree via reflection: maps with string-kind keys (map[string]any,
// map[string]string, map[string]int, named key types, ...), slices and
// arrays of any element type, structs (field names), through interfaces
// and pointers. It returns the violating key's path, the key as written,
// and true on a hit. Sibling map keys visit in sorted (lower-cased)
// order so the winner is deterministic.
func scanMarkers(v any, path string, markers map[string]struct{}) (string, string, bool) {
	return scanValue(reflect.ValueOf(v), path, markers, make(map[visit]bool))
}

// visit identifies one reference-cycle ancestor. Slices fold their
// length in (n >= 0); maps and pointers leave n at -1 so a resliced
// view sharing one backing array never shadows a different length.
type visit struct {
	typ reflect.Type
	ptr uintptr
	n   int
}

// scanValue is the reflection core behind scanMarkers. Only map keys of
// string kind and struct field names claim — string/scalar values
// (prose) never trip the seam. seen tracks the ancestor chain only
// (insert on descend, delete on return) so aliased-but-acyclic values
// are fully walked while true reference cycles terminate.
func scanValue(rv reflect.Value, path string, markers map[string]struct{}, seen map[visit]bool) (string, string, bool) {
	if !rv.IsValid() {
		return "", "", false
	}
	switch rv.Kind() {
	case reflect.Interface, reflect.Pointer:
		if rv.IsNil() {
			return "", "", false
		}
		if rv.Kind() == reflect.Pointer {
			key := visit{typ: rv.Type(), ptr: rv.Pointer(), n: -1}
			if seen[key] {
				return "", "", false
			}
			seen[key] = true
			defer delete(seen, key)
		}
		return scanValue(rv.Elem(), path, markers, seen)
	case reflect.Map:
		if rv.IsNil() {
			return "", "", false
		}
		key := visit{typ: rv.Type(), ptr: rv.Pointer(), n: -1}
		if seen[key] {
			return "", "", false
		}
		seen[key] = true
		defer delete(seen, key)
		if rv.Type().Key().Kind() != reflect.String {
			// Non-string keys cannot claim, but values may nest
			// claim-carrying containers — still descend.
			for _, k := range rv.MapKeys() {
				p := fmt.Sprintf("%s[%v]", path, k.Interface())
				if hit, name, ok := scanValue(rv.MapIndex(k), p, markers, seen); ok {
					return hit, name, true
				}
			}
			return "", "", false
		}
		for _, k := range sortedReflectKeys(rv) {
			name := k.String()
			p := name
			if path != "" {
				p = path + "." + name
			}
			if _, ok := markers[strings.ToLower(name)]; ok {
				return p, name, true
			}
			if hit, found, ok := scanValue(rv.MapIndex(k), p, markers, seen); ok {
				return hit, found, true
			}
		}
		return "", "", false
	case reflect.Slice:
		if rv.IsNil() {
			return "", "", false
		}
		return scanSlice(rv, path, markers, seen)
	case reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if hit, name, ok := scanValue(rv.Index(i), fmt.Sprintf("%s[%d]", path, i), markers, seen); ok {
				return hit, name, true
			}
		}
		return "", "", false
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			name := t.Field(i).Name
			p := name
			if path != "" {
				p = path + "." + name
			}
			if _, ok := markers[strings.ToLower(name)]; ok {
				return p, name, true
			}
			if !t.Field(i).IsExported() {
				continue
			}
			if hit, found, ok := scanValue(rv.Field(i), p, markers, seen); ok {
				return hit, found, true
			}
		}
		return "", "", false
	default:
		return "", "", false
	}
}

// scanSlice walks every slice element with ancestor-scoped cycle
// protection keyed by (type, first-element pointer, length), so
// resliced views sharing one backing array still walk fully.
func scanSlice(rv reflect.Value, path string, markers map[string]struct{}, seen map[visit]bool) (string, string, bool) {
	probe := visit{typ: rv.Type(), ptr: rv.Pointer(), n: rv.Len()}
	if seen[probe] {
		return "", "", false
	}
	seen[probe] = true
	defer delete(seen, probe)
	for i := 0; i < rv.Len(); i++ {
		if hit, name, ok := scanValue(rv.Index(i), fmt.Sprintf("%s[%d]", path, i), markers, seen); ok {
			return hit, name, true
		}
	}
	return "", "", false
}

// sortedReflectKeys orders string-kind map keys by lower-cased form
// (raw key breaks ties) so traversal order is deterministic.
func sortedReflectKeys(rv reflect.Value) []reflect.Value {
	keys := rv.MapKeys()
	sort.Slice(keys, func(i, j int) bool {
		li, lj := strings.ToLower(keys[i].String()), strings.ToLower(keys[j].String())
		if li != lj {
			return li < lj
		}
		return keys[i].String() < keys[j].String()
	})
	return keys
}
