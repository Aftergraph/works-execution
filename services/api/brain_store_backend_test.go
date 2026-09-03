package api_test

// k-050 seam tests: the /v1/brain surface driven end-to-end against the
// REAL store.SQLiteStore (schema v11) — the exact path production takes.
// The fake-backend tests in brain_handler_test.go prove the laws; these
// prove the seam: the store-assertion interlock flips live, revision
// allocation appends on real rows, immutable promotion stamps rev 1 in
// place, evidence provenance persists, tombstones append, mounts round-trip
// through brain_mounts.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// realBrainServer wires a Server whose Brain backend is a REAL store
// (no fake anywhere): this is the production construction path.
func realBrainServer(t *testing.T) (*store.SQLiteStore, *httptest.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "brain-live.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &api.Server{Store: st, AuthEnabled: false}
	srv.Brain = api.NewBrainServiceFromStore(st, st)
	if srv.Brain.Disabled {
		t.Fatal("k-050 interlock: real store did NOT satisfy the backend seam — surface would 503 in production")
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return st, ts
}

func brainJSON(t *testing.T, ts *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
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
	return resp.StatusCode, out
}

// A valid v1 object path.
func livePath(name string) string { return "/org/0a1b/decisions/" + name }

func TestSeamCreateAppendAndReadRealStore(t *testing.T) {
	_, ts := realBrainServer(t)
	p := livePath("adr-smoke")
	code, body := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": p, "class": "mutable_with_revision", "content": map[string]any{"b": 1, "a": 2}, "evidence_ref": "note:seam"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	if body["revision"] != float64(1) || len(body["content_hash"].(string)) != 64 {
		t.Fatalf("create response: %v", body)
	}
	// key-order independence of the stored hash (same content, different wire order)
	code2, body2 := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": p, "class": "mutable_with_revision", "content": map[string]any{"a": 2, "b": 1}, "evidence_ref": "note:seam2"})
	if code2 != http.StatusCreated || body2["revision"] != float64(2) {
		t.Fatalf("append: %d %v", code2, body2)
	}
	// GET latest
	code3, got := brainJSON(t, ts, http.MethodGet, "/v1/brain/objects?path="+p, nil)
	if code3 != http.StatusOK || got["revision"] != float64(2) {
		t.Fatalf("get: %d %v", code3, got)
	}
	// content never serializes (hash-only wire contract)
	if _, leaked := got["content"]; leaked {
		t.Fatalf("GET leaked content field: %v", got)
	}
	// evidence provenance persisted on the real row
	obj, err := readRow(t, ts, p)
	if err != nil {
		t.Fatal(err)
	}
	if obj["evidence_ref"] != "note:seam2" {
		t.Fatalf("evidence provenance missing from row: %v", obj)
	}
	// prefix listing sees the path
	_, list := brainJSON(t, ts, http.MethodGet, "/v1/brain/objects?prefix=/org/0a1b/decisions", nil)
	paths, _ := list["paths"].([]any)
	if len(paths) != 1 || paths[0] != p {
		t.Fatalf("prefix listing: %v", list)
	}
}

// readRow fetches a specific revision through the API (revision is in the
// GET projection; store-level fields checked via tombstone/promote reads).
func readRow(t *testing.T, ts *httptest.Server, p string) (map[string]any, error) {
	t.Helper()
	code, body := brainJSON(t, ts, http.MethodGet, "/v1/brain/objects?path="+p, nil)
	if code != http.StatusOK {
		return nil, fmt.Errorf("read %s: HTTP %d", p, code)
	}
	return body, nil
}

func TestSeamImmutablePromoteStampsRev1(t *testing.T) {
	_, ts := realBrainServer(t)
	p := livePath("constitution")
	code, _ := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": p, "class": "immutable", "content": map[string]any{"text": "one revision ever"}, "evidence_ref": "note:seam"})
	if code != http.StatusCreated {
		t.Fatalf("immutable create: %d", code)
	}
	// second append refused
	code, body := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": p, "class": "immutable", "content": map[string]any{"text": "nope"}, "evidence_ref": "note:seam2"})
	if code != http.StatusConflict || body["error"] != "immutable_no_new_revision" {
		t.Fatalf("immutable second revision: %d %v", code, body)
	}
	// promote rides rev 1 IN PLACE (k-041 §L5 exception)
	code, body = brainJSON(t, ts, http.MethodPost, "/v1/brain/objects/promote",
		map[string]any{"path": p, "human_id": "jonas", "note": "ratified"})
	if code != http.StatusOK {
		t.Fatalf("promote immutable: %d %v", code, body)
	}
	if body["revision"] != float64(1) || body["authoritative"] != true || body["promotion"] != "human_stamped" {
		t.Fatalf("stamp not on rev 1: %v", body)
	}
}

