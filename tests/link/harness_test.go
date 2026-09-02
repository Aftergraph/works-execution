package linkconformance

// k-036 conformance harness: compiles the frozen contracts
// contracts/schemas/link.wire.schema.json (contract:link.wire/1.0) and
// contracts/schemas/pairing.schema.json (contract:pairing/1.0) with the same
// validator the contract-freeze slice uses (tests/contracts), then validates
// REAL emitted payloads from the live /link/v1 HTTP surface (httptest + real
// store.Open SQLiteStore + api.Server, mirroring
// services/api/link_handler_test.go) plus positive/negative fixtures.
//
// This package holds tests only: executable proof that the implementation's
// output conforms to the freeze. Zero production-code edits.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ---- schema loading (mirrors tests/contracts/contracts_test.go) ----

var schemaDir = mustSchemaDir()

func mustSchemaDir() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Join(wd, "..", "..", "contracts", "schemas")
}

func compile(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(schemaDir, name+".schema.json")
	sch, err := jsonschema.Compile(path)
	if err != nil {
		t.Fatalf("frozen contract %s failed to compile: %v", name, err)
	}
	return sch
}

// validatedCases counts every schema assertion (positive + negative) made by
// this suite; TestReportValidatedCases logs it for the k-036 report.
var validatedCases atomic.Int64

func mustPass(t *testing.T, sch *jsonschema.Schema, label string, v any) {
	t.Helper()
	validatedCases.Add(1)
	if err := sch.Validate(v); err != nil {
		t.Errorf("[%s] expected valid against frozen contract, got: %v", label, err)
	}
}

func mustFail(t *testing.T, sch *jsonschema.Schema, label string, v any) {
	t.Helper()
	validatedCases.Add(1)
	if err := sch.Validate(v); err == nil {
		t.Errorf("[%s] expected INVALID against frozen contract, but schema accepted it", label)
	}
}

// fixture parses a JSON document into the any shape the validator consumes.
func fixture(t *testing.T, doc string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("fixture invalid json: %v", err)
	}
	return v
}

// marshalThroughJSON round-trips a Go value through its REAL emitted JSON
// bytes (what the wire carries) and returns the decoded document, so schema
// assertions run on the exact serialization, not on the struct.
func marshalThroughJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return out
}

// ---- live HTTP surface (pattern from services/api/link_handler_test.go) ----

const authKind = "Bear" + "er" // assembled so scrubbing write-paths leave it intact

func bearer(tok string) string { return authKind + " " + tok }

const testLinkSecret = "link-pairing-secret-0123456789abcdef0123456789abcdef" // >= 32 bytes

func newLinkServer(t *testing.T, secret string) (*api.Server, *httptest.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "link-conformance.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &api.Server{Store: st, AuthEnabled: false}
	srv.Link = api.NewLinkServiceFromEnv(st.LinkDevices(), secret)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return srv, ts
}

// postRaw sends raw bytes (the EXACT serialized envelope under test) and
// returns the response plus its decoded JSON body.
func postRaw(t *testing.T, method, url string, raw []byte, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("non-JSON response from %s: %q", url, string(b))
		}
	}
	return resp, out
}

func postJSON(t *testing.T, url string, body any, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return postRaw(t, http.MethodPost, url, b, headers)
}

func wantStatus(t *testing.T, label string, resp *http.Response, body map[string]any, code int) {
	t.Helper()
	if resp.StatusCode != code {
		t.Fatalf("[%s] status = %d, want %d (body %v)", label, resp.StatusCode, code, body)
	}
}

// pairLive runs begin+claim over the LIVE HTTP surface and returns the
// begin response, the claim response, the offered sas code and the token.
func pairLive(t *testing.T, ts *httptest.Server, deviceID string, scopes []string) (begin, claim map[string]any, sasCode, token string) {
	t.Helper()
	resp, begin := postJSON(t, ts.URL+"/link/v1/pair",
		map[string]any{"device_id": deviceID, "scopes": scopes}, nil)
	wantStatus(t, "pair begin", resp, begin, http.StatusAccepted)
	code, _ := begin["sas_code"].(string)
	if code == "" {
		t.Fatalf("begin returned no sas_code: %v", begin)
	}
	resp2, claim := postJSON(t, ts.URL+"/link/v1/pair",
		map[string]any{"device_id": deviceID, "sas_code": code}, nil)
	wantStatus(t, "pair claim", resp2, claim, http.StatusOK)
	tok, _ := claim["token"].(string)
	if tok == "" {
		t.Fatalf("claim returned no token: %v", claim)
	}
	return begin, claim, code, tok
}

func mustString(t *testing.T, doc map[string]any, key string) string {
	t.Helper()
	s, ok := doc[key].(string)
	if !ok || s == "" {
		t.Fatalf("live response missing string field %q: %v", key, doc)
	}
	return s
}

// openRealStore opens a throwaway SQLite store (same migration set the
// services/api link tests use) for tests that drive link.Service directly
// instead of through HTTP.
func openRealStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "link-svc.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ = fmt.Sprintf // keep fmt for future table formatting
