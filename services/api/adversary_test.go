package api

// Composition-adversary sweep for v0.3.2 (k-054). Fresh-context gate for
// the three slices merged on top of v0.3.1: rab/1.0 registry (k-053),
// secret-ref runtime (k-051), obslaw boundary (k-052). Each slice passed
// its own unit gates; these tests probe only the SEAMS between them and
// between them and pre-existing code.
//
// TEST CONVENTION (required by the k-054 gate: `go test ./...` must stay
// GREEN while the findings are pinned): each TestAdversary_* is a PIN
// test. It asserts the OBSERVED broken composition and passes today, and
// it calls t.Fatal("PIN INVALID ...") — i.e. it starts FAILING the moment
// the underlying bug is fixed — so no test is ever vacuously green. When
// a finding is remediated, the corresponding pin must be flipped into a
// regression assertion; the t.Fatal message says exactly that.
//
// SURFACES VERIFIED CLEAN (no test below, reported as "just works"):
//   - runner.SecretResolver adapter vs kernel secrets.Resolver: the
//     adapter delegates via composition (fresh EnvResolver per call, no
//     caching, errors name refs only). Nil-resolver legacy pass-through
//     holds on the only entry point (Run -> runStep); there is no second
//     exec path in services/runner.
//   - k-051 is UNWIRED in cmd/works-worker (known deviation): the real
//     worker execs item.Env literally (internal/worker/worker.go), so a
//     "secret://..." ref can reach a child env as text — but it is never
//     resolved, cached, echoed into evidence Details, or emitted to
//     audit. Legacy fall-through is safe; the finding is "inert, not
//     wired", documented in the k-054 report, not a violation of any
//     current law.
//   - Produce rejects empty HMACKey/KeyID with errors (no panic);
//     AttestBundle with <32-byte key returns an error through
//     NewVerifier; nil bundle / nil event return errors, not panics.
//     Produce's law hook runs after signing and cannot leak a bundle on
//     error paths (checkBundleLaw on the hardcoded-legal projection is
//     currently untriggerable — verified, matches its docstring).
//   - obslaw attestation is NOT folded into the bundle JSON (Attested is
//     a sibling type; GET /v1/works/{id}/evidence wire shape unchanged).
//     Attested.Record is a pointer out of Attest; Sign does not mutate;
//     concurrent AttestBundle calls on one bundle are byte-identical.
//   - audit evt_/evidence evb_ id prefixes cannot collide.
//   - rabRunnerGate: POST /abi before /register is a clean 404 (no 500,
//     no orphan storage via HTTP). Negotiate on unknown cap is 400;
//     requesting a legal-but-unadvertised cap yields empty intersection
//     by documented design. Registry mu is a single sync.RWMutex
//     guarding byID and abiByRunner consistently; overwrite vs negotiate
//     is lock-atomic (pointer swap).
//
// FINDINGS (original sweep k-054; status as of k-059):
//   A. cloneRAB (k-053) shallow-copies Extra: nested maps/slices in an
//      advertisement alias the stored record in BOTH directions.
//      FIXED + flipped to regression in the k-054 remediation commit.
//   B. RuntimeABI.MarshalJSON (k-053) silently destroys client-
//      advertised rab/1.0 fields whose names collide with the linkage
//      keys — breaking the kernel's N-1 round-trip law at the api seam.
//      FIXED + flipped to regression in the k-054 remediation commit.
//   C. k-053's unconditional upsert composes with the pre-existing
//      zero-auth registration surface (k-002) into an unauthenticated
//      capability-downgrade primitive against ANY known runner id —
//      contradicting runner_abi.go's own claim that "nothing here is
//      weaker" than registration. CLOSED by k-059 (bearer auth on the
//      mutating POST /abi); the pin was flipped to regression, see
//      TestAdversary_UnauthenticatedRABDowngradeBlocked, which also
//      pins the residual per-action authz gap on purpose.

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

	"github.com/JonasAbde/works-execution/packages/abi"
	"github.com/JonasAbde/works-execution/services/runner"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// advServer wires the real router over a temp store (same pattern as
