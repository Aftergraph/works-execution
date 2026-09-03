package api

// k-061: authz matrix for the runner registry surface (runner_authz.go).
//
// Pins the full (dev-mode | auth-enforced) x (anonymous | own-runner |
// foreign-runner) grid for the four endpoints this slice changed the
// posture of:
//
//	POST /v1/runners/register          bearer (k-061) + identity binding
//	POST /v1/runners/{id}/abi          bearer (k-059) + ownership (k-061)
//	GET  /v1/runners/{id}/abi          bearer read (k-061), no ownership
//	POST /v1/runners/{id}/abi/negotiate bearer read (k-061), no ownership
//
// and asserts every denial provably leaves registry state unchanged
// (byte-for-byte GET equality before/after).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/services/work/store"
)

// authzFixture is a real router over a temp store, with auth either
// enforced (AuthEnabled=true) or dev-mode (false).
type authzFixture struct {
	ts  *httptest.Server
	srv *Server
}

func authzServer(t *testing.T, authEnabled bool) *authzFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "runner-authz.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: st, AuthEnabled: authEnabled}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = st.Close() })
	return &authzFixture{ts: ts, srv: srv}
}

// mint signs a dev-mode enrollment token for workerID (auth-enabled
// servers only; the issuer is lazily created by Routes()).
func (f *authzFixture) mint(t *testing.T, workerID string) string {
	t.Helper()
	tok, err := f.srv.Auth.Mint(context.Background(), workerID, time.Hour)
	if err != nil {
		t.Fatalf("mint token for %s: %v", workerID, err)
	}
	return tok
}

// do issues a JSON request; token == "" means no Authorization header.
func (f *authzFixture) do(t *testing.T, method, path, body, token string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// identBody renders a minimal schema-valid runner.Identity for id.
func identBody(id string) string {
	return `{"runner_id":"` + id + `","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":[],"os":["linux"],"arch":["amd64"]}}`
}

const (
	rabFull  = `{"abi":"rab/1.0","caps":["observe","control"],"control_token_required":true}`
	rabSmall = `{"abi":"rab/1.0","caps":["screenshot"]}`
)

// mustRegisterOwner registers runnerID with its own token, failing the
// test unless the API accepts it (201 first / 200 heartbeat upsert).
func (f *authzFixture) mustRegisterOwner(t *testing.T, runnerID, tok string) {
	t.Helper()
	code, body := f.do(t, http.MethodPost, "/v1/runners/register", identBody(runnerID), tok)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("owner register %s: status %d body %s", runnerID, code, body)
	}
}

func TestRunnerAuthz_DevModeInterlock(t *testing.T) {
	// Pinned interlock: AuthEnabled=false means requireBearer never
	// populates claims, so gateRunnerOwnership passes and the whole
	// runner surface keeps its zero-auth k-002 behavior. The e2e suite
	// and local dev depend on this; if it flips, e2e breaks.
	f := authzServer(t, false)
	if code, body := f.do(t, http.MethodPost, "/v1/runners/register", identBody("wrkr_dev_a"), ""); code != http.StatusCreated {
		t.Fatalf("dev anonymous register: status %d body %s, want 201", code, body)
	}
	// Foreign write (no token at all) passes the nil-claims interlock.
	abiURL := "/v1/runners/wrkr_dev_a/abi"
	if code, body := f.do(t, http.MethodPost, abiURL, rabFull, ""); code != http.StatusOK {
		t.Fatalf("dev anonymous POST /abi: status %d body %s, want 200", code, body)
	}
	if code, body := f.do(t, http.MethodGet, abiURL, "", ""); code != http.StatusOK {
		t.Fatalf("dev anonymous GET /abi: status %d body %s, want 200", code, body)
	}
	if code, body := f.do(t, http.MethodPost, abiURL+"/negotiate", `{"caps":["control"]}`, ""); code != http.StatusOK {
		t.Fatalf("dev anonymous negotiate: status %d body %s, want 200", code, body)
	}
	// Token is simply ignored in dev mode (middleware pass-through).
	devTok := f.mint(t, "wrkr_someone_else")
	if code, body := f.do(t, http.MethodPost, "/v1/runners/register", identBody("wrkr_dev_a"), devTok); code != http.StatusOK {
		t.Fatalf("dev heartbeat re-register with unrelated token: status %d body %s, want 200 (token ignored in dev mode)", code, body)
	}
}

