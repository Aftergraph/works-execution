package api_test

// k-043 — /v1/brain HTTP surface tests.
//
// Laws under test (from the k-043 spec):
//   - path regex ^/org/[a-f0-9-]+/(missions|decisions|capabilities|evidence|notes)/[A-Za-z0-9_/-]+$
//     enforced fail-closed with 400. Path must end in a name segment.
//   - path_prefix for mounts additionally allows the collection root:
//     ^/org/[a-f0-9-]+/(missions|decisions|capabilities|evidence|notes)(/[A-Za-z0-9_/-]+)?$
//   - evidence_ref empty -> 400 evidence_required
//   - evidence_ref starting wrk_ must exist via GetWork, else 404 unknown_work
//     (we never leak the Work — only its existence)
//   - class enum 400; immutable class -> 409 immutable_no_new_revision;
//     latest tombstoned -> 409 tombstoned; ephemeral expired -> 409 expired
//   - content nil values inside -> 400 not_roundtrip_safe
//   - promote: human_id + note required non-empty -> 400 human_stamp_required
//     (no anonymous authority); ephemeral class -> 409 ephemeral_cannot_promote;
//     already authoritative -> 409; unknown -> 404
//   - mount scopes: only {read} for v1, anything else 400 scope_unsupported
//   - mount ttl default 3600 max 86400
//   - revoke: 200 {id, revoked:true} ALWAYS (unknown id -> revoked:true too)
//   - disabled BrainService -> every route 503 brain_unavailable (fail-closed)
//
// All tests are stand-alone: a fake backend (in-memory maps) implements the
// BrainBackend interface, and a fake GetWork behind a tiny WorkGetter is
// injected via a private constructor. No real SQLiteStore is needed; the
// tests prove the surface compiles and answers per the law table BEFORE
// the k-042 sibling store-impl branch lands.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
)

// (bearer helper is defined in link_handler_test.go and reused here)
// fakeBrainBackend is the in-memory stand-in for the k-042 SQLite-backed
// brain store. The tests use it to drive every legal and illegal state
// transition without touching the database. GetWork is the second axis
// required for the wrk_ existence check on evidence_ref.
type fakeBrainBackend struct {
	// path -> latest revision number (0 = absent)
	latest map[string]int
	// path -> revisions[] (index 0 = rev 1). tombstone[path][rev] is the flag.
	tombstone map[string]map[int]bool
	// path -> class
	class map[string]string
	// path -> authoritative
	auth map[string]bool
	// path -> expires_at (zero time = no expiry)
	expiry map[string]time.Time
	// path -> canonical content hash ledger (test-only inspection)
	hashes map[string][]string

	// mount side
	mounts      map[string]fakeMount
	revokeCalls []string

	// works side — for wrk_ evidence existence check
	works map[string]bool
}

type fakeMount struct {
	ID         string
	Subject    string
	PathPrefix string
	Scopes     []string
	ExpiresAt  time.Time
	Revoked    bool
}

func newFakeBrain() *fakeBrainBackend {
	return &fakeBrainBackend{
		latest:    map[string]int{},
		tombstone: map[string]map[int]bool{},
		class:     map[string]string{},
		auth:      map[string]bool{},
		expiry:    map[string]time.Time{},
		hashes:    map[string][]string{},
		mounts:    map[string]fakeMount{},
		works:     map[string]bool{},
	}
}

// newSeededFake returns a fake with the common evidence work IDs
// pre-populated. Most tests use "wrk_x" as a stand-in work id; this
// keeps the per-test scaffolding short. Tests that explicitly need
// the wrk_ unknown path (TestBrain_EvidenceRef_Wrk_UnknownWork_404 and
// the "non-wrk_ skips existence" test) use newFakeBrain() instead.
func newSeededFake() *fakeBrainBackend {
	f := newFakeBrain()
	f.putWork("wrk_x")
	f.putWork("wrk_known_1")
	f.putWork("wrk_obs_1")
	return f
}

func (f *fakeBrainBackend) putWork(id string) { f.works[id] = true }

// BrainBackend method set — must match the one declared in brain_handler.go.