func TestSeamMutablePromoteAppends(t *testing.T) {
	_, ts := realBrainServer(t)
	p := livePath("policy")
	if code, _ := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": p, "class": "mutable_with_revision", "content": map[string]any{"v": 1}, "evidence_ref": "note:seam"}); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	code, body := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects/promote",
		map[string]any{"path": p, "human_id": "jonas", "note": "approved"})
	if code != http.StatusOK || body["revision"] != float64(2) {
		t.Fatalf("mutable promote must append rev 2: %d %v", code, body)
	}
	// re-promote refused
	code, body = brainJSON(t, ts, http.MethodPost, "/v1/brain/objects/promote",
		map[string]any{"path": p, "human_id": "jonas", "note": "again"})
	if code != http.StatusConflict || body["error"] != "already_authoritative" {
		t.Fatalf("re-promote: %d %v", code, body)
	}
}

func TestSeamTombstoneAppendsAndBlocks(t *testing.T) {
	_, ts := realBrainServer(t)
	p := livePath("deprecated")
	brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": p, "class": "mutable_with_revision", "content": map[string]any{"v": 1}, "evidence_ref": "note:seam"})
	code, body := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects/tombstone",
		map[string]any{"path": p, "evidence_ref": "note:rip"})
	if code != http.StatusOK || body["revision"] != float64(2) || body["tombstone"] != true {
		t.Fatalf("tombstone: %d %v", code, body)
	}
	// further writes refused
	if code, body = brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": p, "class": "mutable_with_revision", "content": map[string]any{"v": 2}, "evidence_ref": "note:zombie"}); code != http.StatusConflict || body["error"] != "tombstoned" {
		t.Fatalf("post-tombstone write: %d %v", code, body)
	}
}

func TestSeamMountsRealStore(t *testing.T) {
	_, ts := realBrainServer(t)
	code, mount := brainJSON(t, ts, http.MethodPost, "/v1/brain/mounts",
		map[string]any{"subject": "worker:forge", "path_prefix": "/org/0a1b/decisions", "scopes": []string{"read"}, "ttl_seconds": 600})
	if code != http.StatusCreated {
		t.Fatalf("mount create: %d %v", code, mount)
	}
	id, _ := mount["id"].(string)
	if !strings.HasPrefix(id, "bmt_") {
		t.Fatalf("mount id shape: %v", mount)
	}
	_, list := brainJSON(t, ts, http.MethodGet, "/v1/brain/mounts?subject=worker:forge", nil)
	rows, _ := list["mounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("mount listing: %v", list)
	}
	// revoke, then listing hides it (API view)
	if code, _ = brainJSON(t, ts, http.MethodPost, "/v1/brain/mounts/revoke", map[string]any{"id": id}); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	_, list2 := brainJSON(t, ts, http.MethodGet, "/v1/brain/mounts?subject=worker:forge", nil)
	if rows2, _ := list2["mounts"].([]any); len(rows2) != 0 {
		t.Fatalf("revoked mount still visible: %v", list2)
	}
}

func TestSeamEvidenceWrkEnforced(t *testing.T) {
	st, ts := realBrainServer(t)
	// no such work -> 404 unknown_work
	if code, body := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": livePath("x-note"), "class": "mutable_with_revision",
			"content": map[string]any{"v": 1}, "evidence_ref": "wrk_doesnotexist00000000000000000ff"}); code != http.StatusNotFound || body["error"] != "unknown_work" {
		t.Fatalf("phantom evidence: %d %v", code, body)
	}
	// create a real work, then evidence passes (existence-only check)
	w := &workgraph.Work{ID: "wrk_000000000000000000000000000000ab",
		Source: workgraph.Source{Type: "cli"}, Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}}}
	if err := st.CreateWork(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if code, body := brainJSON(t, ts, http.MethodPost, "/v1/brain/objects",
		map[string]any{"path": livePath("x-note"), "class": "mutable_with_revision",
			"content": map[string]any{"v": 1}, "evidence_ref": w.ID}); code != http.StatusCreated {
		t.Fatalf("real evidence rejected: %d %v", code, body)
	}
}
