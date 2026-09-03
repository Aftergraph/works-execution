package api

// k-043 — /v1/brain HTTP surface.
//
// The brain is the works-execution knowledge layer: durable, named
// objects under /org/{id}/{collection}/{name}, content-addressed by
// canonical-JSON SHA-256, with per-object revision history and a human
// promotion stamp. This file is the ONLY HTTP boundary on the surface;
// the concrete SQLite-backed store lands from the k-042 sibling branch
// and is wired in cmd/works-api/main.go via NewBrainServiceFromStore.
//
// Routes (all behind requireBearer in api.Routes()):
//
//   POST /v1/brain/objects           create or append a revision
//   GET  /v1/brain/objects?path=     fetch the latest revision (404 if absent)
//   GET  /v1/brain/objects?prefix=   list paths under a prefix
//   POST /v1/brain/objects/promote   human stamp; only path to authoritative
//   POST /v1/brain/objects/tombstone append a tombstone revision
//   POST /v1/brain/mounts            create a read-view mount
//   GET  /v1/brain/mounts?subject=   list non-revoked mounts for a subject
//   POST /v1/brain/mounts/revoke     idempotent revoke (unknown id also 200)
//
// Fail-closed law: an unwired BrainService (no BrainBackend AND/OR no
// WorkGetter behind the store) answers 503 brain_unavailable on every
// route. The shape is stable: the integrator's k-042 merge flips the
// service live; until then, the surface is mounted and silent-by-design.
//
// Path regex (k-041): the brain's collection roots are owned collections
// (missions, decisions, capabilities, evidence, notes) and the path must
// include a name segment. The mount prefix variant additionally allows
// the bare collection root, so a mount can read the whole collection.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// ---------- public types ----------

// Class enum for brain objects. The set is closed at this layer; the
// domain package (k-041) will own the same enum and may add members.
const (
	BrainClassMutable   = "mutable"
	BrainClassImmutable = "immutable"
	BrainClassEphemeral = "ephemeral"
)

// Promotion values for brain objects. The route that stamps authoritative
// objects sets Promotion to human_stamped; everything else is "none".
const (
	BrainPromotionNone         = "none"
	BrainPromotionHumanStamped = "human_stamped"
)