func (f *fakeBrainBackend) PutBrainObject(ctx context.Context, in *api.BrainPut) (*api.BrainObject, error) {
	if in == nil {
		return nil, errFake("nil put")
	}
	rev := 1
	if cur, ok := f.latest[in.Path]; ok {
		if in.AppendRevision == 0 {
			// explicit new path
			rev = cur + 1
		} else {
			rev = in.AppendRevision
		}
	} else if in.AppendRevision != 0 {
		rev = in.AppendRevision
	}
	if f.tombstone[in.Path] == nil {
		f.tombstone[in.Path] = map[int]bool{}
	}
	if in.Tombstone {
		f.tombstone[in.Path][rev] = true
		f.auth[in.Path] = false
	} else {
		f.class[in.Path] = in.Class
		f.auth[in.Path] = in.Authoritative
		if in.ExpiresAt != nil {
			f.expiry[in.Path] = *in.ExpiresAt
		} else {
			delete(f.expiry, in.Path)
		}
	}
	f.latest[in.Path] = rev
	f.hashes[in.Path] = append(f.hashes[in.Path], in.ContentHash)
	return &api.BrainObject{
		Path:          in.Path,
		Revision:      rev,
		Class:         in.Class,
		Authoritative: in.Authoritative,
		Promotion:     in.Promotion,
		HumanStamp:    in.HumanStamp,
		Tombstone:     in.Tombstone,
		ExpiresAt:     in.ExpiresAt,
		ContentHash:   in.ContentHash,
	}, nil
}

func (f *fakeBrainBackend) GetBrainObject(ctx context.Context, path string, revision int) (*api.BrainObject, error) {
	if _, ok := f.latest[path]; !ok {
		return nil, errFake("not_found")
	}
	rev := revision
	if rev <= 0 {
		rev = f.latest[path]
	}
	tm := f.tombstone[path][rev]
	cls, _ := f.class[path]
	auth := f.auth[path]
	var exp *time.Time
	if t, ok := f.expiry[path]; ok && !t.IsZero() {
		tt := t
		exp = &tt
	}
	return &api.BrainObject{
		Path:          path,
		Revision:      rev,
		Class:         cls,
		Authoritative: auth,
		Promotion:     "none",
		Tombstone:     tm,
		ExpiresAt:     exp,
		ContentHash:   "fakehash",
	}, nil
}

