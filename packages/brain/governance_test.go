package brain

// Governance seam tests (V4 memory-acc-brain separation binding,
// docs/MEMORY-ACC-BRAIN-V1.md in after-graph-governance).
//
// The Brain holds durable organizational knowledge ONLY — settled evidence,
// quittance, promoted know-how — under the human-stamped promotion law. It
// never absorbs live personal context and never substitutes for mission
// authority. These tests pin that seam; governance.go is the canonical
// owner. Service/HTTP wiring is integrator work (same split as obslaw).

import (
	"errors"
	"strings"
	"testing"
)

func govContent() map[string]any {
	return map[string]any{"body": "settled: we ship on tuesday"}
}

// --- G1: durable organizational knowledge only ------------------------------

func TestAdmit_AcceptsDurableKinds(t *testing.T) {
	cases := []struct {
		name string
		adm  Admission
	}{
		{"settled evidence", Admission{Kind: KindSettledEvidence, Content: govContent(), EvidenceRef: "evidence:bundle/b-777"}},
		{"quittance", Admission{Kind: KindQuittance, Content: govContent(), EvidenceRef: "evidence:quittance/q-001"}},
		{"promoted know-how", Admission{Kind: KindPromotedKnowHow, Content: govContent(), EvidenceRef: "evidence:bundle/b-777"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Admit(tc.adm); err != nil {
				t.Fatalf("Admit(%s) = %v, want nil", tc.name, err)
			}
		})
	}
}

func TestAdmit_RejectsIneligibleKinds(t *testing.T) {
	for _, kind := range []KnowledgeKind{"", "live_personal_context", "prediction", "mission_grant", "capsule_delta", "observation"} {
		t.Run("kind="+string(kind), func(t *testing.T) {
			err := Admit(Admission{Kind: kind, Content: govContent(), EvidenceRef: "evidence:bundle/b-777"})
			if !errors.Is(err, ErrIneligibleKind) {
				t.Fatalf("Admit(kind=%q) = %v, want ErrIneligibleKind", kind, err)
			}
		})
	}
}

// --- G3: never absorbs live personal context --------------------------------

func TestAdmit_RejectsLivePersonalContext(t *testing.T) {
	for _, key := range []string{"live_personal_context", "personal_context", "live_context", "session_context"} {
		t.Run("key="+key, func(t *testing.T) {
			content := govContent()
			content[key] = map[string]any{"mood": "tired"}
			err := Admit(Admission{Kind: KindPromotedKnowHow, Content: content, EvidenceRef: "evidence:bundle/b-777"})
			if !errors.Is(err, ErrPersonalContext) {
				t.Fatalf("Admit(content with %q) = %v, want ErrPersonalContext", key, err)
			}
		})
	}
}

// --- G4: never substitutes for mission authority ----------------------------

func TestAdmit_RejectsAuthoritySubstitution(t *testing.T) {
	for _, key := range []string{"authority", "grant", "grants", "mission_authority", "mission_envelope", "delegation", "permission", "permissions", "consent", "approval"} {
		t.Run("key="+key, func(t *testing.T) {
			content := govContent()
			content[key] = "principal may deploy"
			err := Admit(Admission{Kind: KindSettledEvidence, Content: content, EvidenceRef: "evidence:bundle/b-777"})
			if !errors.Is(err, ErrAuthoritySubstitution) {
				t.Fatalf("Admit(content with %q) = %v, want ErrAuthoritySubstitution", key, err)
			}
		})
	}
}

func TestAdmit_ProseAuthorityWordsStillAdmit(t *testing.T) {
	// Prose that merely mentions authority words is not a claim: know-how
	// describing a past permission discussion must still admit.
	content := map[string]any{
		"body": "we needed permission from legal before shipping; the grant was discussed, not conferred",
	}
	if err := Admit(Admission{Kind: KindPromotedKnowHow, Content: content, EvidenceRef: "evidence:bundle/b-777"}); err != nil {
		t.Fatalf("prose authority words must not trip the seam, got %v", err)
	}
}

