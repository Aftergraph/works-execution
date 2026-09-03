package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	org      = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	otherOrg = "0d1f2c3b-aaaa-bbbb-cccc-ddddeeeeffff"
	ev       = "evidence:quittance/q-001"
	otherEv  = "evidence:bundle/b-777"
)

func pathIn(collection, tail string) string {
	return "/org/" + org + "/" + collection + "/" + tail
}

var base = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func later(d time.Duration) time.Time { return base.Add(d) }

func mustObj(t *testing.T, path, class string, content map[string]any) *Object {
	t.Helper()
	o, err := NewObject(path, class, content, ev, base)
	if err != nil {
		t.Fatalf("NewObject(%s, %s): unexpected error: %v", path, class, err)
	}
	return o
}

func simpleContent() map[string]any {
	return map[string]any{"body": "we ship on tuesday"}
}

// --- C1 / L1 / frozen shape: Validate --------------------------------------

func TestValidate_PathLaw(t *testing.T) {
	cases := []struct {
		name string
		path string
		ok   bool
	}{
		{"valid decisions", pathIn("decisions", "2026/ship-date"), true},
		{"valid all five collections", "/org/" + org + "/capabilities/x", true},
		{"missing /org prefix", "/team/" + org + "/decisions/x", false},
		{"wrong collection", "/org/" + org + "/secrets/x", false},
		{"uppercase in org segment", "/org/F47AC10B/decisions/x", false},
		{"non-hex org segment", "/org/zzz/decisions/x", false},
		{"empty tail", pathIn("decisions", "x")[:len(pathIn("decisions", ""))], false},
		{"trailing slash", pathIn("decisions", "x/"), false},
		{"double slash", "/org/" + org + "/decisions//x", false},
		{"dot-dot traversal", "/org/" + org + "/decisions/../notes/x", false},
		{"missing tail entirely", "/org/" + org + "/decisions", false},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := mustObj(t, pathIn("notes", "seed"), ClassEphemeral, simpleContent())
			o.Path = tc.path
			err := o.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tc.path, err)
			}
			if !tc.ok && !errors.Is(err, ErrBadPath) {
				t.Fatalf("Validate(%q) = %v, want ErrBadPath", tc.path, err)
			}
		})
	}
}