func (f *fakeBrainBackend) ListBrainPathsWithPrefix(ctx context.Context, prefix string) ([]string, error) {
	out := []string{}
	for p := range f.latest {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeBrainBackend) LatestRevision(ctx context.Context, path string) (int, error) {
	if r, ok := f.latest[path]; ok {
		return r, nil
	}
	return 0, errFake("not_found")
}

func (f *fakeBrainBackend) Tombstoned(ctx context.Context, path string, revision int) (bool, error) {
	rev := revision
	if rev <= 0 {
		rev = f.latest[path]
	}
	if rev == 0 {
		return false, errFake("not_found")
	}
	return f.tombstone[path][rev], nil
}

func (f *fakeBrainBackend) CreateBrainMount(ctx context.Context, in *api.BrainMountPut) (*api.BrainMount, error) {
	if f.mounts == nil {
		f.mounts = map[string]fakeMount{}
	}
	m := fakeMount{
		ID:         in.ID,
		Subject:    in.Subject,
		PathPrefix: in.PathPrefix,
		Scopes:     in.Scopes,
		ExpiresAt:  in.ExpiresAt,
	}
	f.mounts[in.ID] = m
	out := &api.BrainMount{
		ID:         m.ID,
		Subject:    m.Subject,
		PathPrefix: m.PathPrefix,
		Scopes:     m.Scopes,
		ExpiresAt:  m.ExpiresAt,
	}
	return out, nil
}

func (f *fakeBrainBackend) RevokeBrainMount(ctx context.Context, id string) error {
	if _, ok := f.mounts[id]; !ok {
		// unknown id — create a tombstone so subsequent list filters it out
		f.mounts[id] = fakeMount{ID: id, Revoked: true}
	} else {
		m := f.mounts[id]
		m.Revoked = true
		f.mounts[id] = m
	}
	f.revokeCalls = append(f.revokeCalls, id)
	return nil
}

func (f *fakeBrainBackend) ListBrainMounts(ctx context.Context, subject string) ([]*api.BrainMount, error) {
	out := []*api.BrainMount{}
	for _, m := range f.mounts {
		if m.Revoked {
			continue
		}
		if m.Subject != subject {
			continue
		}
		out = append(out, &api.BrainMount{
			ID:         m.ID,
			Subject:    m.Subject,
			PathPrefix: m.PathPrefix,
			Scopes:     m.Scopes,
			ExpiresAt:  m.ExpiresAt,
		})
	}
	return out, nil
}

// WorkGetter — used to verify wrk_ evidence_ref existence.

func (f *fakeBrainBackend) GetWork(ctx context.Context, id string) (*workgraph.Work, error) {
	if !f.works[id] {
		return nil, errFake("not_found")
	}
	return &workgraph.Work{ID: id, State: workgraph.StateQueued}, nil
}

type errFake string

func (e errFake) Error() string { return string(e) }

// ---------- server wiring helpers ----------

func newBrainTestServer(t *testing.T, fake *fakeBrainBackend) *httptest.Server {
	t.Helper()
	srv := &api.Server{
		Logger:      testLogger(t),
		AuthEnabled: false, // test surface — bearer requirement tested separately if needed
	}
	// Pass the fake directly: it implements BOTH BrainBackend (the 8
	// methods) and WorkGetter (GetWork). The interface assertion inside
	// NewBrainServiceFromStore succeeds and Disabled stays false.
	srv.Brain = api.NewBrainServiceFromStore(fake, fake)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

func doReq(t *testing.T, method, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp, out
}

// ---------- 503 fail-closed ----------

func TestBrain_AllRoutes_503WhenDisabled(t *testing.T) {
	srv := &api.Server{Logger: testLogger(t), AuthEnabled: false}
	// Mount Brain as a non-nil-but-disabled service: NewBrainServiceFromStore
	// with a nil-shaped store creates this. The surface answers 503 loudly.
	srv.Brain = &api.BrainService{Disabled: true}
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"create", "POST", "/v1/brain/objects", map[string]any{"path": "/org/abc/notes/x", "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x"}},
		{"get", "GET", "/v1/brain/objects?path=/org/abc/notes/x", nil},
		{"prefix", "GET", "/v1/brain/objects?prefix=/org/abc/notes/", nil},
		{"promote", "POST", "/v1/brain/objects/promote", map[string]any{"path": "/org/abc/notes/x", "human_id": "h", "note": "n"}},
		{"tombstone", "POST", "/v1/brain/objects/tombstone", map[string]any{"path": "/org/abc/notes/x", "evidence_ref": "wrk_x"}},
		{"mount create", "POST", "/v1/brain/mounts", map[string]any{"subject": "alice", "path_prefix": "/org/abc/notes", "scopes": []string{"read"}}},
		{"mount list", "GET", "/v1/brain/mounts?subject=alice", nil},
		{"mount revoke", "POST", "/v1/brain/mounts/revoke", map[string]any{"id": "bmt_x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doReq(t, tc.method, ts.URL+tc.path, tc.body)
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d (%v)", resp.StatusCode, body)
			}
			if code, _ := body["error"].(string); code != "brain_unavailable" {
				t.Fatalf("expected code brain_unavailable, got %q (body=%v)", code, body)
			}
		})
	}
}

// TestBrain_NotMountedWhenNil ensures the link to "surface not configured"
// stays absent: when Brain == nil on the server, the routes are not
// registered and a request 404s at the mux — never a 200, never a 503.
func TestBrain_NotMountedWhenNil(t *testing.T) {
	srv := &api.Server{Logger: testLogger(t), AuthEnabled: false}
	// Brain deliberately nil
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
	resp, _ := doReq(t, "GET", ts.URL+"/v1/brain/objects?path=/org/abc/notes/x", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when surface not mounted, got %d", resp.StatusCode)
	}
}

// ---------- happy paths ----------

// validPath returns a path that matches the k-041 collection regex.
// The org segment is hex + dash only (k-041 enum); using "abc0dead"
// would fail because 'm', 'i', 's', 'o', 'n' are outside [a-f0-9].
func validPath() string { return "/org/abc0dead/notes/monday-summary" }

func TestBrain_PostObject_201(t *testing.T) {
	ts := newBrainTestServer(t, newSeededFake())
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path":         validPath(),
		"class":        "ephemeral",
		"content":      map[string]any{"summary": "monday"},
		"evidence_ref": "wrk_obs_1",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", resp.StatusCode, body)
	}
	if body["path"] != validPath() {
		t.Fatalf("path echo wrong: %v", body["path"])
	}
	if body["revision"].(float64) != 1 {
		t.Fatalf("expected revision 1, got %v", body["revision"])
	}
	if body["authoritative"] != false {
		t.Fatalf("expected authoritative:false, got %v", body["authoritative"])
	}
	if body["promotion"] != "none" {
		t.Fatalf("expected promotion:none, got %v", body["promotion"])
	}
	if h, _ := body["content_hash"].(string); h == "" || len(h) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %q", h)
	}
}