func TestAdmit_RejectsNestedPersonalContext(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content map[string]any
		key     string
	}{
		{"one level", map[string]any{"note": map[string]any{"personal_context": "mood: tired"}}, "note.personal_context"},
		{"deep", map[string]any{"a": map[string]any{"b": map[string]any{"session_context": "x"}}}, "a.b.session_context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Admit(Admission{Kind: KindPromotedKnowHow, Content: tc.content, EvidenceRef: "evidence:bundle/b-777"})
			if !errors.Is(err, ErrPersonalContext) {
				t.Fatalf("Admit(nested %s) = %v, want ErrPersonalContext", tc.key, err)
			}
			if got := err.Error(); !strings.Contains(got, tc.key) {
				t.Fatalf("Admit(nested %s) error %q must report the violating path", tc.key, got)
			}
		})
	}
}

func TestAdmit_RejectsNestedAuthorityMarkers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content map[string]any
		key     string
	}{
		{"one level", map[string]any{"note": map[string]any{"grant": "principal may deploy"}}, "note.grant"},
		{"deep mission envelope", map[string]any{"a": map[string]any{"b": map[string]any{"mission_envelope": "x"}}}, "a.b.mission_envelope"},
		{"in slice", map[string]any{"items": []any{map[string]any{"delegation": "x"}}}, "items[0].delegation"},
		{"deep in nested slices", map[string]any{"items": []any{[]any{map[string]any{"approval": "x"}}}}, "items[0][0].approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Admit(Admission{Kind: KindSettledEvidence, Content: tc.content, EvidenceRef: "evidence:bundle/b-777"})
			if !errors.Is(err, ErrAuthoritySubstitution) {
				t.Fatalf("Admit(nested %s) = %v, want ErrAuthoritySubstitution", tc.key, err)
			}
			if got := err.Error(); !strings.Contains(got, tc.key) {
				t.Fatalf("Admit(nested %s) error %q must report the violating path", tc.key, got)
			}
		})
	}
}

func TestAdmit_RejectsCaseVariantMarkers(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		want error
		kind KnowledgeKind
	}{
		{"top-level Grant", "Grant", ErrAuthoritySubstitution, KindSettledEvidence},
		{"top-level GRANT", "GRANT", ErrAuthoritySubstitution, KindSettledEvidence},
		{"top-level Personal_Context", "Personal_Context", ErrPersonalContext, KindPromotedKnowHow},
		{"nested GRANT", "GRANT", ErrAuthoritySubstitution, KindSettledEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var content map[string]any
			if strings.HasPrefix(tc.name, "nested") {
				content = map[string]any{"note": map[string]any{tc.key: "x"}}
			} else {
				content = govContent()
				content[tc.key] = "x"
			}
			if err := Admit(Admission{Kind: tc.kind, Content: content, EvidenceRef: "evidence:bundle/b-777"}); !errors.Is(err, tc.want) {
				t.Fatalf("Admit(%q) = %v, want %v", tc.key, err, tc.want)
			}
		})
	}
}

func TestAdmit_NestedLegitContentAdmits(t *testing.T) {
	// Nested maps/slices with no marker keys at any depth must still admit.
	content := map[string]any{
		"body": "settled: we ship on tuesday",
		"detail": map[string]any{
			"reviewer": "human-1",
			"steps":    []any{"cut branch", map[string]any{"note": "tagged release"}},
		},
	}
	if err := Admit(Admission{Kind: KindPromotedKnowHow, Content: content, EvidenceRef: "evidence:bundle/b-777"}); err != nil {
		t.Fatalf("legit nested content must admit, got %v", err)
	}
}

// --- Concrete-type scan (type-confusion hardening) ---------------------------
//
// scanMarkers walks by reflection, not by type assertion: concretely-typed
// containers (map[string]string, []map[string]string, map[string]int,
// arrays, structs, pointers) carry the same claim keys as map[string]any
// / []any and must trip the same tripwires at the same paths.