func TestRunnerAuthz_RegisterMatrix(t *testing.T) {
	f := authzServer(t, true)
	ownerTok := f.mint(t, "wrkr_a")
	foreignTok := f.mint(t, "wrkr_b")
	regURL := "/v1/runners/register"

	// 1. Anonymous registration is gone: 401 before any mint/store.
	code, body := f.do(t, http.MethodPost, regURL, identBody("wrkr_a"), "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous register: status %d body %s, want 401", code, body)
	}
	if !strings.Contains(string(body), "missing_authorization") {
		t.Fatalf("anonymous register: want missing_authorization, got %s", body)
	}
	if code, _ := f.do(t, http.MethodGet, "/v1/runners/wrkr_a", "", ""); code != http.StatusNotFound {
		t.Fatalf("anonymous register denial must not create the runner: GET id = %d, want 404", code)
	}

	// 2. Foreign token may not register/heartbeat-flood a runner it
	//    does not own (identity binding before store).
	code, body = f.do(t, http.MethodPost, regURL, identBody("wrkr_a"), foreignTok)
	if code != http.StatusForbidden {
		t.Fatalf("foreign-token register: status %d body %s, want 403", code, body)
	}
	if !strings.Contains(string(body), "not_runner_owner") {
		t.Fatalf("foreign-token register: want not_runner_owner, got %s", body)
	}
	if code, _ := f.do(t, http.MethodGet, "/v1/runners/wrkr_a", "", ""); code != http.StatusNotFound {
		t.Fatalf("foreign-token register denial must not create the runner: GET id = %d, want 404", code)
	}

	// 3. Exact-match path (internal/worker startup + heartbeat): works.
	f.mustRegisterOwner(t, "wrkr_a", ownerTok) // 201
	_, hb := f.do(t, http.MethodGet, "/v1/runners/wrkr_a", "", "")
	f.mustRegisterOwner(t, "wrkr_a", ownerTok) // 200 (idempotent upsert)
	_, hb2 := f.do(t, http.MethodGet, "/v1/runners/wrkr_a", "", "")
	if bytes.Equal(hb, hb2) {
		t.Fatal("unexpected: owner heartbeat did not refresh the stored record")
	}
	// A foreign token cannot clobber the now-existing record either.
	before := hb2
	code, body = f.do(t, http.MethodPost, regURL, identBody("wrkr_a"), foreignTok)
	if code != http.StatusForbidden || !strings.Contains(string(body), "not_runner_owner") {
		t.Fatalf("foreign heartbeat-flood attempt: status %d body %s, want 403 not_runner_owner", code, body)
	}
	if _, after := f.do(t, http.MethodGet, "/v1/runners/wrkr_a", "", ""); !bytes.Equal(before, after) {
		t.Fatalf("foreign register attempt mutated the stored identity: before=%s after=%s", before, after)
	}

	// 4. Legacy mode pinned: omitting runner_id lets the server mint
	//    one, and the minted id is NOT auto-bound to the minting token.
	code, body = f.do(t, http.MethodPost, regURL,
		`{"trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":[],"os":["linux"],"arch":["amd64"]}}`, ownerTok)
	if code != http.StatusCreated {
		t.Fatalf("server-mint register: status %d body %s, want 201", code, body)
	}
	var minted struct {
		RunnerID string `json:"runner_id"`
	}
	if err := json.Unmarshal(body, &minted); err != nil {
		t.Fatal(err)
	}
	if minted.RunnerID == "" || minted.RunnerID == "wrkr_a" {
		t.Fatalf("server-mint register returned unexpected runner_id %q", minted.RunnerID)
	}
	// The minting token does not own the minted id (it names wrkr_a):
	// advertising for it is refused. This is the documented legacy-mode
	// consequence in docs/AUTH.md.
	code, body = f.do(t, http.MethodPost, "/v1/runners/"+minted.RunnerID+"/abi", rabFull, ownerTok)
	if code != http.StatusForbidden || !strings.Contains(string(body), "not_runner_owner") {
		t.Fatalf("abi write to server-minted id with minting token: status %d body %s, want 403 not_runner_owner", code, body)
	}
}