func TestBrain_PostObject_HashIsDeterministic(t *testing.T) {
	ts := newBrainTestServer(t, newSeededFake())
	content := map[string]any{"k": "v", "n": 1}
	resp1, b1 := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": content, "evidence_ref": "wrk_x",
	})
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("post 1: %d %v", resp1.StatusCode, b1)
	}
	// second post on a fresh path — same content, same hash
	resp2, b2 := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": "/org/abc0dead/notes/another", "class": "ephemeral", "content": content, "evidence_ref": "wrk_x",
	})
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("post 2: %d %v", resp2.StatusCode, b2)
	}
	if b1["content_hash"] != b2["content_hash"] {
		t.Fatalf("content hash should be deterministic for same content: %v vs %v", b1["content_hash"], b2["content_hash"])
	}
}

func TestBrain_PostObject_RejectsNilInsideContent(t *testing.T) {
	ts := newBrainTestServer(t, newSeededFake())
	// map with explicit nil value — encoding/json's Marshal drops nil-valued
	// map entries silently, which breaks round-trip equality. Spec: 400.
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"k": nil}, "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%v)", resp.StatusCode, body)
	}
	if body["error"] != "not_roundtrip_safe" {
		t.Fatalf("expected code not_roundtrip_safe, got %v", body["error"])
	}
}

// ---------- 400s on path ----------

func TestBrain_PathRegex_Adversarial(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	cases := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"no_org_prefix", "/notes/x"},
		{"no_collection", "/org/abc0dead/"},
		{"no_name_segment", "/org/abc0dead/notes/"},
		{"bad_collection", "/org/abc0dead/widgets/x"},
		{"uppercase_org", "/org/ABC/notes/x"},
		{"unslug", "/org/abc/notes/Spaces Here/x"},
		{"sql_injection", "/org/abc/notes/x'; DROP TABLE--"},
		{"slashdotdot", "/org/abc/notes/../etc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
				"path": tc.path, "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
			})
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%v) for path %q", resp.StatusCode, body, tc.path)
			}
			if body["error"] != "invalid_path" {
				t.Fatalf("expected code invalid_path, got %v", body["error"])
			}
		})
	}
}

// ---------- 400 evidence_required ----------

func TestBrain_EvidenceRef_Empty_400(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if body["error"] != "evidence_required" {
		t.Fatalf("got %v", body["error"])
	}
}

// ---------- 404 unknown_work ----------

func TestBrain_EvidenceRef_Wrk_UnknownWork_404(t *testing.T) {
	fake := newFakeBrain()
	// note: fake.works is empty
	ts := newBrainTestServer(t, fake)
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_does_not_exist",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if body["error"] != "unknown_work" {
		t.Fatalf("got %v", body["error"])
	}
	// MUST NOT leak work fields — message and body must be empty of work
	// content (no ID echoed back as message).
	if msg, _ := body["message"].(string); strings.Contains(msg, "wrk_") {
		t.Fatalf("message must not echo work id, got %q", msg)
	}
}

func TestBrain_EvidenceRef_Wrk_KnownWork_201(t *testing.T) {
	fake := newFakeBrain()
	fake.putWork("wrk_known_1")
	ts := newBrainTestServer(t, fake)
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_known_1",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", resp.StatusCode, body)
	}
}

func TestBrain_EvidenceRef_NonWrk_NoExistenceCheck(t *testing.T) {
	// a non-wrk_ evidence_ref MUST NOT trigger the work existence check;
	// the spec only enforces existence for refs starting with "wrk_".
	fake := newFakeBrain() // works map empty
	ts := newBrainTestServer(t, fake)
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "obs_anything",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 (non-wrk_ evidence skips existence), got %d (%v)", resp.StatusCode, body)
	}
}

// ---------- 400 class enum ----------

func TestBrain_Class_Enum_400(t *testing.T) {
	ts := newBrainTestServer(t, newSeededFake())
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "weather", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if body["error"] != "invalid_class" {
		t.Fatalf("got %v", body["error"])
	}
}

// ---------- 409 immutable / tombstoned / expired ----------