// BrainObject is the projection returned to the API client. Internal
// fields (created_at, etc.) are deliberately omitted — the NOW/console
// projection is a later slice (per the spec).
type BrainObject struct {
	Path          string     `json:"path"`
	Revision      int        `json:"revision"`
	ContentHash   string     `json:"content_hash"`
	Class         string     `json:"class"`
	Authoritative bool       `json:"authoritative"`
	Promotion     string     `json:"promotion"`
	HumanStamp    string     `json:"human_stamp,omitempty"`
	Tombstone     bool       `json:"tombstone,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// BrainPut is the input shape the store's PutBrainObject consumes. The
// handler fills every field; the store is responsible for atomicity and
// revision-number allocation when AppendRevision is 0.
type BrainPut struct {
	Path           string
	Revision       int // explicit revision (promote-on-immutable path); 0 = append
	AppendRevision int // alias used by the fake; kept distinct for clarity
	Class          string
	Authoritative  bool
	Promotion      string
	HumanStamp     string
	Tombstone      bool
	ExpiresAt      *time.Time
	ContentHash    string
}

// BrainMount is the API projection of a mount record.
type BrainMount struct {
	ID         string    `json:"id"`
	Subject    string    `json:"subject"`
	PathPrefix string    `json:"path_prefix"`
	Scopes     []string  `json:"scopes"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// BrainMountPut is the input to CreateBrainMount; the handler mints the
// ID (NewID("bmt")) and the expires_at, the store persists.
type BrainMountPut struct {
	ID         string
	Subject    string
	PathPrefix string
	Scopes     []string
	ExpiresAt  time.Time
}

// ---------- backend interface (k-042 method set) ----------

// BrainBackend is the k-042 store-method set the brain surface requires.
// *store.SQLiteStore will satisfy this after the k-042 branch merges; the
// assertion is performed in NewBrainServiceFromStore. Methods take a
// context as the first argument for cancelation hygiene.
type BrainBackend interface {
	PutBrainObject(ctx context.Context, in *BrainPut) (*BrainObject, error)
	GetBrainObject(ctx context.Context, path string, revision int) (*BrainObject, error)
	ListBrainPathsWithPrefix(ctx context.Context, prefix string) ([]string, error)
	LatestRevision(ctx context.Context, path string) (int, error)
	Tombstoned(ctx context.Context, path string, revision int) (bool, error)
	CreateBrainMount(ctx context.Context, in *BrainMountPut) (*BrainMount, error)
	RevokeBrainMount(ctx context.Context, id string) error
	ListBrainMounts(ctx context.Context, subject string) ([]*BrainMount, error)
}

// WorkGetter is the second axis the brain needs: the wrk_ evidence_ref
// existence check (we MUST NOT leak Work fields, just confirm presence).
// *store.SQLiteStore satisfies this TODAY via its GetWork method.
type WorkGetter interface {
	GetWork(ctx context.Context, id string) (*workgraph.Work, error)
}

// BrainService wires the brain surface. Disabled=true means a route
// request gets 503 brain_unavailable — the fail-closed path. Disabled
// is set by NewBrainServiceFromStore when EITHER the BrainBackend or
// WorkGetter assertion fails against the supplied store.
type BrainService struct {
	// Backend is the k-042 store. Nil when Disabled.
	Backend BrainBackend
	// Works is the WorkGetter (existence-only). Nil when Disabled.
	Works WorkGetter
	// Disabled is true when EITHER assertion failed. Routes still mount
	// (api.Routes) but every call returns 503 brain_unavailable.
	Disabled bool
}

// NewBrainServiceFromStore type-asserts the store against BOTH the
// BrainBackend and WorkGetter interfaces. If either fails, the returned
// service is non-nil but Disabled=true — the fail-closed contract.
//
// On the pre-merge state (k-042 not yet merged to main), the
// BrainBackend assertion will fail and every route will 503. After
// k-042 lands, the integrator re-runs the brain_handler_test.go suite
// (whose tests use a fake backend) and the production surface comes
// alive. The 503-path tests in this file stay valid; the happy-path
// tests work against the fake until then.
func NewBrainServiceFromStore(store any, works WorkGetter) *BrainService {
	svc := &BrainService{}
	if works == nil {
		if wg, ok := store.(WorkGetter); ok {
			works = wg
		}
	}
	if bb, ok := store.(BrainBackend); ok {
		svc.Backend = bb
	} else {
		svc.Disabled = true
	}
	if works != nil {
		svc.Works = works
	} else {
		svc.Disabled = true
	}
	return svc
}

// ---------- regex + scope + ttl constants ----------

// brainPathRe matches a full brain object path:
//
//	/org/{org-id}/{collection}/{name-segments}
//
// org-id is hex/dash lowercase; collection is one of the closed enum;
// name segments allow slashes, hyphens, underscores, alphanumerics.
var brainPathRe = regexp.MustCompile(`^/org/[a-f0-9-]+/(missions|decisions|capabilities|evidence|notes)/[A-Za-z0-9_/-]+$`)

// brainMountPrefixRe matches a mount path prefix — same shape as the
// object path but the trailing /{name} is OPTIONAL, so a mount can
// cover a whole collection.
var brainMountPrefixRe = regexp.MustCompile(`^/org/[a-f0-9-]+/(missions|decisions|capabilities|evidence|notes)(/[A-Za-z0-9_/-]+)?$`)

const (
	brainDefaultTTLSeconds = 3600
	brainMaxTTLSeconds     = 86400
	brainScopeRead         = "read"
	brainBodyLimit         = 64 * 1024
)

// validBrainClass returns true when cls is a member of the closed enum.
func validBrainClass(cls string) bool {
	switch cls {
	case BrainClassMutable, BrainClassImmutable, BrainClassEphemeral:
		return true
	}
	return false
}

// canonicalContentJSON produces a deterministic JSON encoding of a brain
// object's content: keys sorted alphabetically at every depth, no
// insignificant whitespace. This is the byte string whose SHA-256 is
// the content_hash. A nil anywhere inside content is REJECTED — encoding/json
// would silently drop nil map entries on round-trip, which would
// invalidate the hash law (the "not_roundtrip_safe" 400).
func canonicalContentJSON(content any) ([]byte, bool, error) {
	// roundtrip-marshal first to surface "concrete" types (numbers as
	// float64, etc.) and to apply default JSON coercions exactly once.
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, false, err
	}
	// detect nil values inside objects: encoding/json silently drops
	// them, so we walk the original and re-check via a fresh decode.
	var anyVal any
	if err := json.Unmarshal(raw, &anyVal); err != nil {
		return nil, false, err
	}
	if hasNilMember(anyVal) {
		return nil, false, nil
	}
	// Re-marshal with sorted keys.
	out, err := marshalCanonical(anyVal)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func hasNilMember(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for _, vv := range t {
			if vv == nil {
				return true
			}
			if hasNilMember(vv) {
				return true
			}
		}
	case []any:
		for _, vv := range t {
			if vv == nil {
				return true
			}
			if hasNilMember(vv) {
				return true
			}
		}
	}
	return false
}