func TestAdmit_RejectsConcreteTypedMarkers(t *testing.T) {
	concreteMap := map[string]string{"grant": "x"}
	cases := []struct {
		name    string
		content map[string]any
		path    string
		want    error
		kind    KnowledgeKind
	}{
		{"map[string]string", map[string]any{"outer": map[string]string{"grant": "x"}}, "outer.grant", ErrAuthoritySubstitution, KindSettledEvidence},
		{"map[string]string upper", map[string]any{"outer": map[string]string{"GRANT": "x"}}, "outer.GRANT", ErrAuthoritySubstitution, KindSettledEvidence},
		{"map[string]string personal", map[string]any{"outer": map[string]string{"personal_context": "x"}}, "outer.personal_context", ErrPersonalContext, KindPromotedKnowHow},
		{"slice of concrete maps", map[string]any{"items": []map[string]string{{"delegation": "x"}}}, "items[0].delegation", ErrAuthoritySubstitution, KindSettledEvidence},
		{"map[string]int", map[string]any{"outer": map[string]int{"grant": 1}}, "outer.grant", ErrAuthoritySubstitution, KindSettledEvidence},
		{"nested combo", map[string]any{"a": map[string]any{"b": map[string]string{"approval": "x"}}}, "a.b.approval", ErrAuthoritySubstitution, KindSettledEvidence},
		{"array of concrete maps", map[string]any{"items": [1]map[string]string{{"grant": "x"}}}, "items[0].grant", ErrAuthoritySubstitution, KindSettledEvidence},
		{"pointer to concrete map", map[string]any{"outer": &concreteMap}, "outer.grant", ErrAuthoritySubstitution, KindSettledEvidence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Admit(Admission{Kind: tc.kind, Content: tc.content, EvidenceRef: "evidence:bundle/b-777"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Admit(%s) = %v, want %v", tc.name, err, tc.want)
			}
			if got := err.Error(); !strings.Contains(got, tc.path) {
				t.Fatalf("Admit(%s) error %q must report the violating path %q", tc.name, got, tc.path)
			}
		})
	}
}

func TestAdmit_RejectsStructFieldMarkers(t *testing.T) {
	type grantClaim struct {
		Grant string
	}
	type upperClaim struct {
		GRANT string
	}
	type personalClaim struct {
		Personal_Context string
	}
	type outer struct {
		Note grantClaim
	}
	for _, tc := range []struct {
		name    string
		content map[string]any
		path    string
		want    error
		kind    KnowledgeKind
	}{
		{"direct field", map[string]any{"note": grantClaim{Grant: "x"}}, "note.Grant", ErrAuthoritySubstitution, KindSettledEvidence},
		{"upper field", map[string]any{"note": upperClaim{GRANT: "x"}}, "note.GRANT", ErrAuthoritySubstitution, KindSettledEvidence},
		{"personal field", map[string]any{"note": personalClaim{Personal_Context: "x"}}, "note.Personal_Context", ErrPersonalContext, KindPromotedKnowHow},
		{"pointer field", map[string]any{"note": &grantClaim{Grant: "x"}}, "note.Grant", ErrAuthoritySubstitution, KindSettledEvidence},
		{"struct in slice", map[string]any{"items": []grantClaim{{Grant: "x"}}}, "items[0].Grant", ErrAuthoritySubstitution, KindSettledEvidence},
		{"nested struct", map[string]any{"wrap": outer{Note: grantClaim{Grant: "x"}}}, "wrap.Note.Grant", ErrAuthoritySubstitution, KindSettledEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Admit(Admission{Kind: tc.kind, Content: tc.content, EvidenceRef: "evidence:bundle/b-777"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Admit(%s) = %v, want %v", tc.name, err, tc.want)
			}
			if got := err.Error(); !strings.Contains(got, tc.path) {
				t.Fatalf("Admit(%s) error %q must report the violating path %q", tc.name, got, tc.path)
			}
		})
	}
}