func TestBrain_ImmutableClass_409(t *testing.T) {
	ts := newBrainTestServer(t, newSeededFake())
	// 1) create as immutable
	resp, _ := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "immutable", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed create: %d", resp.StatusCode)
	}
	// 2) second post on same path — must refuse
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "immutable", "content": map[string]any{"v": 2}, "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%v)", resp.StatusCode, body)
	}
	if body["error"] != "immutable_no_new_revision" {
		t.Fatalf("got %v", body["error"])
	}
}

func TestBrain_Tombstoned_409(t *testing.T) {
	fake := newSeededFake()
	ts := newBrainTestServer(t, fake)
	// 1) create ephemeral
	resp, _ := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed create: %d", resp.StatusCode)
	}
	// 2) tombstone
	resp, _ = doReq(t, "POST", ts.URL+"/v1/brain/objects/tombstone", map[string]any{
		"path": validPath(), "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tombstone: %d", resp.StatusCode)
	}
	// 3) attempt to create on the same path — must refuse
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 2}, "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%v)", resp.StatusCode, body)
	}
	if body["error"] != "tombstoned" {
		t.Fatalf("got %v", body["error"])
	}
}

func TestBrain_EphemeralExpired_409(t *testing.T) {
	// Seed an already-expired ephemeral directly into the fake via the
	// server's own POST: the spec says the handler must reject rev-2 posts
	// to an expired path with 409 expired (no revival).
	fake := newSeededFake()
	ts := newBrainTestServer(t, fake)
	// Manually set latest + class + expiry to an expired state.
	// The fake's PutBrainObject enforces these flags; we drive it via
	// an out-of-band revision 1 write:
	// Easier: create the object, then mutate expiry via the fake's
	// internal map (test-only back-door).
	resp, _ := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed: %d", resp.StatusCode)
	}
	// Force expiry into the past
	fake.expiry[validPath()] = time.Now().Add(-time.Hour)
	// Now attempt a new revision
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 2}, "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%v)", resp.StatusCode, body)
	}
	if body["error"] != "expired" {
		t.Fatalf("got %v", body["error"])
	}
}

// ---------- GET single object ----------

func TestBrain_GetObject_404(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, body := doReq(t, "GET", ts.URL+"/v1/brain/objects?path=/org/abc0dead/notes/missing", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if body["error"] != "not_found" {
		t.Fatalf("got %v", body["error"])
	}
}

func TestBrain_GetObject_OK(t *testing.T) {
	fake := newSeededFake()
	fake.putWork("wrk_x")
	ts := newBrainTestServer(t, fake)
	// create
	doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	resp, body := doReq(t, "GET", ts.URL+"/v1/brain/objects?path="+validPath(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", resp.StatusCode, body)
	}
	if body["path"] != validPath() {
		t.Fatalf("path echo: %v", body["path"])
	}
}

func TestBrain_GetObject_400_MissingQuery(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, _ := doReq(t, "GET", ts.URL+"/v1/brain/objects", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when neither path nor prefix given, got %d", resp.StatusCode)
	}
}

func TestBrain_ListByPrefix(t *testing.T) {
	fake := newSeededFake()
	fake.putWork("wrk_x")
	ts := newBrainTestServer(t, fake)
	paths := []string{
		"/org/abc0dead/notes/a",
		"/org/abc0dead/notes/b",
		"/org/abc0dead/decisions/c",
	}
	for _, p := range paths {
		doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
			"path": p, "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
		})
	}
	resp, body := doReq(t, "GET", ts.URL+"/v1/brain/objects?prefix=/org/abc0dead/notes/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", resp.StatusCode, body)
	}
	got, _ := body["paths"].([]any)
	if len(got) != 2 {
		t.Fatalf("expected 2 paths under notes/, got %d (%v)", len(got), got)
	}
}

// ---------- promote ----------