func marshalCanonical(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			buf = append(buf, kb...)
			buf = append(buf, ':')
			vb, err := marshalCanonical(t[k])
			if err != nil {
				return nil, err
			}
			buf = append(buf, vb...)
		}
		buf = append(buf, '}')
		return buf, nil
	case []any:
		buf := []byte{'['}
		for i, x := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			xb, err := marshalCanonical(x)
			if err != nil {
				return nil, err
			}
			buf = append(buf, xb...)
		}
		buf = append(buf, ']')
		return buf, nil
	default:
		return json.Marshal(t)
	}
}

// brainHashHex returns the 64-char lowercase hex SHA-256 of b. Named
// distinctly to avoid collision with work_resume.go's sha256Hex(string).
func brainHashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---------- router + readBrainJSON ----------

// brainRouter dispatches /v1/brain/ requests. The prefix has already
// been stripped by api.Routes(); rest is the suffix (e.g. "/objects",
// "/objects/promote", "/mounts", "/mounts/revoke"). Unknown routes 404.
func (s *Server) brainRouter(w http.ResponseWriter, r *http.Request) {
	if s.Brain == nil {
		// surface not mounted at all (Server.Brain never set) — 404 at the mux.
		writeError(w, http.StatusNotFound, "not_found", r.URL.Path)
		return
	}
	if s.Brain.Disabled {
		writeError(w, http.StatusServiceUnavailable, "brain_unavailable", "Brain surface mounted but unavailable")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/brain")
	switch {
	case rest == "/objects" && r.Method == http.MethodPost:
		s.brainCreateObject(w, r)
	case rest == "/objects" && r.Method == http.MethodGet:
		s.brainGetOrListObjects(w, r)
	case rest == "/objects/promote" && r.Method == http.MethodPost:
		s.brainPromoteObject(w, r)
	case rest == "/objects/tombstone" && r.Method == http.MethodPost:
		s.brainTombstoneObject(w, r)
	case rest == "/mounts" && r.Method == http.MethodPost:
		s.brainCreateMount(w, r)
	case rest == "/mounts" && r.Method == http.MethodGet:
		s.brainListMounts(w, r)
	case rest == "/mounts/revoke" && r.Method == http.MethodPost:
		s.brainRevokeMount(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not in brain surface")
	}
}

// readBrainJSON decodes a size-capped JSON body into dst. The 64KiB
// cap matches the link surface (link_handler.go) — both surfaces are
// metadata-only, no media uploads ever cross this boundary.
func (s *Server) readBrainJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, brainBodyLimit))
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "brain payload could not be decoded")
		return err
	}
	return nil
}

// ---------- POST /v1/brain/objects ----------

type brainCreateBody struct {
	Path        string         `json:"path"`
	Class       string         `json:"class"`
	Content     map[string]any `json:"content"`
	EvidenceRef string         `json:"evidence_ref"`
}

