// Package api_test: k-062 server-verified control tokens bound to runner
// identity (see rab_control_token.go for the format and the mode law).
//
// Pinned semantics:
//   - key UNSET => the k-058 advertisement law exactly (presence-only;
//     any non-empty value passes, cross-runner values pass). This file
//     re-pins that legacy shape beside the unedited k-058 tests in
//     claim_abi_gate_test.go, which keep passing untouched.
//   - key SET => the value must verify for the CLAIMING runnerID:
//     correct token proceeds; garbage or a valid token bound to another
//     runner is denied 403 "control_token_invalid" (missing stays
//     "control_token_required") BEFORE any lease state transition.
//   - the token value never appears in logs or error bodies.
//
// Helpers (gateRegister/gatePostRAB/gateCreateWork/gateActiveLeaseNodes/
// gateRABControl) are reused from claim_abi_gate_test.go (same package,
// that file is not edited).
package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bytes"

	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

const ctTestKey = "k062-test-key-0123456789abcdef0123456789"

// ctTestServer mirrors gateTestServer but wires the k-062 carrier
// (key == "" leaves verification mode OFF, exactly like every existing
// dev-mode server) and captures server logs for the leak sweep.
func ctTestServer(t *testing.T, key string) (*httptest.Server, store.Store, *bytes.Buffer) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "ctl-token.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var logs bytes.Buffer
	srv := &api.Server{Store: s, Logger: log.New(&logs, "", 0)}
	if key != "" {
		srv.RABControlKey = []byte(key)
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, s, &logs
}