// the k-053 black-box tests, duplicated because this file may not touch
// any other file's helpers).
func advServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "adversary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &Server{Store: st}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

func advRegister(t *testing.T, ts *httptest.Server, id string) {
	t.Helper()
	body := `{"runner_id":"` + id + `","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":[],"os":["linux"],"arch":["amd64"]}}`
	resp, err := http.Post(ts.URL+"/v1/runners/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s: status %d", id, resp.StatusCode)
	}
}

func advPostJSON(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// advPostAuthJSON is the authenticated sibling of advPostJSON (k-059):
// same POST, plus an Authorization: Bearer <token> header.
func advPostAuthJSON(t *testing.T, url, body, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// advGetRaw GETs a public read endpoint and returns the full response
// body, for byte-for-byte before/after equality checks.
func advGetRaw(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAdversary_RABCopyOutPromiseBrokenBySharedNestedExtra is a
// REGRESSION test (k-054 finding A, now fixed: cloneRAB re-canonicalises
// Extra through the kernel's JSON shape via Marshal+Unmarshal, yielding
// a complete deep clone of every nested JSON value the kernel accepts).
// Per the file's documented copy-out law, a caller mutating a record
// handed out by getABI/getRuntimeABI/listABI — or mutating the input RAB
// after putABI — must NEVER reach the stored advertisement.
//
// Prior pinned behaviour (k-054 commit 8f7448b before remediation):
//
//	A1. caller's post-advertisement mutation of in.Extra leaked into the
//	    stored RAB (nested map value aliased).
//	A2. reader mutation of a handed-out record's nested Extra value
//	    corrupted the advertisement served to every future consumer.
func TestAdversary_RABCopyOutPromiseBrokenBySharedNestedExtra(t *testing.T) {
	reg := newRunnerRegistry()
	reg.put(&runner.Identity{RunnerID: "wrkr_adv_a"})

	tr := true
	in := &abi.RAB{
		Abi:                  abi.AbiVersion,
		Caps:                 []string{"observe"},
		ControlTokenRequired: &tr,
		Extra: map[string]any{
			"spec": map[string]any{"tier": "standard"},
			"xs":   []any{"keep"},
		},
	}
	if err := reg.putABI("wrkr_adv_a", in); err != nil {
		t.Fatalf("putABI: %v", err)
	}

	// A1 regression: caller mutating the original input after putABI
	// must NOT reach the stored record.
	in.Extra["spec"].(map[string]any)["tier"] = "ADMIN-SELF"
	got, ok := reg.getABI("wrkr_adv_a")
	if !ok {
		t.Fatal("getABI miss")
	}
	if v := got.Extra["spec"].(map[string]any)["tier"]; v == "ADMIN-SELF" {
		t.Fatalf("REGRESSION (A1): caller's post-advertisement mutation leaked into the stored RAB (tier=%v)", v)
	}

	// A2 regression: reader mutating a handed-out record's nested Extra
	// must NOT reach the stored record.
	rec, ok := reg.getRuntimeABI("wrkr_adv_a")
	if !ok {
		t.Fatal("getRuntimeABI miss")
	}
	rec.RAB.Extra["spec"].(map[string]any)["tier"] = "READER-POISON"
	rec2, _ := reg.getRuntimeABI("wrkr_adv_a")
	all := reg.listABI()
	gotTier := rec2.RAB.Extra["spec"].(map[string]any)["tier"]
	listTier := all[0].RAB.Extra["spec"].(map[string]any)["tier"]
	if gotTier == "READER-POISON" || listTier == "READER-POISON" {
		t.Fatalf("REGRESSION (A2): reader mutation of nested Extra poisoned the stored advertisement (rec2=%v list=%v)", gotTier, listTier)
	}

	// Control (proves the test is about EXTRA nesting, not general
	// breakage): the deep-copied fields ARE isolated — mutating a
	// handed-out Caps slice does NOT corrupt the store.
	got.Caps[0] = "input"
	if again, _ := reg.getABI("wrkr_adv_a"); again.Caps[0] != "observe" {
		t.Fatalf("unexpected: Caps copy-out also broken (%v) — different bug than pinned", again.Caps)
	}
}

// TestAdversary_RABFlattenDestroysCollidingAdvertisedField is a REGRESSION
// test (k-054 finding B, now fixed: RuntimeABI.MarshalJSON namespaces
// linkage under "rab_runtime_meta" instead of overlaying on the flattened
// RAB map). The advertised document must round-trip bit-for-bit through
// the GET response even when an N-1 field collides with a server linkage
// name.
//
// Prior pinned behaviour (k-054 commit 8f7448b before remediation):
// POST 200, GET returned server-stamp "registered_at" and the client's
// advertised value was silently destroyed.
func TestAdversary_RABFlattenDestroysCollidingAdvertisedField(t *testing.T) {
	ts := advServer(t)
	advRegister(t, ts, "wrkr_adv_b")

	clientStamp := "client-advertised-registered-at"
	body := `{"abi":"rab/1.0","caps":["observe"],"registered_at":"` + clientStamp + `","x_meta":{"keep":true}}`
	code, postBody := advPostJSON(t, ts.URL+"/v1/runners/wrkr_adv_b/abi", body)
	if code != http.StatusOK {
		t.Fatalf("POST /abi: status %d body %s", code, postBody)
	}

	resp, err := http.Get(ts.URL + "/v1/runners/wrkr_adv_b/abi")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rec map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		t.Fatal(err)
	}

	// Control: non-colliding extra survives the flatten.
	if _, ok := rec["x_meta"]; !ok {
		t.Fatalf("unexpected: x_meta extra lost too — wire shape changed: %v", rec)
	}
	// Regression: the advertised field round-trips. The server stamps
	// "registered_at" under the namespaced "rab_runtime_meta" key.
	got, ok := rec["registered_at"].(string)
	if !ok || got != clientStamp {
		t.Fatalf("REGRESSION: advertised registered_at did not round-trip; got=%v want=%q full=%v", rec["registered_at"], clientStamp, rec)
	}
	meta, ok := rec["rab_runtime_meta"].(map[string]any)
	if !ok {
		t.Fatalf("REGRESSION: rab_runtime_meta linkage missing on GET: %v", rec)
	}
	if _, ok := meta["registered_at"].(string); !ok {
		t.Fatalf("REGRESSION: rab_runtime_meta.registered_at not a server-stamped time string: %v", meta)
	}
}

// TestAdversary_UnauthenticatedRABDowngradeBlocked is a REGRESSION test
// for k-054 finding C, CLOSED by k-059.
//
// History: found by k-054 (composition-adversary sweep) as
// TestAdversary_UnauthenticatedRABDowngradeOfForeignRunner: k-053's
// idempotent upsert composed with k-002's zero-auth runner surface into
// an unauthenticated capability-DOWNGRADE primitive: any network client
// could overwrite any runner's RAB, and victims' negotiate then returned
// an empty grant. Closed by k-059: POST /v1/runners/{id}/abi is mounted
// behind requireBearer in Routes(). Anonymous downgrade now answers 401
// "missing_authorization" and the victim's stored RAB is unchanged
// (byte-for-byte GET equality). Reads (GET /abi, negotiate) stay
// unauthenticated, matching the public identity reads.
//
// RESIDUAL, pinned ON PURPOSE by the second leg below: bearer proves
// token VALIDITY, not OWNERSHIP. Any valid worker token may still
// rewrite ANY runner's RAB; per-runner authorization (which token may
// rewrite which runner) remains an open per-action authz slice, matching
// auth.go's note that requireBearer is NOT a substitute for per-action
// authz.
func TestAdversary_UnauthenticatedRABDowngradeBlocked(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "adversary-c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &Server{Store: st, AuthEnabled: true}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	advRegister(t, ts, "wrkr_adv_c")

	mint := func(workerID string) string {
		t.Helper()
		tok, err := srv.Auth.Mint(context.Background(), workerID, time.Hour)
		if err != nil {
			t.Fatalf("mint token for %s: %v", workerID, err)
		}
		return tok
	}

	// Victim legitimately advertises control+observe over the now-authed
	// mutating route (control law holds: control_token_required=true).
	victimTok := mint("wrkr_adv_c")
	code, body := advPostAuthJSON(t, ts.URL+"/v1/runners/wrkr_adv_c/abi",
		`{"abi":"rab/1.0","caps":["observe","control"],"control_token_required":true}`, victimTok)
	if code != http.StatusOK {
		t.Fatalf("victim authenticated POST /abi: %d %s", code, body)
	}
	before := advGetRaw(t, ts.URL+"/v1/runners/wrkr_adv_c/abi")

	// Leg 1 (finding C, closed): attacker with no identity, no secret,
	// no token — just the victim's id, which GET /v1/runners exposes to
	// everyone — must now be refused.
	code, body = advPostJSON(t, ts.URL+"/v1/runners/wrkr_adv_c/abi",
		`{"abi":"rab/1.0","caps":["screenshot"]}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("REGRESSION: anonymous downgrade POST /abi returned %d (%s), want 401 — k-059 bearer auth on the mutating abi route is gone", code, body)
	}
	if !strings.Contains(string(body), "missing_authorization") {
		t.Fatalf("REGRESSION: 401 must name missing_authorization, got %s", body)
	}
	if after := advGetRaw(t, ts.URL+"/v1/runners/wrkr_adv_c/abi"); !bytes.Equal(before, after) {
		t.Fatalf("REGRESSION: victim RAB changed despite the 401: before=%s after=%s", before, after)
	}
	// The refused downgrade must not be effective on the negotiation
	// leg either: the victim keeps its own control capability.
	code, body = advPostJSON(t, ts.URL+"/v1/runners/wrkr_adv_c/abi/negotiate",
		`{"caps":["control"]}`)
	if code != http.StatusOK {
		t.Fatalf("negotiate: %d %s", code, body)
	}
	var neg struct {
		Caps []string `json:"caps"`
	}
	if err := json.Unmarshal(body, &neg); err != nil {
		t.Fatal(err)
	}
	grantedControl := false
	for _, c := range neg.Caps {
		if c == "control" {
			grantedControl = true
		}
	}
	if !grantedControl {
		t.Fatalf("REGRESSION: control gone from negotiate after a refused anonymous POST — downgrade still live: %v", neg.Caps)
	}

	// Leg 2 (RESIDUAL, asserted intentionally): an authenticated request
	// with a valid token for an UNRELATED worker still rewrites the
	// victim's RAB. Authentication landed; per-runner authz did not.
	attackerTok := mint("wrkr_adv_c_attacker")
	code, body = advPostAuthJSON(t, ts.URL+"/v1/runners/wrkr_adv_c/abi",
		`{"abi":"rab/1.0","caps":["screenshot"]}`, attackerTok)
	if code != http.StatusOK {
		t.Fatalf("residual-gap premise broken: foreign-token authenticated downgrade returned %d (%s) — per-action authz may have landed; move this assertion to the authz slice", code, body)
	}
	code, body = advPostJSON(t, ts.URL+"/v1/runners/wrkr_adv_c/abi/negotiate",
		`{"caps":["control"]}`)
	if code != http.StatusOK {
		t.Fatalf("negotiate after authed downgrade: %d %s", code, body)
	}
	if err := json.Unmarshal(body, &neg); err != nil {
		t.Fatal(err)
	}
	for _, c := range neg.Caps {
		if c == "control" {
			t.Fatalf("unexpected: control still granted after authenticated downgrade — leg 2 premise broken: %v", neg.Caps)
		}
	}
	t.Logf("RESIDUAL pinned: any valid bearer token may rewrite any runner's RAB (victim negotiate now %v) — per-action authz remains an open slice", neg.Caps)
}