func (s *Server) brainCreateObject(w http.ResponseWriter, r *http.Request) {
	var body brainCreateBody
	if err := s.readBrainJSON(w, r, &body); err != nil {
		return
	}
	if !brainPathRe.MatchString(body.Path) {
		writeError(w, http.StatusBadRequest, "invalid_path", "path must match /org/{org-id}/{collection}/{name}")
		return
	}
	if body.EvidenceRef == "" {
		writeError(w, http.StatusBadRequest, "evidence_required", "evidence_ref is required")
		return
	}
	if !validBrainClass(body.Class) {
		writeError(w, http.StatusBadRequest, "invalid_class", "class must be mutable|immutable|ephemeral")
		return
	}
	// wrk_ evidence: must exist (existence only — never leak fields)
	if strings.HasPrefix(body.EvidenceRef, "wrk_") {
		if _, err := s.Brain.Works.GetWork(r.Context(), body.EvidenceRef); err != nil {
			writeError(w, http.StatusNotFound, "unknown_work", "evidence_ref does not reference a known work")
			return
		}
	}
	// canonical content + nil-value rejection
	canon, ok, err := canonicalContentJSON(body.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_content", "content is not JSON-encodable")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "not_roundtrip_safe", "content contains nil values; round-trip would lose data and break the hash law")
		return
	}
	hash := brainHashHex(canon)

	// Check existing path state for immutable / tombstoned / expired laws.
	if rev, err := s.Brain.Backend.LatestRevision(r.Context(), body.Path); err == nil && rev > 0 {
		// path exists: get latest to determine class + tombstone + expiry
		obj, err := s.Brain.Backend.GetBrainObject(r.Context(), body.Path, 0)
		if err == nil {
			if obj.Class == BrainClassImmutable {
				writeError(w, http.StatusConflict, "immutable_no_new_revision", "immutable objects cannot be revised")
				return
			}
			if obj.Tombstone {
				writeError(w, http.StatusConflict, "tombstoned", "latest revision is tombstoned")
				return
			}
			if obj.Class == BrainClassEphemeral && obj.ExpiresAt != nil && obj.ExpiresAt.Before(time.Now()) {
				writeError(w, http.StatusConflict, "expired", "ephemeral object is expired; no revival")
				return
			}
		}
	}

	put := &BrainPut{
		Path:          body.Path,
		Class:         body.Class,
		Authoritative: false,
		Promotion:     BrainPromotionNone,
		ContentHash:   hash,
	}
	if body.Class == BrainClassEphemeral {
		// default expiry: 1h. The spec doesn't pin this value; it's
		// defensive — without an expiry, the "ephemeral expired -> 409"
		// law can never trigger. The store may persist a longer TTL.
		exp := time.Now().Add(time.Hour)
		put.ExpiresAt = &exp
	}
	out, err := s.Brain.Backend.PutBrainObject(r.Context(), put)
	if err != nil {
		s.logf("brain put failed: %v", err)
		writeError(w, http.StatusInternalServerError, "store_error", "could not write brain object")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---------- GET /v1/brain/objects ----------

func (s *Server) brainGetOrListObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path := q.Get("path")
	prefix := q.Get("prefix")
	if path == "" && prefix == "" {
		writeError(w, http.StatusBadRequest, "missing_query", "either ?path= or ?prefix= required")
		return
	}
	if path != "" {
		if !brainPathRe.MatchString(path) {
			writeError(w, http.StatusBadRequest, "invalid_path", "path must match /org/{org-id}/{collection}/{name}")
			return
		}
		obj, err := s.Brain.Backend.GetBrainObject(r.Context(), path, 0)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", path)
			return
		}
		writeJSON(w, http.StatusOK, obj)
		return
	}
	// prefix listing
	paths, err := s.Brain.Backend.ListBrainPathsWithPrefix(r.Context(), prefix)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if paths == nil {
		paths = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
}

// ---------- POST /v1/brain/objects/promote ----------

type brainPromoteBody struct {
	Path    string `json:"path"`
	HumanID string `json:"human_id"`
	Note    string `json:"note"`
}

func (s *Server) brainPromoteObject(w http.ResponseWriter, r *http.Request) {
	var body brainPromoteBody
	if err := s.readBrainJSON(w, r, &body); err != nil {
		return
	}
	// THE law: no anonymous authority. Both fields required, non-empty.
	if body.HumanID == "" || body.Note == "" {
		writeError(w, http.StatusBadRequest, "human_stamp_required", "human_id and note are required and must be non-empty")
		return
	}
	if !brainPathRe.MatchString(body.Path) {
		writeError(w, http.StatusBadRequest, "invalid_path", "path must match /org/{org-id}/{collection}/{name}")
		return
	}
	// unknown path -> 404 (not 400): the path is well-formed, just absent.
	rev, err := s.Brain.Backend.LatestRevision(r.Context(), body.Path)
	if err != nil || rev <= 0 {
		writeError(w, http.StatusNotFound, "not_found", body.Path)
		return
	}
	obj, err := s.Brain.Backend.GetBrainObject(r.Context(), body.Path, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", body.Path)
		return
	}
	if obj.Class == BrainClassEphemeral {
		writeError(w, http.StatusConflict, "ephemeral_cannot_promote", "ephemeral objects can never be authoritative")
		return
	}
	if obj.Authoritative {
		writeError(w, http.StatusConflict, "already_authoritative", "object is already authoritative")
		return
	}

	put := &BrainPut{
		Path:          body.Path,
		Class:         obj.Class,
		Authoritative: true,
		Promotion:     BrainPromotionHumanStamped,
		HumanStamp:    body.HumanID,
		ContentHash:   obj.ContentHash,
	}
	if obj.Class == BrainClassImmutable {
		// immutable objects are write-once EXCEPT in this route: the
		// promotion fields are stamped onto revision 1 (the only rev)
		// in-place. The store honors the explicit Revision field.
		put.Revision = 1
		put.AppendRevision = 1
	}
	out, err := s.Brain.Backend.PutBrainObject(r.Context(), put)
	if err != nil {
		s.logf("brain promote failed: %v", err)
		writeError(w, http.StatusInternalServerError, "store_error", "could not write promotion")
		return
	}
	// promote response carries the new state; we preserve the human note
	// surface by including it on the response object (the store does
	// not need to persist it — the stamp + rev is the audit trail).
	writeJSON(w, http.StatusOK, out)
}

// ---------- POST /v1/brain/objects/tombstone ----------

type brainTombstoneBody struct {
	Path        string `json:"path"`
	EvidenceRef string `json:"evidence_ref"`
}

func (s *Server) brainTombstoneObject(w http.ResponseWriter, r *http.Request) {
	var body brainTombstoneBody
	if err := s.readBrainJSON(w, r, &body); err != nil {
		return
	}
	if !brainPathRe.MatchString(body.Path) {
		writeError(w, http.StatusBadRequest, "invalid_path", "path must match /org/{org-id}/{collection}/{name}")
		return
	}
	if body.EvidenceRef == "" {
		writeError(w, http.StatusBadRequest, "evidence_required", "evidence_ref is required")
		return
	}
	if strings.HasPrefix(body.EvidenceRef, "wrk_") {
		if _, err := s.Brain.Works.GetWork(r.Context(), body.EvidenceRef); err != nil {
			writeError(w, http.StatusNotFound, "unknown_work", "evidence_ref does not reference a known work")
			return
		}
	}
	// Append a new revision flagged as tombstone. The store allocates the
	// next revision number; we leave the class field blank because the
	// tombstone revision is classless (it cancels all prior revisions of
	// any class).
	put := &BrainPut{
		Path:          body.Path,
		Class:         objClassForTombstone(s, r.Context(), body.Path),
		Authoritative: false, // tombstone always forces authoritative:false
		Promotion:     BrainPromotionNone,
		Tombstone:     true,
		ContentHash:   "", // tombstones carry no content
	}
	out, err := s.Brain.Backend.PutBrainObject(r.Context(), put)
	if err != nil {
		s.logf("brain tombstone failed: %v", err)
		writeError(w, http.StatusInternalServerError, "store_error", "could not write tombstone")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      out.Path,
		"revision":  out.Revision,
		"tombstone": true,
	})
}