func TestRunnerAuthz_PostABIOwnership(t *testing.T) {
	f := authzServer(t, true)
	tokA := f.mint(t, "wrkr_a")
	tokB := f.mint(t, "wrkr_b")
	f.mustRegisterOwner(t, "wrkr_a", tokA)
	f.mustRegisterOwner(t, "wrkr_b", tokB)
	abiA := "/v1/runners/wrkr_a/abi"

	// Owner write succeeds.
	code, body := f.do(t, http.MethodPost, abiA, rabFull, tokA)
	if code != http.StatusOK {
		t.Fatalf("owner POST /abi: status %d body %s, want 200", code, body)
	}
	_, before := f.do(t, http.MethodGet, abiA, "", tokA)

	// Anonymous write: 401 (route-level bearer), state unchanged.
	code, body = f.do(t, http.MethodPost, abiA, rabSmall, "")
	if code != http.StatusUnauthorized || !strings.Contains(string(body), "missing_authorization") {
		t.Fatalf("anonymous POST /abi: status %d body %s, want 401 missing_authorization", code, body)
	}
	if _, after := f.do(t, http.MethodGet, abiA, "", tokA); !bytes.Equal(before, after) {
		t.Fatalf("anonymous write changed state: before=%s after=%s", before, after)
	}

	// Foreign-token write: 403 not_runner_owner (the k-059 residual,
	// now closed), state unchanged.
	code, body = f.do(t, http.MethodPost, abiA, rabSmall, tokB)
	if code != http.StatusForbidden || !strings.Contains(string(body), "not_runner_owner") {
		t.Fatalf("foreign POST /abi: status %d body %s, want 403 not_runner_owner", code, body)
	}
	if _, after := f.do(t, http.MethodGet, abiA, "", tokA); !bytes.Equal(before, after) {
		t.Fatalf("foreign write changed state: before=%s after=%s", before, after)
	}

	// Ordering pinned: the 404 integration-order law runs BEFORE the
	// ownership gate, so a foreign token probing an unknown runner gets
	// runner_not_found, not a 403 oracle.
	code, body = f.do(t, http.MethodPost, "/v1/runners/wrkr_unknown/abi", rabSmall, tokB)
	if code != http.StatusNotFound || !strings.Contains(string(body), "runner_not_found") {
		t.Fatalf("foreign POST /abi on unknown runner: status %d body %s, want 404 runner_not_found", code, body)
	}

	// Owner may still re-advertise after the denials (gate is
	// ownership-scoped, not blanket-hostile).
	code, body = f.do(t, http.MethodPost, abiA, rabSmall, tokA)
	if code != http.StatusOK {
		t.Fatalf("owner re-advertise: status %d body %s, want 200", code, body)
	}
	if _, after := f.do(t, http.MethodGet, abiA, "", tokA); bytes.Equal(before, after) {
		t.Fatal("owner re-advertise did not change the stored advertisement")
	}
}

func TestRunnerAuthz_ReadsAreBearerOnly(t *testing.T) {
	f := authzServer(t, true)
	tokA := f.mint(t, "wrkr_a")
	tokB := f.mint(t, "wrkr_b")
	f.mustRegisterOwner(t, "wrkr_a", tokA)
	abiA := "/v1/runners/wrkr_a/abi"
	if code, body := f.do(t, http.MethodPost, abiA, rabFull, tokA); code != http.StatusOK {
		t.Fatalf("seed POST /abi: status %d body %s", code, body)
	}

	// Anonymous reads are refused: capability info is bearer-gated.
	if code, _ := f.do(t, http.MethodGet, abiA, "", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /abi: status %d, want 401", code)
	}
	if code, body := f.do(t, http.MethodPost, abiA+"/negotiate", `{"caps":["control"]}`, ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous negotiate: status %d body %s, want 401", code, body)
	}

	// Reads are NOT ownership-bound: a foreign enrolled caller may read
	// and negotiate (the scheduler negotiates against other runners'
	// RABs). Pinned on purpose.
	code, body := f.do(t, http.MethodGet, abiA, "", tokB)
	if code != http.StatusOK {
		t.Fatalf("foreign-token GET /abi: status %d body %s, want 200 (bearer read)", code, body)
	}
	code, body = f.do(t, http.MethodPost, abiA+"/negotiate", `{"caps":["control"]}`, tokB)
	if code != http.StatusOK {
		t.Fatalf("foreign-token negotiate: status %d body %s, want 200 (bearer read)", code, body)
	}
	var neg struct {
		Caps []string `json:"caps"`
	}
	if err := json.Unmarshal(body, &neg); err != nil {
		t.Fatal(err)
	}
	if len(neg.Caps) != 1 || neg.Caps[0] != "control" {
		t.Fatalf("negotiate grants = %v, want [control]", neg.Caps)
	}

	// Unknown runner + valid bearer: integration-order 404 survives.
	if code, _ := f.do(t, http.MethodGet, "/v1/runners/wrkr_none/abi", "", tokB); code != http.StatusNotFound {
		t.Fatalf("bearer GET /abi on unknown runner: status %d, want 404", code)
	}

	// Identity reads stay PUBLIC (operator discovery; pinned).
	if code, _ := f.do(t, http.MethodGet, "/v1/runners/wrkr_a", "", ""); code != http.StatusOK {
		t.Fatalf("anonymous GET /v1/runners/{id}: status %d, want 200 (identity read stays public)", code)
	}
}