func TestAdmit_ConcreteTypedLegitContentAdmits(t *testing.T) {
	// Concretely-typed containers with no marker keys — including prose
	// that merely mentions authority words as values — must still admit.
	type clean struct {
		Body string
	}
	content := map[string]any{
		"outer":  map[string]string{"body": "settled note"},
		"counts": map[string]int{"attempts": 3},
		"tags":   []string{"the grant was discussed, not conferred"},
		"nested": []map[string]string{{"note": "tagged release"}},
		"arr":    [2]string{"cut branch", "tagged release"},
		"obj":    clean{Body: "settled"},
	}
	if err := Admit(Admission{Kind: KindPromotedKnowHow, Content: content, EvidenceRef: "evidence:bundle/b-777"}); err != nil {
		t.Fatalf("legit concrete-type content must admit, got %v", err)
	}
}

func TestAdmit_CyclicContentTerminates(t *testing.T) {
	// Reference cycles must terminate the scan instead of overflowing
	// the stack; with no marker keys the admission still admits.
	cyclic := map[string]any{"body": "settled: we ship on tuesday"}
	cyclic["self"] = cyclic
	if err := Admit(Admission{Kind: KindPromotedKnowHow, Content: cyclic, EvidenceRef: "evidence:bundle/b-777"}); err != nil {
		t.Fatalf("cyclic content without markers must admit, got %v", err)
	}
	type node struct {
		Body string
		Next *node
	}
	n := &node{Body: "settled"}
	n.Next = n
	content := map[string]any{"root": n}
	if err := Admit(Admission{Kind: KindPromotedKnowHow, Content: content, EvidenceRef: "evidence:bundle/b-777"}); err != nil {
		t.Fatalf("cyclic struct content without markers must admit, got %v", err)
	}
}

// --- G2: human-stamped promotion law (pinned at the seam) -------------------

func TestGovernance_PromotionRequiresHumanStamp(t *testing.T) {
	// Unstamped authority fails closed through the kernel Validate.
	o := mustObj(t, pathIn("decisions", "gov-promote"), ClassMutable, simpleContent())
	o.Authoritative = true
	if err := o.Validate(); !errors.Is(err, ErrNoHumanStamp) {
		t.Fatalf("unstamped authority Validate = %v, want ErrNoHumanStamp (promotion-law bypass)", err)
	}
	// The only constructor of authority demands a human and a note.
	if _, err := PromoteToAuthoritative(mustObj(t, pathIn("decisions", "gov-promote2"), ClassMutable, simpleContent()), "", "note", later(0)); !errors.Is(err, ErrNoHumanStamp) {
		t.Fatalf("anonymous promote = %v, want ErrNoHumanStamp", err)
	}
	if _, err := PromoteToAuthoritative(mustObj(t, pathIn("decisions", "gov-promote3"), ClassMutable, simpleContent()), "human-1", "", later(0)); !errors.Is(err, ErrNoHumanStamp) {
		t.Fatalf("noteless promote = %v, want ErrNoHumanStamp", err)
	}
	// Stamped promotion validates: the lawful path stays open.
	p, err := PromoteToAuthoritative(mustObj(t, pathIn("decisions", "gov-promote4"), ClassMutable, simpleContent()), "human-1", "ratified", later(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("stamped promotion must validate: %v", err)
	}
}

// --- Quittance integrity: no forged quittance --------------------------------

func TestAdmit_QuittanceRequiresQuittanceEvidence(t *testing.T) {
	// Quittance-kind content backed by non-quittance evidence is forgery.
	err := Admit(Admission{Kind: KindQuittance, Content: govContent(), EvidenceRef: "evidence:bundle/b-777"})
	if !errors.Is(err, ErrQuittanceForgery) {
		t.Fatalf("quittance without quittance evidence = %v, want ErrQuittanceForgery", err)
	}
	// Quittance-kind content with no evidence at all fails on evidence (L2).
	err = Admit(Admission{Kind: KindQuittance, Content: govContent(), EvidenceRef: ""})
	if !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("quittance without evidence = %v, want ErrNoEvidence", err)
	}
	// Non-quittance kinds may still cite quittance evidence (a receipt can
	// back know-how); only the quittance label is guarded.
	if err := Admit(Admission{Kind: KindPromotedKnowHow, Content: govContent(), EvidenceRef: "evidence:quittance/q-001"}); err != nil {
		t.Fatalf("know-how citing a quittance must admit, got %v", err)
	}
}