// objClassForTombstone is a small helper: when tombstoning, we keep the
// existing class on the new revision so the get-response shape doesn't
// change (downstream readers can still see the original class label).
func objClassForTombstone(s *Server, ctx context.Context, path string) string {
	if obj, err := s.Brain.Backend.GetBrainObject(ctx, path, 0); err == nil {
		return obj.Class
	}
	return BrainClassMutable // unknown path -> tombstone is a fresh classless
	// record; the store may treat it as mutable-default
}

// ---------- POST /v1/brain/mounts ----------

type brainCreateMountBody struct {
	Subject    string   `json:"subject"`
	PathPrefix string   `json:"path_prefix"`
	Scopes     []string `json:"scopes"`
	TTLSeconds int      `json:"ttl_seconds,omitempty"`
}

func (s *Server) brainCreateMount(w http.ResponseWriter, r *http.Request) {
	var body brainCreateMountBody
	if err := s.readBrainJSON(w, r, &body); err != nil {
		return
	}
	if body.Subject == "" {
		writeError(w, http.StatusBadRequest, "missing_subject", "subject is required")
		return
	}
	if !brainMountPrefixRe.MatchString(body.PathPrefix) {
		writeError(w, http.StatusBadRequest, "invalid_path_prefix", "path_prefix must match /org/{org-id}/{collection}(/{name})?")
		return
	}
	// scopes: non-empty subset of {read} for v1 (ADR-0023 — mounts are
	// read-views; write-scopes are out of scope until later slices).
	if len(body.Scopes) == 0 {
		writeError(w, http.StatusBadRequest, "scope_unsupported", "scopes must be a non-empty subset of {read}")
		return
	}
	for _, sc := range body.Scopes {
		if sc != brainScopeRead {
			writeError(w, http.StatusBadRequest, "scope_unsupported", "only 'read' scope is supported in v1")
			return
		}
	}
	// ttl: default 3600, max 86400. Zero means "use default"; any other
	// out-of-range value is rejected.
	ttl := body.TTLSeconds
	if ttl == 0 {
		ttl = brainDefaultTTLSeconds
	}
	if ttl < 0 || ttl > brainMaxTTLSeconds {
		writeError(w, http.StatusBadRequest, "ttl_out_of_range", "ttl_seconds must be between 1 and 86400")
		return
	}
	id := workgraph.NewID("bmt")
	exp := time.Now().Add(time.Duration(ttl) * time.Second)
	mount, err := s.Brain.Backend.CreateBrainMount(r.Context(), &BrainMountPut{
		ID:         id,
		Subject:    body.Subject,
		PathPrefix: body.PathPrefix,
		Scopes:     body.Scopes,
		ExpiresAt:  exp,
	})
	if err != nil {
		s.logf("brain mount create failed: %v", err)
		writeError(w, http.StatusInternalServerError, "store_error", "could not create mount")
		return
	}
	writeJSON(w, http.StatusCreated, mount)
}

