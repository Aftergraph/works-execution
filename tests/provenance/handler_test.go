// Handler tests for GET /v1/works/{id}/provenance.
//
// Exercises the full HTTP path through services/api to prove the route is
// mounted, status codes are correct, the response envelope matches the
// canonical attestation, and the migration to v5 actually creates the
// work_provenance table on a fresh store.

package provenance_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/standards"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/provenance"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// newProvenanceServer spins up an api.Server backed by a fresh SQLite
// store with the producer wired up. Returns the test server, the
// underlying store, and the configured key for cross-check assertions.
func newProvenanceServer(t *testing.T) (*httptest.Server, store.Store, []byte) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	key := []byte("api-handler-test-key-32bytes-min")
	srv := &api.Server{
		Store: st,
		ProvenanceConfig: &api.ProvenanceConfig{
			KeyID:    "test-key-v1",
			HMACKey:  key,
		},
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, st, key
}

// seedTerminalWork creates a Work in a terminal state so the producer
// will accept it.
func seedTerminalWork(t *testing.T, st store.Store) *workgraph.Work {
	t.Helper()
	w := terminalWork()
	if err := st.CreateWork(t.Context(), w); err != nil {
		t.Fatalf("create: %v", err)
	}
	return w
}

func TestHandler_200_ReturnsAttestation(t *testing.T) {
	ts, st, _ := newProvenanceServer(t)
	w := seedTerminalWork(t, st)

	resp, err := http.Get(ts.URL + "/v1/works/" + w.ID + "/provenance")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: got %q", ct)
	}
	var body struct {
		WorkID      string          `json:"work_id"`
		Attestation json.RawMessage `json:"attestation"`
		Signature   string          `json:"signature"`
		KeyID       string          `json:"key_id"`
		BuilderID   string          `json:"builder_id"`
		ProducedAt  string          `json:"produced_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.WorkID != w.ID {
		t.Errorf("work_id: got %q want %q", body.WorkID, w.ID)
	}
	if len(body.Attestation) == 0 {
		t.Error("attestation empty")
	}
	if body.Signature == "" {
		t.Error("signature empty")
	}
	if body.KeyID != "test-key-v1" {
		t.Errorf("key_id: got %q", body.KeyID)
	}
	if body.BuilderID != provenance.BuilderURI {
		t.Errorf("builder_id: got %q", body.BuilderID)
	}
	// Validate the returned envelope against the JSON Schema.
	if err := standards.ValidateBytes("workflow-provenance.schema.json", body.Attestation); err != nil {
		t.Errorf("envelope fails schema validation: %v", err)
	}
}

func TestHandler_405_NonGet(t *testing.T) {
	ts, st, _ := newProvenanceServer(t)
	w := seedTerminalWork(t, st)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/works/"+w.ID+"/provenance", bytes.NewReader([]byte("{}")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d want 405", resp.StatusCode)
	}
}

func TestHandler_404_UnknownWork(t *testing.T) {
	ts, _, _ := newProvenanceServer(t)
	resp, err := http.Get(ts.URL + "/v1/works/wrk_missing/provenance")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
}

func TestHandler_503_Unconfigured(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	srv := &api.Server{Store: st} // ProvenanceConfig is nil
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/works/wrk_anything/provenance")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d want 503", resp.StatusCode)
	}
}

func TestHandler_409_NonTerminalWork(t *testing.T) {
	ts, st, _ := newProvenanceServer(t)
	w := terminalWork()
	w.State = workgraph.StateRunning
	if err := st.CreateWork(t.Context(), w); err != nil {
		t.Fatalf("create: %v", err)
	}
	resp, err := http.Get(ts.URL + "/v1/works/" + w.ID + "/provenance")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status: got %d want 409", resp.StatusCode)
	}
}

func TestHandler_Idempotent_Get(t *testing.T) {
	ts, st, _ := newProvenanceServer(t)
	w := seedTerminalWork(t, st)
	url := ts.URL + "/v1/works/" + w.ID + "/provenance"

	r1, err := http.Get(url)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	defer r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first status: got %d", r1.StatusCode)
	}
	var b1 struct {
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(r1.Body).Decode(&b1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	r2, err := http.Get(url)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("second status: got %d", r2.StatusCode)
	}
	var b2 struct {
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&b2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b1.Signature != b2.Signature {
		t.Errorf("signature should be stable across GETs, got %s then %s", b1.Signature, b2.Signature)
	}
}

func TestStore_MigrationV5(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Work-provenance must exist and accept inserts at v5+.
	if _, err := st.GetProvenance(t.Context(), "wrk_anything"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound on empty store, got %v", err)
	}

	// Provenance has a foreign key to works(id); create the parent first.
	w := terminalWork()
	if err := st.CreateWork(t.Context(), w); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	p := store.Provenance{
		WorkID:      w.ID,
		Attestation: []byte(`{"x":1}`),
		Signature:   []byte("00"),
		KeyID:       "k",
		BuilderID:   "b",
		ProducedAt:  time.Now().UTC(),
	}
	if err := st.SaveProvenance(t.Context(), p); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := st.GetProvenance(t.Context(), w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || string(got.Signature) != "00" {
		t.Errorf("persisted signature mismatch: %+v", got)
	}
}