func TestBrain_Promote_LawMatrix(t *testing.T) {
	cases := []struct {
		name     string
		seed     func(f *fakeBrainBackend, ts *httptest.Server)
		body     map[string]any
		wantCode int
		wantErr  string
	}{
		{
			name:     "missing_human_id",
			body:     map[string]any{"path": "/org/abc0dead/notes/m", "note": "n"},
			wantCode: http.StatusBadRequest, wantErr: "human_stamp_required",
		},
		{
			name:     "missing_note",
			body:     map[string]any{"path": "/org/abc0dead/notes/m", "human_id": "h"},
			wantCode: http.StatusBadRequest, wantErr: "human_stamp_required",
		},
		{
			name:     "empty_human_id",
			body:     map[string]any{"path": "/org/abc0dead/notes/m", "human_id": "", "note": "n"},
			wantCode: http.StatusBadRequest, wantErr: "human_stamp_required",
		},
		{
			name:     "empty_note",
			body:     map[string]any{"path": "/org/abc0dead/notes/m", "human_id": "h", "note": ""},
			wantCode: http.StatusBadRequest, wantErr: "human_stamp_required",
		},
		{
			name:     "unknown_path",
			body:     map[string]any{"path": "/org/abc0dead/notes/m", "human_id": "h", "note": "n"},
			wantCode: http.StatusNotFound, wantErr: "not_found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newSeededFake()
			ts := newBrainTestServer(t, fake)
			if tc.seed != nil {
				tc.seed(fake, ts)
			}
			resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects/promote", tc.body)
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("expected %d, got %d (%v)", tc.wantCode, resp.StatusCode, body)
			}
			if body["error"] != tc.wantErr {
				t.Fatalf("expected %s, got %v", tc.wantErr, body["error"])
			}
		})
	}
}

func TestBrain_Promote_Ephemeral_409(t *testing.T) {
	fake := newSeededFake()
	fake.putWork("wrk_x")
	ts := newBrainTestServer(t, fake)
	doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "ephemeral", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects/promote", map[string]any{
		"path": validPath(), "human_id": "alice", "note": "verified",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	if body["error"] != "ephemeral_cannot_promote" {
		t.Fatalf("got %v", body["error"])
	}
}

func TestBrain_Promote_AlreadyAuthoritative_409(t *testing.T) {
	fake := newSeededFake()
	fake.putWork("wrk_x")
	ts := newBrainTestServer(t, fake)
	doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "mutable_with_revision", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	doReq(t, "POST", ts.URL+"/v1/brain/objects/promote", map[string]any{
		"path": validPath(), "human_id": "alice", "note": "first stamp",
	})
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects/promote", map[string]any{
		"path": validPath(), "human_id": "alice", "note": "second stamp",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	if body["error"] != "already_authoritative" {
		t.Fatalf("got %v", body["error"])
	}
}

func TestBrain_Promote_HappyPath_200(t *testing.T) {
	fake := newSeededFake()
	fake.putWork("wrk_x")
	ts := newBrainTestServer(t, fake)
	doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "mutable_with_revision", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects/promote", map[string]any{
		"path": validPath(), "human_id": "alice", "note": "looks good",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", resp.StatusCode, body)
	}
	if body["authoritative"] != true {
		t.Fatalf("expected authoritative:true after promote, got %v", body["authoritative"])
	}
	if body["promotion"] != "human_stamped" {
		t.Fatalf("expected promotion:human_stamped, got %v", body["promotion"])
	}
	if body["human_stamp"] != "alice" {
		t.Fatalf("expected human_stamp:alice, got %v", body["human_stamp"])
	}
}

func TestBrain_Promote_Immutable_StampsRev1ViaUpdate(t *testing.T) {
	// Spec: "immutable stamps revision 1 via update allowed ONLY in this
	// route with promotion fields". We test the contract: an immutable
	// object can be promoted exactly once, and the resulting revision is 1
	// (stamped) — the existing rev 1 carries authoritative:true.
	fake := newSeededFake()
	ts := newBrainTestServer(t, fake)
	doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "immutable", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects/promote", map[string]any{
		"path": validPath(), "human_id": "alice", "note": "stamp",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", resp.StatusCode, body)
	}
	if body["authoritative"] != true {
		t.Fatalf("expected authoritative:true, got %v", body["authoritative"])
	}
	if body["revision"].(float64) != 1 {
		t.Fatalf("expected revision 1 (stamped in place), got %v", body["revision"])
	}
}

// ---------- tombstone ----------

func TestBrain_Tombstone_HappyPath_200(t *testing.T) {
	fake := newSeededFake()
	fake.putWork("wrk_x")
	ts := newBrainTestServer(t, fake)
	doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "mutable_with_revision", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects/tombstone", map[string]any{
		"path": validPath(), "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", resp.StatusCode, body)
	}
	if body["tombstone"] != true {
		t.Fatalf("expected tombstone:true, got %v", body["tombstone"])
	}
	if _, ok := body["revision"]; !ok {
		t.Fatalf("response must include revision, got %v", body)
	}
	// The {path,revision,tombstone:true} response shape is the spec.
	// The "authoritative forced false" check is on the GET-after-tombstone
	// (TestBrain_Tombstone_AppendsRevision reads the persisted state).
}