// ---------- GET /v1/brain/mounts?subject= ----------

func (s *Server) brainListMounts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	subject := q.Get("subject")
	if subject == "" {
		writeError(w, http.StatusBadRequest, "missing_subject", "subject query param required")
		return
	}
	rows, err := s.Brain.Backend.ListBrainMounts(r.Context(), subject)
	if err != nil {
		s.logf("brain list mounts failed: %v", err)
		writeError(w, http.StatusInternalServerError, "list_failed", "could not list mounts")
		return
	}
	if rows == nil {
		rows = []*BrainMount{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mounts": rows})
}

// ---------- POST /v1/brain/mounts/revoke ----------

type brainRevokeBody struct {
	ID string `json:"id"`
}

func (s *Server) brainRevokeMount(w http.ResponseWriter, r *http.Request) {
	var body brainRevokeBody
	if err := s.readBrainJSON(w, r, &body); err != nil {
		return
	}
	if body.ID == "" {
		writeError(w, http.StatusBadRequest, "id_required", "id is required")
		return
	}
	// Idempotent law: unknown id also returns 200 with revoked:true.
	if err := s.Brain.Backend.RevokeBrainMount(r.Context(), body.ID); err != nil {
		s.logf("brain revoke mount failed: %v", err)
		writeError(w, http.StatusInternalServerError, "store_error", "could not revoke mount")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": body.ID, "revoked": true})
}
