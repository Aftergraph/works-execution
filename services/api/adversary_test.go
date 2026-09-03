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
// FINDINGS PINNED BELOW:
//   A. cloneRAB (k-053) shallow-copies Extra: nested maps/slices in an
//      advertisement alias the stored record in BOTH directions.
//   B. RuntimeABI.MarshalJSON (k-053) silently destroys client-
//      advertised rab/1.0 fields whose names collide with the linkage
//      keys — breaking the kernel's N-1 round-trip law at the api seam.
//   C. k-053's unconditional upsert composes with the pre-existing
//      zero-auth registration surface (k-002) into an unauthenticated
//      capability-downgrade primitive against ANY known runner id —
//      contradicting runner_abi.go's own claim that "nothing here is
//      weaker" than registration.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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

// TestAdversary_RABCopyOutPromiseBrokenBySharedNestedExtra is a
// REGRESSION test (k-054 finding A, now fixed: cloneRAB re-canonicalises
// Extra through the kernel's JSON shape via Marshal+Unmarshal, yielding
// a complete deep clone of every nested JSON value the kernel accepts).
// Per the file's documented copy-out law, a caller mutating a record
// handed out by getABI/getRuntimeABI/listABI — or mutating the input RAB
// after putABI — must NEVER reach the stored advertisement.
//
// Prior pinned behaviour (k-054 commit 8f7448b before remediation):
//   A1. caller's post-advertisement mutation of in.Extra leaked into the
//       stored RAB (nested map value aliased).
//   A2. reader mutation of a handed-out record's nested Extra value
//       corrupted the advertisement served to every future consumer.
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

// TestAdversary_UnauthenticatedRABDowngradeOfForeignRunner pins finding C.
//
// Scenario: k-053's file header claims "Nothing here is weaker:
// registration itself remains the gate that mints the runner id." True
// for CREATE — but the composition with the pre-existing zero-auth
// surface (runner registry mounted unauthenticated since k-002; any
// client may idempotently re-register or read any runner id) turns the
// idempotent-upsert POST /abi into an unauthenticated capability-DOWNGRADE
// primitive: any network client that can read GET /v1/runners can
// overwrite an existing victim runner's RAB — stripping "control",
// flipping control_token_required, injecting arbitrary N-1 fields —
// and every consumer of GET/negotiate (none yet, scheduler is planned)
// will then negotiate against the forged advertisement. Neither slice
// caught it: k-002 had no capability state; k-053 tested law shape, not
// ownership.
//
// Expected (once the surface is authenticated): attacker POST /abi on a
// foreign runner without any credential is 401/403 and the victim RAB
// is unchanged.
// Observed (pinned): 200 accepted; negotiate for the victim's own
// advertised "control" cap now returns an EMPTY grant — the downgrade
// is live on the wire.
func TestAdversary_UnauthenticatedRABDowngradeOfForeignRunner(t *testing.T) {
	ts := advServer(t)
	advRegister(t, ts, "wrkr_adv_c")

	// Victim legitimately advertises control+observe (control law holds:
	// control_token_required=true).
	code, body := advPostJSON(t, ts.URL+"/v1/runners/wrkr_adv_c/abi",
		`{"abi":"rab/1.0","caps":["observe","control"],"control_token_required":true}`)
	if code != http.StatusOK {
		t.Fatalf("victim POST /abi: %d %s", code, body)
	}

	// Attacker: no identity, no secret, no registration of its own —
	// just the victim's id, which GET /v1/runners exposes to everyone.
	code, body = advPostJSON(t, ts.URL+"/v1/runners/wrkr_adv_c/abi",
		`{"abi":"rab/1.0","caps":["screenshot"]}`)
	if code == http.StatusOK {
		t.Logf("PIN C observed: unauthenticated overwrite of foreign runner RAB ACCEPTED (200)")
	} else {
		t.Fatalf("PIN INVALID: foreign RAB overwrite is gated (status %d body %s) — auth landed; flip to regression", code, body)
	}

	// The downgrade must be effective on the negotiation leg: the
	// victim's own control capability is now gone.
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
	for _, c := range neg.Caps {
		if c == "control" {
			t.Fatalf("unexpected: control still granted after downgrade — pin C premise broken: %v", neg.Caps)
		}
	}
	t.Logf("PIN C observed: victim's negotiate(control) now yields %v — capability downgraded by an anonymous client", neg.Caps)
}