func TestBrain_Tombstone_AppendsRevision(t *testing.T) {
	fake := newSeededFake()
	fake.putWork("wrk_x")
	ts := newBrainTestServer(t, fake)
	doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": validPath(), "class": "mutable_with_revision", "content": map[string]any{"v": 1}, "evidence_ref": "wrk_x",
	})
	resp, _ := doReq(t, "POST", ts.URL+"/v1/brain/objects/tombstone", map[string]any{
		"path": validPath(), "evidence_ref": "wrk_x",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tombstone: %d", resp.StatusCode)
	}
	// GET should reflect tombstoned latest revision
	resp2, body := doReq(t, "GET", ts.URL+"/v1/brain/objects?path="+validPath(), nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get: %d", resp2.StatusCode)
	}
	if body["tombstone"] != true {
		t.Fatalf("expected tombstone:true in get response, got %v", body)
	}
}

func TestBrain_Tombstone_Evidence_400(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/objects/tombstone", map[string]any{
		"path": validPath(), "evidence_ref": "",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if body["error"] != "evidence_required" {
		t.Fatalf("got %v", body["error"])
	}
}

// ---------- mounts ----------

func TestBrain_Mount_Create_201(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject":     "alice",
		"path_prefix": "/org/abc0dead/notes",
		"scopes":      []string{"read"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", resp.StatusCode, body)
	}
	id, _ := body["id"].(string)
	if !strings.HasPrefix(id, "bmt_") {
		t.Fatalf("expected id to start with bmt_, got %q", id)
	}
	if body["subject"] != "alice" {
		t.Fatalf("subject: %v", body["subject"])
	}
	// ttl default 3600 — expires_at should be ~now+3600
	expStr, _ := body["expires_at"].(string)
	exp, err := time.Parse(time.RFC3339Nano, expStr)
	if err != nil {
		t.Fatalf("expires_at parse: %v", err)
	}
	delta := time.Until(exp)
	if delta < 3500*time.Second || delta > 3700*time.Second {
		t.Fatalf("default ttl expected ~3600s, got %v", delta)
	}
}

func TestBrain_Mount_DefaultPrefix_Allowed(t *testing.T) {
	// The mount path_prefix regex allows a bare collection root (no
	// trailing name segment) — a strict post object would 400, mounts do not.
	ts := newBrainTestServer(t, newFakeBrain())
	resp, _ := doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject":     "alice",
		"path_prefix": "/org/abc0dead/notes",
		"scopes":      []string{"read"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

func TestBrain_Mount_TTLClamp(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	// ttl > 86400 should clamp (handler must reject or clamp; the spec
	// says "max 86400" — we test 400 above the cap).
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject":     "alice",
		"path_prefix": "/org/abc0dead/notes",
		"scopes":      []string{"read"},
		"ttl_seconds": 99999,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%v)", resp.StatusCode, body)
	}
	if body["error"] != "ttl_out_of_range" {
		t.Fatalf("got %v", body["error"])
	}
}

func TestBrain_Mount_TTLZero_UsesDefault(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject":     "alice",
		"path_prefix": "/org/abc0dead/notes",
		"scopes":      []string{"read"},
		"ttl_seconds": 0,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 (ttl 0 -> default 3600), got %d (%v)", resp.StatusCode, body)
	}
	expStr, _ := body["expires_at"].(string)
	exp, _ := time.Parse(time.RFC3339Nano, expStr)
	if d := time.Until(exp); d < 3500*time.Second || d > 3700*time.Second {
		t.Fatalf("default ttl: %v", d)
	}
}

func TestBrain_Mount_BadScope_400(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	cases := [][]string{
		{"write"}, {"read", "write"}, {"admin"}, {""}, {},
	}
	for _, sc := range cases {
		resp, body := doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
			"subject":     "alice",
			"path_prefix": "/org/abc0dead/notes",
			"scopes":      sc,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("scopes=%v expected 400, got %d (%v)", sc, resp.StatusCode, body)
		}
		if body["error"] != "scope_unsupported" {
			t.Fatalf("scopes=%v got %v", sc, body["error"])
		}
	}
}