// --- Evidence presence (L2 restated at the seam) -----------------------------

func TestAdmit_EmptyEvidenceFailsClosed(t *testing.T) {
	for _, kind := range []KnowledgeKind{KindSettledEvidence, KindQuittance, KindPromotedKnowHow} {
		t.Run(string(kind), func(t *testing.T) {
			if err := Admit(Admission{Kind: kind, Content: govContent(), EvidenceRef: ""}); !errors.Is(err, ErrNoEvidence) {
				t.Fatalf("Admit(%s, no evidence) = %v, want ErrNoEvidence", kind, err)
			}
		})
	}
}

func TestAdmit_NilContentFailsClosed(t *testing.T) {
	if err := Admit(Admission{Kind: KindSettledEvidence, Content: nil, EvidenceRef: "evidence:bundle/b-777"}); err == nil {
		t.Fatal("nil content must fail closed")
	}
}

// --- Evidence shape: evidence:<layer>/<id> -----------------------------------

func TestAdmit_RejectsMalformedEvidenceShape(t *testing.T) {
	// Every kind must cite the canonical shape evidence:<layer>/<id>
	// with a non-empty layer and a non-empty id.
	malformed := []string{
		"garbage",             // missing scheme
		"x",                   // missing scheme
		"bundle/b-777",        // missing scheme
		"evidence:",           // empty remainder
		"evidence:bundle",     // missing layer/id separator
		"evidence:/b-777",     // empty layer
		"evidence:bundle/",    // empty id
		"evidence:quittance/", // empty id (probed bypass)
	}
	safe := strings.NewReplacer("/", "_", ":", "_")
	for _, kind := range []KnowledgeKind{KindSettledEvidence, KindQuittance, KindPromotedKnowHow} {
		for _, ref := range malformed {
			t.Run(string(kind)+"/"+safe.Replace(ref), func(t *testing.T) {
				err := Admit(Admission{Kind: kind, Content: govContent(), EvidenceRef: ref})
				if !errors.Is(err, ErrEvidenceShape) {
					t.Fatalf("Admit(%s, ref=%q) = %v, want ErrEvidenceShape", kind, ref, err)
				}
			})
		}
	}
}

func TestAdmit_QuittanceRequiresQuittanceLayer(t *testing.T) {
	// Well-formed but wrong layer for the quittance kind stays forgery.
	err := Admit(Admission{Kind: KindQuittance, Content: govContent(), EvidenceRef: "evidence:receipt/q-001"})
	if !errors.Is(err, ErrQuittanceForgery) {
		t.Fatalf("quittance citing a non-quittance layer = %v, want ErrQuittanceForgery", err)
	}
}

func TestAdmit_AcceptsWellFormedEvidenceLayers(t *testing.T) {
	// Non-quittance kinds accept any well-formed layer: the seam guards
	// shape, not the layer vocabulary.
	for _, ref := range []string{"evidence:bundle/b-777", "evidence:receipt/r-1"} {
		t.Run(ref, func(t *testing.T) {
			if err := Admit(Admission{Kind: KindSettledEvidence, Content: govContent(), EvidenceRef: ref}); err != nil {
				t.Fatalf("well-formed ref %q must admit, got %v", ref, err)
			}
		})
	}
}