// ctClaimRaw is gateClaim with the response body returned verbatim, so
// leak sweeps see the exact bytes the server wrote.
func ctClaimRaw(t *testing.T, ts *httptest.Server, workerID, workID string, token *string) (int, string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"work_id":     workID,
		"node_id":     "a",
		"worker_id":   workerID,
		"ttl_seconds": 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/leases/grant", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != nil {
		req.Header.Set("X-RAB-Control-Token", *token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// ctDeniedMustNotMoveState mirrors k-058 case (c): zero active leases
// and work state unchanged -- the deterministic proof the credential
// check still precedes any transition.
func ctDeniedMustNotMoveState(t *testing.T, st store.Store, workID, priorState string) {
	t.Helper()
	if active := gateActiveLeaseNodes(t, st, workID); len(active) != 0 {
		t.Fatalf("denied claim must NOT transition lease state: active=%v", active)
	}
	after, err := st.GetWork(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.State) != priorState {
		t.Fatalf("denied claim must NOT move work state: %s -> %s", priorState, after.State)
	}
}

// Legacy pin: key OFF => k-058 advertisement law, exactly. Any non-empty
// value passes; absence is 403 control_token_required with no transition.
func TestControlTokenUnconfiguredKeepsK058AdvertisementLaw(t *testing.T) {
	ts, st, _ := ctTestServer(t, "") // no key: verification mode OFF
	gateRegister(t, ts, "wrkr_ct_off_a")
	gateRegister(t, ts, "wrkr_ct_off_b")
	gatePostRAB(t, ts, "wrkr_ct_off_a", gateRABControl)
	gatePostRAB(t, ts, "wrkr_ct_off_b", gateRABControl)

	w := gateCreateWork(t, ts)
	if code, body := ctClaimRaw(t, ts, "wrkr_ct_off_a", w, strptr("anything-goes")); code != http.StatusCreated {
		t.Fatalf("key-off presence-only law: garbage token must still pass: got %d %s", code, body)
	}
	if !gateActiveLeaseNodes(t, st, w)["a"] {
		t.Fatal("key-off accepted claim must have created an ACTIVE lease")
	}

	// Cross-runner value passes with no key (k-058 case (e) re-pinned).
	w2 := gateCreateWork(t, ts)
	before, err := st.GetWork(context.Background(), w2)
	if err != nil {
		t.Fatal(err)
	}
	if code, body := ctClaimRaw(t, ts, "wrkr_ct_off_b", w2, strptr("belongs-to-someone-else")); code != http.StatusCreated {
		t.Fatalf("key-off cross-runner value must pass: got %d %s", code, body)
	}
	_ = before

	// Missing header keeps the k-058 denial shape.
	w3 := gateCreateWork(t, ts)
	before3, err := st.GetWork(context.Background(), w3)
	if err != nil {
		t.Fatal(err)
	}
	code, body := ctClaimRaw(t, ts, "wrkr_ct_off_a", w3, nil)
	if code != http.StatusForbidden {
		t.Fatalf("key-off missing token: got %d %s, want 403", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out["error"] != "control_token_required" {
		t.Fatalf("key-off missing token code: got %v, want control_token_required", out["error"])
	}
	ctDeniedMustNotMoveState(t, st, w3, string(before3.State))
}

// Key ON + correct minted token => the claim proceeds.
func TestControlTokenConfiguredValidTokenProceeds(t *testing.T) {
	ts, st, _ := ctTestServer(t, ctTestKey)
	gateRegister(t, ts, "wrkr_ct_ok")
	gatePostRAB(t, ts, "wrkr_ct_ok", gateRABControl)
	tok, err := api.MintRABControlToken([]byte(ctTestKey), "wrkr_ct_ok")
	if err != nil {
		t.Fatal(err)
	}

	w := gateCreateWork(t, ts)
	if code, body := ctClaimRaw(t, ts, "wrkr_ct_ok", w, strptr(tok)); code != http.StatusCreated {
		t.Fatalf("valid bound token must clear the gate: got %d %s", code, body)
	}
	if !gateActiveLeaseNodes(t, st, w)["a"] {
		t.Fatal("accepted claim must have created an ACTIVE lease")
	}
}

// Key ON + garbage => 403 control_token_invalid, no lease transition.
// Key ON + MISSING header => still the k-058 control_token_required code.
func TestControlTokenConfiguredGarbageDenied(t *testing.T) {
	ts, st, _ := ctTestServer(t, ctTestKey)
	gateRegister(t, ts, "wrkr_ct_bad")
	gatePostRAB(t, ts, "wrkr_ct_bad", gateRABControl)

	w := gateCreateWork(t, ts)
	before, err := st.GetWork(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	code, body := ctClaimRaw(t, ts, "wrkr_ct_bad", w, strptr("not.a.real.control-token"))
	if code != http.StatusForbidden {
		t.Fatalf("garbage token: got %d %s, want 403", code, body)
	}
	if !strings.Contains(body, `"error":"control_token_invalid"`) {
		t.Fatalf("garbage token code: body=%s, want control_token_invalid", body)
	}
	ctDeniedMustNotMoveState(t, st, w, string(before.State))

	w2 := gateCreateWork(t, ts)
	before2, err := st.GetWork(context.Background(), w2)
	if err != nil {
		t.Fatal(err)
	}
	code, body = ctClaimRaw(t, ts, "wrkr_ct_bad", w2, nil)
	if code != http.StatusForbidden {
		t.Fatalf("missing token under key-on: got %d %s, want 403", code, body)
	}
	if !strings.Contains(body, `"error":"control_token_required"`) {
		t.Fatalf("missing-token code: body=%s, want control_token_required", body)
	}
	ctDeniedMustNotMoveState(t, st, w2, string(before2.State))
}

// Binding proof: a VALID token minted for runner A, presented while
// claiming as runner B (both control RABs, same server key), is denied
// 403 control_token_invalid -- and the same token still works for A.
func TestControlTokenBoundToOtherRunnerDenied(t *testing.T) {
	ts, st, _ := ctTestServer(t, ctTestKey)
	gateRegister(t, ts, "wrkr_ct_a")
	gateRegister(t, ts, "wrkr_ct_b")
	gatePostRAB(t, ts, "wrkr_ct_a", gateRABControl)
	gatePostRAB(t, ts, "wrkr_ct_b", gateRABControl)
	tokA, err := api.MintRABControlToken([]byte(ctTestKey), "wrkr_ct_a")
	if err != nil {
		t.Fatal(err)
	}

	w := gateCreateWork(t, ts)
	before, err := st.GetWork(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	code, body := ctClaimRaw(t, ts, "wrkr_ct_b", w, strptr(tokA))
	if code != http.StatusForbidden {
		t.Fatalf("cross-runner bound token: got %d %s, want 403", code, body)
	}
	if !strings.Contains(body, `"error":"control_token_invalid"`) {
		t.Fatalf("cross-runner code: body=%s, want control_token_invalid", body)
	}
	ctDeniedMustNotMoveState(t, st, w, string(before.State))

	// The same token clears the gate for its bound runner: the denial
	// was the binding, not token existence or format.
	if code, body := ctClaimRaw(t, ts, "wrkr_ct_a", w, strptr(tokA)); code != http.StatusCreated {
		t.Fatalf("bound token for its own runner must proceed: got %d %s", code, body)
	}
	if !gateActiveLeaseNodes(t, st, w)["a"] {
		t.Fatal("accepted claim must have created an ACTIVE lease")
	}
}

// Key ON must not over-gate: observe-only RABs and legacy
// no-advertisement runners claim exactly as before.
func TestControlTokenConfiguredKeepsLegacyPasses(t *testing.T) {
	ts, st, _ := ctTestServer(t, ctTestKey)
	gateRegister(t, ts, "wrkr_ct_obs")
	gatePostRAB(t, ts, "wrkr_ct_obs", gateRABObserve)

	w := gateCreateWork(t, ts)
	if code, body := ctClaimRaw(t, ts, "wrkr_ct_obs", w, nil); code != http.StatusCreated {
		t.Fatalf("observe-only RAB under key-on must not need a token: got %d %s", code, body)
	}
	if !gateActiveLeaseNodes(t, st, w)["a"] {
		t.Fatal("observe-only claim must have created an ACTIVE lease")
	}

	w2 := gateCreateWork(t, ts)
	if code, body := ctClaimRaw(t, ts, "wrkr_ct_never_registered", w2, nil); code != http.StatusCreated {
		t.Fatalf("unregistered worker under key-on must be legacy-pass: got %d %s", code, body)
	}
}

// Leak sweep: neither server logs nor response bodies may contain the
// presented token value or its MAC half, in acceptance or denial.
func TestControlTokenNeverAppearsInLogsOrErrors(t *testing.T) {
	ts, st, logs := ctTestServer(t, ctTestKey)
	gateRegister(t, ts, "wrkr_ct_leak")
	gatePostRAB(t, ts, "wrkr_ct_leak", gateRABControl)
	tok, err := api.MintRABControlToken([]byte(ctTestKey), "wrkr_ct_leak")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected token shape: %q", tok)
	}
	macHalf := parts[1]

	// Deny with a foreign-but-well-formed token (binding failure path).
	gateRegister(t, ts, "wrkr_ct_leak2")
	gatePostRAB(t, ts, "wrkr_ct_leak2", gateRABControl)
	w := gateCreateWork(t, ts)
	code, body := ctClaimRaw(t, ts, "wrkr_ct_leak2", w, strptr(tok))
	if code != http.StatusForbidden {
		t.Fatalf("leak sweep claim: got %d, want 403", code)
	}
	if strings.Contains(body, tok) || strings.Contains(body, macHalf) {
		t.Fatalf("response body leaked the token: %s", body)
	}

	// Accept path: still no echo anywhere server-side.
	w2 := gateCreateWork(t, ts)
	code, body = ctClaimRaw(t, ts, "wrkr_ct_leak", w2, strptr(tok))
	if code != http.StatusCreated {
		t.Fatalf("bound token claim: got %d %s, want 201", code, body)
	}
	if strings.Contains(body, tok) || strings.Contains(body, macHalf) {
		t.Fatalf("accept response leaked the token: %s", body)
	}
	if strings.Contains(logs.String(), tok) || strings.Contains(logs.String(), macHalf) {
		t.Fatalf("server log leaked the token: %s", logs.String())
	}
	if strings.Contains(logs.String(), ctTestKey) {
		t.Fatalf("server log leaked the signing key")
	}
	_ = st
}

// Direct mint/verify unit table: format shape and every failure mode of
// the verifier, without the HTTP claim surface in the way.
func TestControlTokenMintVerifyUnit(t *testing.T) {
	key := []byte(ctTestKey)
	srv := &api.Server{RABControlKey: key}
	noKey := &api.Server{}

	tok, err := api.MintRABControlToken(key, "wrkr_unit")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		t.Fatalf("token must be id.MAC: %q", tok)
	}
	if id, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil || string(id) != "wrkr_unit" {
		t.Fatalf("first half must be base64url(runner_id): %q err=%v", parts[0], err)
	}
	if len(parts[1]) != 64 || strings.ToLower(parts[1]) != parts[1] {
		t.Fatalf("second half must be lowercase hex SHA-256 MAC: %q", parts[1])
	}
	if _, err := api.MintRABControlToken(nil, "wrkr_unit"); err == nil {
		t.Fatal("mint with empty key must fail")
	}
	if _, err := api.MintRABControlToken(key, ""); err == nil {
		t.Fatal("mint with empty runner id must fail")
	}

	newReq := func(value string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/leases/grant", nil)
		if value != "" {
			r.Header.Set("X-RAB-Control-Token", value)
		}
		return r
	}
	cases := []struct {
		name   string
		srv    *api.Server
		req    *http.Request
		runner string
		want   bool
	}{
		{"correct", srv, newReq(tok), "wrkr_unit", true},
		{"no-key-server", noKey, newReq(tok), "wrkr_unit", false},
		{"missing-header", srv, newReq(""), "wrkr_unit", false},
		{"garbage", srv, newReq("nope"), "wrkr_unit", false},
		{"extra-dots", srv, newReq(tok + ".extra"), "wrkr_unit", false},
		{"bad-base64", srv, newReq("!!!." + parts[1]), "wrkr_unit", false},
		{"bad-hex", srv, newReq(parts[0] + ".zzzz"), "wrkr_unit", false},
		{"short-mac", srv, newReq(parts[0] + ".deadbeef"), "wrkr_unit", false},
		{"wrong-runner-binding", srv, newReq(tok), "wrkr_other", false},
		{"empty-claimed-id", srv, newReq(tok), "", false},
	}
	for _, tc := range cases {
		if got := tc.srv.VerifyControlToken(tc.req, tc.runner); got != tc.want {
			t.Errorf("%s: VerifyControlToken = %v, want %v", tc.name, got, tc.want)
		}
	}
}