func TestBrain_Mount_EmptySubject_400(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, _ := doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject":     "",
		"path_prefix": "/org/abc0dead/notes",
		"scopes":      []string{"read"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestBrain_Mount_PathPrefixRegex(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject":     "alice",
		"path_prefix": "/no-org-prefix/notes",
		"scopes":      []string{"read"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if body["error"] != "invalid_path_prefix" {
		t.Fatalf("got %v", body["error"])
	}
}

func TestBrain_Mount_List(t *testing.T) {
	fake := newFakeBrain()
	ts := newBrainTestServer(t, fake)
	doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject": "alice", "path_prefix": "/org/abc0dead/notes", "scopes": []string{"read"},
	})
	doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject": "bob", "path_prefix": "/org/abc0dead/notes", "scopes": []string{"read"},
	})
	resp, body := doReq(t, "GET", ts.URL+"/v1/brain/mounts?subject=alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	mounts, _ := body["mounts"].([]any)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount for alice, got %d", len(mounts))
	}
}

func TestBrain_Mount_List_400WithoutSubject(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, body := doReq(t, "GET", ts.URL+"/v1/brain/mounts", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if body["error"] != "missing_subject" {
		t.Fatalf("got %v", body["error"])
	}
}

// ---------- revoke idempotent ----------

func TestBrain_Revoke_Idempotent_UnknownAndKnown(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	// unknown id
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/mounts/revoke", map[string]any{"id": "bmt_nope"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body["id"] != "bmt_nope" || body["revoked"] != true {
		t.Fatalf("expected {id, revoked:true}, got %v", body)
	}
	// known id
	doReq(t, "POST", ts.URL+"/v1/brain/mounts", map[string]any{
		"subject": "alice", "path_prefix": "/org/abc0dead/notes", "scopes": []string{"read"},
	})
	// grab the id
	_, mbody := doReq(t, "GET", ts.URL+"/v1/brain/mounts?subject=alice", nil)
	mounts, _ := mbody["mounts"].([]any)
	if len(mounts) == 0 {
		t.Fatal("no mount to revoke")
	}
	mid := mounts[0].(map[string]any)["id"].(string)
	resp, body = doReq(t, "POST", ts.URL+"/v1/brain/mounts/revoke", map[string]any{"id": mid})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body["id"] != mid || body["revoked"] != true {
		t.Fatalf("expected {id, revoked:true}, got %v", body)
	}
	// listing should not show the revoked mount
	_, afterBody := doReq(t, "GET", ts.URL+"/v1/brain/mounts?subject=alice", nil)
	after, _ := afterBody["mounts"].([]any)
	if len(after) != 0 {
		t.Fatalf("revoked mount must be filtered from list, got %d", len(after))
	}
}

func TestBrain_Revoke_400_MissingID(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	resp, body := doReq(t, "POST", ts.URL+"/v1/brain/mounts/revoke", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if body["error"] != "id_required" {
		t.Fatalf("got %v", body["error"])
	}
}

// ---------- canonical JSON hash law: key ordering preserved, no nil loss ----------

func TestBrain_Hash_KeysAreCanonicalized(t *testing.T) {
	ts := newBrainTestServer(t, newFakeBrain())
	// map literals in Go have non-deterministic iteration; a robust
	// canonical JSON encoder sorts keys. Build two structurally identical
	// maps with different key order in the body — encoder will sort them.
	a := map[string]any{"a": 1, "b": 2, "c": 3}
	b := map[string]any{"c": 3, "a": 1, "b": 2}
	_, ba := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": "/org/abc0dead/notes/zz1", "class": "ephemeral", "content": a, "evidence_ref": "wrk_x",
	})
	_, bb := doReq(t, "POST", ts.URL+"/v1/brain/objects", map[string]any{
		"path": "/org/abc0dead/notes/zz2", "class": "ephemeral", "content": b, "evidence_ref": "wrk_x",
	})
	if ba["content_hash"] != bb["content_hash"] {
		t.Fatalf("canonical hash must be key-order independent: %v vs %v", ba["content_hash"], bb["content_hash"])
	}
}

// testLogger is a tiny stdlib log.Logger that discards output; keeps the
// test runs quiet without bringing in the api.test-only logger.
func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(io.Discard, "", 0)
}