func TestValidate_CentralLaw_AuthoritativeNeedsHumanStamp(t *testing.T) {
	// Positive: authoritative + human_stamped + stamp is legal.
	ok := mustObj(t, pathIn("decisions", "law"), ClassMutable, simpleContent())
	ok, err := PromoteToAuthoritative(ok, "human-1", "ceo approved", later(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("promoted object must validate: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(o *Object)
		want   error
	}{
		{"authoritative without promotion", func(o *Object) { o.Authoritative = true }, ErrNoHumanStamp},
		{"authoritative with promotion none", func(o *Object) { o.Authoritative = true; o.Promotion = PromotionNone }, ErrNoHumanStamp},
		{"human_stamped with empty stamp", func(o *Object) { o.Promotion = PromotionHumanStamped; o.HumanStamp = "" }, ErrNoHumanStamp},
		{"ephemeral authoritative", func(o *Object) {
			o.Class = ClassEphemeral
			o.Authoritative = true
			o.Promotion = PromotionHumanStamped
			o.HumanStamp = "human-1"
		}, ErrEphemeralAuthority},
		{"bad class", func(o *Object) { o.Class = "squishy" }, ErrBadClass},
		{"zero revision", func(o *Object) { o.Revision = 0 }, nil},
		{"bad promotion enum", func(o *Object) { o.Promotion = "self_anointed" }, nil},
		{"expires on non-ephemeral", func(o *Object) { exp := later(time.Hour); o.ExpiresAt = &exp }, nil},
		{"tombstone and authoritative", func(o *Object) {
			o.Authoritative = true
			o.Promotion = PromotionHumanStamped
			o.HumanStamp = "human-1"
			o.Tombstone = true
		}, ErrTombstoned},
		{"empty evidence ref", func(o *Object) { o.EvidenceRef = "" }, ErrNoEvidence},
		{"tampered hash", func(o *Object) { o.ContentHash = strings.Repeat("0", 64) }, nil},
		{"nil content", func(o *Object) { o.Content = nil }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := mustObj(t, pathIn("decisions", "law2"), ClassMutable, simpleContent())
			// Hash is already correct from NewObject; mutate last so
			// content-tampering cases stay tampered.
			tc.mutate(o)
			err := o.Validate()
			if err == nil {
				t.Fatalf("Validate must fail closed for %s", tc.name)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Validate error = %v, want sentinel %v", err, tc.want)
			}
		})
	}
}

func TestValidate_RejectsAuthorityOnTamperedContent(t *testing.T) {
	// Even a fully stamped object must fail if its content was swapped after
	// promotion — the content address is the law, not the label.
	o := mustObj(t, pathIn("decisions", "law3"), ClassMutable, simpleContent())
	p, err := PromoteToAuthoritative(o, "human-1", "ceo approved", later(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p.Content = map[string]any{"body": "agent sneaked an edit"}
	p.ContentHash = mustHash(t, p.Content)
	if err := p.Validate(); err != nil {
		t.Fatalf("consistent re-hash should validate; shape laws only: %v", err)
	}
	p.ContentHash = strings.Repeat("f", 64)
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "content_hash mismatch") {
		t.Fatalf("hash mismatch must fail closed, got %v", err)
	}
}

// --- ContentHashOf ----------------------------------------------------------

func mustHash(t *testing.T, content map[string]any) string {
	t.Helper()
	h, err := ContentHashOf(content)
	if err != nil {
		t.Fatalf("ContentHashOf: %v", err)
	}
	return h
}

func TestContentHashOf_Canonical(t *testing.T) {
	got, err := ContentHashOf(map[string]any{"b": "x", "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	want := sha256hex(`{"a":1,"b":"x"}`)
	if got != want {
		t.Fatalf("hash = %s, want canonical sha256 of {\"a\":1,\"b\":\"x\"} = %s", got, want)
	}
	// Key construction order must not matter (sorted-key canonicalisation).
	other := map[string]any{}
	other["b"] = "x"
	other["a"] = 1
	oh, err := ContentHashOf(other)
	if err != nil {
		t.Fatal(err)
	}
	if oh != want {
		t.Fatalf("insertion order changed the content address: %s vs %s", oh, want)
	}
	nested, err := ContentHashOf(map[string]any{"z": map[string]any{"y": 1, "x": []any{1, "two", map[string]any{"k": "v"}}}})
	if err != nil {
		t.Fatal(err)
	}
	wantNested := sha256hex(`{"z":{"x":[1,"two",{"k":"v"}],"y":1}}`)
	if nested != wantNested {
		t.Fatalf("nested canonical hash = %s, want %s", nested, wantNested)
	}
}

func TestContentHashOf_RejectsNilValues(t *testing.T) {
	cases := []struct {
		name    string
		content map[string]any
	}{
		{"top-level nil value", map[string]any{"a": nil}},
		{"nested nil value", map[string]any{"a": map[string]any{"b": nil}}},
		{"nil inside slice", map[string]any{"a": []any{1, nil}}},
		{"nil deep inside", map[string]any{"a": []any{map[string]any{"b": nil}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ContentHashOf(tc.content); err == nil {
				t.Fatal("nil-valued keys must be rejected (hash lesson: they silently change hashes)")
			}
		})
	}
	if _, err := ContentHashOf(nil); err == nil {
		t.Fatal("nil content map must be rejected")
	}
	// Positive: empty map is legal content (hashes to {}).
	h, err := ContentHashOf(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if h != sha256hex("{}") {
		t.Fatalf("empty content hash = %s, want %s", h, sha256hex("{}"))
	}
}

func TestContentHashOf_RejectsUnserializable(t *testing.T) {
	if _, err := ContentHashOf(map[string]any{"f": func() {}}); err == nil {
		t.Fatal("non-JSON value must fail closed")
	}
	if _, err := ContentHashOf(map[string]any{"ch": make(chan int)}); err == nil {
		t.Fatal("channel value must fail closed")
	}
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// --- NewObject (L1, L2, C1-create) -------------------------------------------

func TestNewObject(t *testing.T) {
	t.Run("positive", func(t *testing.T) {
		o := mustObj(t, pathIn("missions", "m1"), ClassMutable, simpleContent())
		if o.Revision != 1 {
			t.Fatalf("revision = %d, want 1 (creation is always revision 1)", o.Revision)
		}
		if o.Authoritative || o.Promotion != PromotionNone || o.HumanStamp != "" {
			t.Fatalf("the create path must structurally never produce authority: %+v", o)
		}
		if o.ContentHash != mustHash(t, o.Content) {
			t.Fatal("content hash must be the content address")
		}
		if !o.CreatedAt.Equal(base) || !o.UpdatedAt.Equal(base) {
			t.Fatal("timestamps must come from the clock parameter")
		}
		if err := jsonRoundTrip(o); err != nil {
			t.Fatalf("object must be JSON round-trip safe: %v", err)
		}
	})
	for _, class := range []string{ClassImmutable, ClassMutable, ClassEphemeral} {
		t.Run("class "+class, func(t *testing.T) {
			mustObj(t, pathIn("notes", "n"), class, simpleContent())
		})
	}
	t.Run("no evidence fails closed", func(t *testing.T) {
		_, err := NewObject(pathIn("notes", "n"), ClassMutable, simpleContent(), "", base)
		if !errors.Is(err, ErrNoEvidence) {
			t.Fatalf("err = %v, want ErrNoEvidence", err)
		}
	})
	t.Run("bad path rejected", func(t *testing.T) {
		_, err := NewObject("/org/"+org+"/secrets/x", ClassMutable, simpleContent(), ev, base)
		if !errors.Is(err, ErrBadPath) {
			t.Fatalf("err = %v, want ErrBadPath", err)
		}
	})
	t.Run("bad class rejected", func(t *testing.T) {
		_, err := NewObject(pathIn("notes", "n"), "eternal", simpleContent(), ev, base)
		if !errors.Is(err, ErrBadClass) {
			t.Fatalf("err = %v, want ErrBadClass", err)
		}
	})
	t.Run("nil content value rejected", func(t *testing.T) {
		_, err := NewObject(pathIn("notes", "n"), ClassMutable, map[string]any{"a": nil}, ev, base)
		if err == nil || !strings.Contains(err.Error(), "nil value") {
			t.Fatalf("err = %v, want nil-value rejection", err)
		}
	})
}

// --- NextRevision (L3, L4, L5, L6) -------------------------------------------

func TestNextRevision(t *testing.T) {
	t.Run("positive: monotonic append", func(t *testing.T) {
		prev := mustObj(t, pathIn("decisions", "d1"), ClassMutable, simpleContent())
		next, err := NextRevision(prev, map[string]any{"body": "we ship on wednesday"}, otherEv, later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if next.Revision != prev.Revision+1 {
			t.Fatalf("revision = %d, want %d (strictly monotonic append)", next.Revision, prev.Revision+1)
		}
		if prev.Revision != 1 {
			t.Fatal("prev must never be edited in place (L4)")
		}
		if next.CreatedAt.Equal(next.UpdatedAt) {
			t.Fatal("next revision must carry its own updated_at")
		}
		if !next.CreatedAt.Equal(prev.CreatedAt) {
			t.Fatal("lineage birth (created_at) carries forward")
		}
		if next.ContentHash == prev.ContentHash {
			t.Fatal("content changed — the content address must change")
		}
		// Positive: identical content keeps the address stable.
		same, err := NextRevision(prev, simpleContent(), otherEv, later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if same.ContentHash != prev.ContentHash {
			t.Fatal("identical content must hash identical across revisions")
		}
	})
	t.Run("revision 3 keeps climbing", func(t *testing.T) {
		a := mustObj(t, pathIn("notes", "chain"), ClassMutable, map[string]any{"v": 1})
		b, _ := NextRevision(a, map[string]any{"v": 2}, ev, later(time.Minute))
		c, err := NextRevision(b, map[string]any{"v": 3}, ev, later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if c.Revision != 3 {
			t.Fatalf("revision = %d, want 3", c.Revision)
		}
	})
	t.Run("authority never rides a new revision", func(t *testing.T) {
		a := mustObj(t, pathIn("decisions", "auth"), ClassMutable, simpleContent())
		pa, err := PromoteToAuthoritative(a, "human-1", "law", later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		nb, err := NextRevision(pa, map[string]any{"body": "agent edit"}, otherEv, later(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if nb.Authoritative || nb.Promotion != PromotionNone || nb.HumanStamp != "" {
			t.Fatalf("a stamp binds to content, not to a path — unstamped new revision is not law: %+v", nb)
		}
	})
	t.Run("immutable: one revision ever", func(t *testing.T) {
		prev := mustObj(t, pathIn("decisions", "law-once"), ClassImmutable, simpleContent())
		_, err := NextRevision(prev, map[string]any{"body": "edit"}, otherEv, later(time.Hour))
		if !errors.Is(err, ErrImmutable) {
			t.Fatalf("err = %v, want ErrImmutable", err)
		}
	})
	t.Run("ephemeral: dead after expiry, no revival", func(t *testing.T) {
		prev := mustObj(t, pathIn("notes", "standup"), ClassEphemeral, simpleContent())
		exp := later(time.Hour)
		prev.ExpiresAt = &exp
		_, err := NextRevision(prev, map[string]any{"body": "x"}, otherEv, later(2*time.Hour))
		if !errors.Is(err, ErrExpired) {
			t.Fatalf("err = %v, want ErrExpired", err)
		}
		_, err = NextRevision(prev, map[string]any{"body": "x"}, otherEv, exp) // exactly at expiry is dead (fail closed)
		if !errors.Is(err, ErrExpired) {
			t.Fatalf("at-expiry err = %v, want ErrExpired", err)
		}
		alive, err := NextRevision(prev, map[string]any{"body": "x"}, otherEv, later(59*time.Minute))
		if err != nil {
			t.Fatalf("within expiry must revise: %v", err)
		}
		if alive.Revision != 2 {
			t.Fatalf("revision = %d, want 2", alive.Revision)
		}
	})
	t.Run("ephemeral without expiry fails closed", func(t *testing.T) {
		prev := mustObj(t, pathIn("notes", "noexp"), ClassEphemeral, simpleContent())
		_, err := NextRevision(prev, map[string]any{"body": "x"}, otherEv, later(time.Hour))
		if !errors.Is(err, ErrExpired) {
			t.Fatalf("missing expiry must fail closed as expired, got %v", err)
		}
	})
	t.Run("tombstoned mutable: dead, no revise", func(t *testing.T) {
		prev := mustObj(t, pathIn("notes", "dead"), ClassMutable, simpleContent())
		tomb, err := Tombstone(prev, otherEv, later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		_, err = NextRevision(tomb, map[string]any{"body": "revival"}, ev, later(2*time.Hour))
		if !errors.Is(err, ErrTombstoned) {
			t.Fatalf("err = %v, want ErrTombstoned (old revisions stay readable; the object is dead)", err)
		}
	})
	t.Run("no evidence fails closed", func(t *testing.T) {
		prev := mustObj(t, pathIn("notes", "n"), ClassMutable, simpleContent())
		_, err := NextRevision(prev, simpleContent(), "", later(time.Hour))
		if !errors.Is(err, ErrNoEvidence) {
			t.Fatalf("err = %v, want ErrNoEvidence", err)
		}
	})
	t.Run("garbage prev fails closed", func(t *testing.T) {
		_, err := NextRevision(nil, simpleContent(), ev, base)
		if err == nil {
			t.Fatal("nil prev must fail closed")
		}
		bad := mustObj(t, pathIn("notes", "n"), ClassMutable, simpleContent())
		bad.ContentHash = "not-a-hash"
		if _, err := NextRevision(bad, simpleContent(), ev, base); err == nil {
			t.Fatal("invalid prev lineage must fail closed")
		}
	})
}

// --- PromoteToAuthoritative (C1, L3, L5) -------------------------------------

func TestPromoteToAuthoritative(t *testing.T) {
	t.Run("mutable: promotes via a NEW revision", func(t *testing.T) {
		o := mustObj(t, pathIn("decisions", "d"), ClassMutable, simpleContent())
		p, err := PromoteToAuthoritative(o, "ceo@human", "board ratified the shift", later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if p.Revision != 2 || o.Revision != 1 {
			t.Fatalf("promotion appends a revision: got new=%d prev=%d, want 2/1", p.Revision, o.Revision)
		}
		if !p.Authoritative || p.Promotion != PromotionHumanStamped || p.HumanStamp != "ceo@human" {
			t.Fatalf("stamp fields wrong: %+v", p)
		}
		if p.ContentHash != o.ContentHash {
			t.Fatal("promotion must not change content or its address")
		}
		if err := o.Validate(); err != nil || o.Authoritative {
			t.Fatal("the caller's prev object must stay untouched (audit reading, L6 spirit)")
		}
	})
	t.Run("immutable: stamps the single revision (L5 exception)", func(t *testing.T) {
		o := mustObj(t, pathIn("decisions", "charter"), ClassImmutable, simpleContent())
		p, err := PromoteToAuthoritative(o, "founder@human", "founding charter", later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if p.Revision != 1 || p.ContentHash != o.ContentHash {
			t.Fatal("immutable stamp must keep revision 1 and the same content address")
		}
		if !p.Authoritative || p.Promotion != PromotionHumanStamped || p.HumanStamp != "founder@human" {
			t.Fatalf("stamp fields wrong: %+v", p)
		}
		if o.Authoritative {
			t.Fatal("prev copy must not be mutated")
		}
		_, err = PromoteToAuthoritative(p, "other@human", "again", later(2*time.Hour))
		if err == nil || !strings.Contains(err.Error(), "no re-promote") {
			t.Fatalf("re-promote of stamped immutable must be refused, got %v", err)
		}
		_, err = NextRevision(p, map[string]any{"body": "edit"}, ev, later(2*time.Hour))
		if !errors.Is(err, ErrImmutable) {
			t.Fatalf("stamped immutable still cannot be revised: %v", err)
		}
	})
	t.Run("ephemeral can never become law", func(t *testing.T) {
		o := mustObj(t, pathIn("notes", "standup"), ClassEphemeral, simpleContent())
		exp := later(time.Hour)
		o.ExpiresAt = &exp
		_, err := PromoteToAuthoritative(o, "ceo@human", "make it law", later(time.Minute))
		if !errors.Is(err, ErrEphemeralAuthority) {
			t.Fatalf("err = %v, want ErrEphemeralAuthority", err)
		}
	})
	t.Run("human_id and note are required", func(t *testing.T) {
		o := mustObj(t, pathIn("decisions", "d"), ClassMutable, simpleContent())
		_, err := PromoteToAuthoritative(o, "", "note", later(time.Hour))
		if !errors.Is(err, ErrNoHumanStamp) {
			t.Fatalf("empty humanID err = %v, want ErrNoHumanStamp", err)
		}
		_, err = PromoteToAuthoritative(o, "ceo@human", "", later(time.Hour))
		if !errors.Is(err, ErrNoHumanStamp) {
			t.Fatalf("empty note err = %v, want ErrNoHumanStamp", err)
		}
	})
	t.Run("tombstoned cannot be promoted", func(t *testing.T) {
		o := mustObj(t, pathIn("notes", "n"), ClassMutable, simpleContent())
		tomb, err := Tombstone(o, otherEv, later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		_, err = PromoteToAuthoritative(tomb, "ceo@human", "revive as law?", later(2*time.Hour))
		if !errors.Is(err, ErrTombstoned) {
			t.Fatalf("err = %v, want ErrTombstoned", err)
		}
	})
	t.Run("invalid input object fails closed", func(t *testing.T) {
		_, err := PromoteToAuthoritative(nil, "h", "n", base)
		if err == nil {
			t.Fatal("nil must fail closed")
		}
		bad := mustObj(t, pathIn("decisions", "d"), ClassMutable, simpleContent())
		bad.Path = "/org/" + org + "/secrets/x"
		if _, err := PromoteToAuthoritative(bad, "h", "n", base); !errors.Is(err, ErrBadPath) {
			t.Fatalf("err = %v, want ErrBadPath", err)
		}
	})
}

// --- Tombstone (L6) -----------------------------------------------------------

func TestTombstone(t *testing.T) {
	t.Run("positive: new dead revision, authority stripped", func(t *testing.T) {
		o := mustObj(t, pathIn("decisions", "d"), ClassMutable, simpleContent())
		p, err := PromoteToAuthoritative(o, "ceo@human", "law", later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		tomb, err := Tombstone(p, otherEv, later(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if tomb.Revision != 3 || !tomb.Tombstone {
			t.Fatalf("tombstone = new revision marking death, got %+v", tomb)
		}
		if tomb.Authoritative || tomb.Promotion != PromotionNone || tomb.HumanStamp != "" {
			t.Fatalf("dead things are not law: %+v", tomb)
		}
		if tomb.ContentHash != p.ContentHash {
			t.Fatal("a tombstone marks death; it does not rewrite content")
		}
		if p.Tombstone || p.Revision != 2 {
			t.Fatalf("the prev revision (the once-authoritative one) must stay readable/untouched for audit (§7): %+v", p)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("the prev revision must still validate for audit reading: %v", err)
		}
	})
	t.Run("immutable cannot be tombstoned", func(t *testing.T) {
		o := mustObj(t, pathIn("decisions", "charter"), ClassImmutable, simpleContent())
		_, err := Tombstone(o, otherEv, later(time.Hour))
		if !errors.Is(err, ErrImmutable) {
			t.Fatalf("err = %v, want ErrImmutable", err)
		}
	})
	t.Run("ephemeral cannot be tombstoned", func(t *testing.T) {
		o := mustObj(t, pathIn("notes", "n"), ClassEphemeral, simpleContent())
		_, err := Tombstone(o, otherEv, later(time.Hour))
		if !errors.Is(err, ErrTombstoned) {
			t.Fatalf("err = %v, want ErrTombstoned (ephemeral dies by expiry)", err)
		}
	})
	t.Run("double tombstone refused", func(t *testing.T) {
		o := mustObj(t, pathIn("notes", "n"), ClassMutable, simpleContent())
		tomb, err := Tombstone(o, otherEv, later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Tombstone(tomb, ev, later(2*time.Hour))
		if !errors.Is(err, ErrTombstoned) {
			t.Fatalf("err = %v, want ErrTombstoned", err)
		}
	})
	t.Run("no evidence fails closed", func(t *testing.T) {
		o := mustObj(t, pathIn("notes", "n"), ClassMutable, simpleContent())
		_, err := Tombstone(o, "", later(time.Hour))
		if !errors.Is(err, ErrNoEvidence) {
			t.Fatalf("err = %v, want ErrNoEvidence", err)
		}
	})
}

// --- MatchMount (L7) -----------------------------------------------------------

func TestMatchMount(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		mount string
		want  bool
	}{
		{"mount root sees its own object", pathIn("decisions", "x"), pathIn("decisions", "x"), true},
		{"collection mount sees subtree", "/org/" + org + "/decisions/2026/q3/pick", "/org/" + org + "/decisions", true},
		{"deeper mount sees below itself", "/org/" + org + "/notes/a/b", "/org/" + org + "/notes/a", true},
		{"other org never matches", "/org/" + otherOrg + "/decisions/x", "/org/" + org + "/decisions", false},
		{"org prefix-confusion (xx vs x)", "/org/f47ac10bb/decisions/x", "/org/" + org + "/decisions", false},
		{"collection boundary", "/org/" + org + "/notes/x", "/org/" + org + "/decisions", false},
		{"prefix-confusion: notes vs notessecret", "/org/" + org + "/notessecret/x", "/org/" + org + "/notes", false},
		{"prefix-confusion at tail", "/org/" + org + "/notes/ab", "/org/" + org + "/notes/a", false},
		{"trailing slash mount rejected", pathIn("notes", "x"), "/org/" + org + "/notes/", false},
		{"dot-dot mount rejected", pathIn("notes", "x"), "/org/" + org + "/notes/..", false},
		{"uppercase org in path rejected", "/org/F47AC10B/decisions/x", "/org/F47AC10B/decisions", false},
		{"non-namespace path invisible", "/etc/passwd", "/org/" + org + "/decisions", false},
		{"empty mount sees nothing", pathIn("notes", "x"), "", false},
		{"mount outside the five collections", pathIn("notes", "x"), "/org/" + org + "/secrets", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchMount(tc.path, tc.mount); got != tc.want {
				t.Fatalf("MatchMount(%q, %q) = %v, want %v", tc.path, tc.mount, got, tc.want)
			}
		})
	}
}

// --- Central law, structurally -------------------------------------------------

// TestCentralLaw_NoAuthorityWithoutHuman proves there is no path through this
// package's public constructors to an authoritative object that lacks a human
// stamp: every constructor is exercised and its output is asserted.
func TestCentralLaw_NoAuthorityWithoutHuman(t *testing.T) {
	mk := func(t *testing.T, class string) *Object {
		t.Helper()
		o := mustObj(t, pathIn("decisions", "c"), class, simpleContent())
		if o.Authoritative || o.Promotion != PromotionNone {
			t.Fatalf("NewObject produced authority for class %s", class)
		}
		if class == ClassEphemeral {
			_, err := PromoteToAuthoritative(o, "h", "n", later(time.Hour))
			if !errors.Is(err, ErrEphemeralAuthority) {
				t.Fatalf("ephemeral promote = %v, want ErrEphemeralAuthority", err)
			}
			return o
		}
		p, err := PromoteToAuthoritative(o, "h", "n", later(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !p.Authoritative || p.Promotion != PromotionHumanStamped || p.HumanStamp == "" {
			t.Fatalf("promoted object lacks a human stamp: %+v", p)
		}
		if class == ClassImmutable {
			return p // one revision ever — NextRevision is law-blocked here
		}
		n, err := NextRevision(p, map[string]any{"body": "edited"}, otherEv, later(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if n.Authoritative {
			t.Fatalf("unstamped revision became authoritative: %+v", n)
		}
		return p
	}
	for _, class := range []string{ClassImmutable, ClassMutable, ClassEphemeral} {
		t.Run("class "+class, func(t *testing.T) { mk(t, class) })
	}
	// A hand-assembled authority (bypassing constructors) still must not
	// validate: Validate is the last gate the mount layer must run.
	forged := &Object{
		Path: pathIn("decisions", "forged"), Class: ClassMutable, Revision: 9,
		Content: simpleContent(), Authoritative: true, Promotion: PromotionNone,
		EvidenceRef: ev, CreatedAt: base, UpdatedAt: base,
	}
	forged.ContentHash = mustHash(t, forged.Content)
	if !errors.Is(forged.Validate(), ErrNoHumanStamp) {
		t.Fatal("forged authority must be rejected by Validate")
	}
}

func jsonRoundTrip(o *Object) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	var back Object
	if err := json.Unmarshal(b, &back); err != nil {
		return err
	}
	h, err := ContentHashOf(back.Content)
	if err != nil {
		return err
	}
	if h != o.ContentHash {
		return fmt.Errorf("content address drifted through JSON round-trip: %s vs %s", h, o.ContentHash)
	}
	return nil
}
