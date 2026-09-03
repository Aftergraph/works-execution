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
// FINDINGS (original sweep k-054; status as of k-061):
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
//      mutating POST /abi); the k-059 residual (any valid token could
//      rewrite ANY runner, and register stayed anonymous) CLOSED by
//      k-061 (bearer + worker_id == runner_id ownership on the register
//      and abi-mutate paths). The pin was flipped to regression, see
//      TestAdversary_RABDowngradeClosedAnonymousAndForeignToken.

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

// advGetAuthRaw is the k-061 sibling of advGetRaw: same GET, plus an
// Authorization: Bearer *** header. The abi reads moved behind bearer
// in k-061, so the auth-enabled adversary server needs this variant.
func advGetAuthRaw(t *testing.T, url, token string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d body %s", url, resp.StatusCode, b)
	}
	return b
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

// TestAdversary_RABDowngradeClosedAnonymousAndForeignToken is the
// REGRESSION test for k-054 finding C, FULLY CLOSED across k-059 and
// k-061.
//
// History: found by k-054 (composition-adversary sweep) as
// TestAdversary_UnauthenticatedRABDowngradeOfForeignRunner: k-053's
// idempotent upsert composed with k-002's zero-auth runner surface into
// an unauthenticated capability-DOWNGRADE primitive: any network client
// could overwrite any runner's RAB, and victims' negotiate then returned
// an empty grant.
//
// Half-closed by k-059: POST /abi moved behind requireBearer, so the
// anonymous downgrade answers 401. The residual - a VALID token may
// still rewrite ANY runner's RAB, and register was still anonymous -
// was pinned on purpose by the old leg 2 (t.Fatal'd once per-action
// authz landed).
//
// Fully closed by k-061 (runner_authz.go): POST /v1/runners/register is
// bearer-gated and identity-bound, POST /abi enforces worker_id ==
// runner_id ownership, and the whole abi surface is bearer. This test
// now asserts BOTH halves:
//   - anonymous register: 401 missing_authorization, registry untouched
//   - foreign-token register: 403 not_runner_owner, registry untouched
//   - anonymous abi write: 401; victim state byte-identical
//   - foreign-token abi write: 403 not_runner_owner; victim state
//     byte-identical and its control cap survives negotiation
//   - own-token register (the production worker path, exact match) and
//     own-token abi write: succeed
func TestAdversary_RABDowngradeClosedAnonymousAndForeignToken(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "adversary-c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &Server{Store: st, AuthEnabled: true}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	mint := func(workerID string) string {
		t.Helper()
		tok, err := srv.Auth.Mint(context.Background(), workerID, time.Hour)
		if err != nil {
			t.Fatalf("mint token for %s: %v", workerID, err)
		}
		return tok
	}
	ident := func(id string) string {
		return `{"runner_id":"` + id + `","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":[],"os":["linux"],"arch":["amd64"]}}`
	}
	registerURL := ts.URL + "/v1/runners/register"
	abiURL := ts.URL + "/v1/runners/wrkr_adv_c/abi"
	negURL := abiURL + "/negotiate"

	victimTok := mint("wrkr_adv_c")
	attackerTok := mint("wrkr_adv_c_attacker")

	// Leg 0 (k-061 half 1): registration is no longer an anonymous
	// identity-minting / foreign-heartbeat-flood primitive.
	code, body := advPostJSON(t, registerURL, ident("wrkr_adv_c"))
	if code != http.StatusUnauthorized {
		t.Fatalf("REGRESSION: anonymous POST /v1/runners/register returned %d (%s), want 401 — k-061 bearer auth on registration is gone", code, body)
	}
	if !strings.Contains(string(body), "missing_authorization") {
		t.Fatalf("REGRESSION: anonymous register 401 must name missing_authorization, got %s", body)
	}
	// A valid but UNRELATED token may not register a foreign runner_id
	// either (identity binding before any mint/store).
	code, body = advPostAuthJSON(t, registerURL, ident("wrkr_adv_c"), attackerTok)
	if code != http.StatusForbidden {
		t.Fatalf("REGRESSION: foreign-token register returned %d (%s), want 403 — k-061 identity binding on registration is gone", code, body)
	}
	if !strings.Contains(string(body), "not_runner_owner") {
		t.Fatalf("REGRESSION: foreign-token register 403 must name not_runner_owner, got %s", body)
	}
	// Both refusals left the registry untouched: the victim does not exist.
	if resp, err := http.Get(ts.URL + "/v1/runners/wrkr_adv_c"); err != nil {
		t.Fatal(err)
	} else {
		if resp.StatusCode != http.StatusNotFound {
			resp.Body.Close()
			t.Fatalf("REGRESSION: refused registrations created the runner (GET /v1/runners/wrkr_adv_c = %d)", resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Victim legitimately registers and advertises control+observe over
	// the bearer-gated surface (exact-match path used by production
	// workers, internal/worker registerRunner: runner_id == worker_id).
	if code, body = advPostAuthJSON(t, registerURL, ident("wrkr_adv_c"), victimTok); code != http.StatusCreated {
		t.Fatalf("victim authenticated register: %d %s", code, body)
	}
	if code, body = advPostAuthJSON(t, abiURL,
		`{"abi":"rab/1.0","caps":["observe","control"],"control_token_required":true}`, victimTok); code != http.StatusOK {
		t.Fatalf("victim authenticated POST /abi: %d %s", code, body)
	}
	before := advGetAuthRaw(t, abiURL, victimTok)

	// Leg 1 (finding C, k-059): attacker with no identity, no secret, no
	// token — just the victim's id, which GET /v1/runners exposes to
	// everyone — is refused, and the victim's advertisement is
	// byte-identical afterwards.
	code, body = advPostJSON(t, abiURL, `{"abi":"rab/1.0","caps":["screenshot"]}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("REGRESSION: anonymous downgrade POST /abi returned %d (%s), want 401 — k-059 bearer auth on the mutating abi route is gone", code, body)
	}
	if !strings.Contains(string(body), "missing_authorization") {
		t.Fatalf("REGRESSION: 401 must name missing_authorization, got %s", body)
	}
	// k-061: the abi READS moved behind bearer too — anonymous
	// reconnaissance of the advertisement now needs a token.
	if resp, err := http.Get(abiURL); err != nil {
		t.Fatal(err)
	} else {
		if resp.StatusCode != http.StatusUnauthorized {
			resp.Body.Close()
			t.Fatalf("REGRESSION: anonymous GET /abi returned %d, want 401 — k-061 bearer reads are gone", resp.StatusCode)
		}
		resp.Body.Close()
	}
	if after := advGetAuthRaw(t, abiURL, victimTok); !bytes.Equal(before, after) {
		t.Fatalf("REGRESSION: victim RAB changed despite the 401: before=%s after=%s", before, after)
	}

	// Leg 2 (k-059 RESIDUAL, closed by k-061): a VALID token for an
	// UNRELATED worker can no longer rewrite the victim's RAB.
	code, body = advPostAuthJSON(t, abiURL, `{"abi":"rab/1.0","caps":["screenshot"]}`, attackerTok)
	if code != http.StatusForbidden {
		t.Fatalf("REGRESSION: foreign-token authenticated downgrade returned %d (%s), want 403 — k-061 per-runner ownership on POST /abi is gone", code, body)
	}
	if !strings.Contains(string(body), "not_runner_owner") {
		t.Fatalf("REGRESSION: 403 must name not_runner_owner, got %s", body)
	}
	if after := advGetAuthRaw(t, abiURL, victimTok); !bytes.Equal(before, after) {
		t.Fatalf("REGRESSION: victim RAB changed despite the 403: before=%s after=%s", before, after)
	}
	// The refused downgrade is not effective on the negotiation leg
	// either: the victim keeps its own control capability. Negotiation
	// is a bearer READ (any enrolled caller may ask; k-061), so the
	// attacker's own token can probe it — it just cannot change it.
	code, body = advPostAuthJSON(t, negURL, `{"caps":["control"]}`, attackerTok)
	if code != http.StatusOK {
		t.Fatalf("negotiate (bearer read): %d %s", code, body)
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
		t.Fatalf("REGRESSION: control gone from negotiate after refused downgrades — downgrade still live: %v", neg.Caps)
	}

	// Control (proves the gate is ownership-scoped, not blanket-hostile):
	// the owner may still re-advertise a smaller RAB, and the change is
	// visible on the bearer read.
	code, body = advPostAuthJSON(t, abiURL, `{"abi":"rab/1.0","caps":["screenshot"]}`, victimTok)
	if code != http.StatusOK {
		t.Fatalf("owner re-advertise own RAB: %d %s", code, body)
	}
	if bytes.Equal(before, advGetAuthRaw(t, abiURL, victimTok)) {
		t.Fatal("unexpected: owner's own POST /abi did not change the stored advertisement")
	}
}
