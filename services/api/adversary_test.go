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

// TestAdversary_RABCopyOutPromiseBrokenBySharedNestedExtra pins finding A.
//
// Scenario: services/runner_abi.go cloneRAB documents that "a caller
// mutating a record handed out by getABI/getRuntimeABI/listABI can
// NEVER corrupt the stored advertisement" and claims to be "deep enough
// for RAB's reference fields". It copies the Caps slice, the
// ControlTokenRequired pointer — and rebuilds Extra as a NEW map — but
// copies its VALUES verbatim (out.Extra[k] = v). rab/1.0 N-1 tolerance
// (kernel: Extra is map[string]any from json.Unmarshal, so ANY nested
// object/array is a shared reference). The k-053 unit gate only ever
// mutated top-level fields (Caps[0]) — a slice the author did deep-copy
// — and stored Extra values only as scalars in the e2e wire tests. The
// seam between the kernel's arbitrary-JSON Extra shape and the api's
// copy-out discipline was tested by neither side.
//
// Expected (per the docstring law): mutation through ANY handed-out
// record, or through the caller's original input RAB, leaves the stored
// advertisement byte-identical.
// Observed (pinned): both directions corrupt the stored record —
// a reader (or the advertiser reusing its object) can rewrite what every
// future GET and the (future) dispatcher negotiate against.
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

	// Direction 1 (advertiser-side aliasing): the caller keeps its own
	// RAB object and mutates a nested Extra value after advertising.
	in.Extra["spec"].(map[string]any)["tier"] = "ADMIN-SELF"
	got, ok := reg.getABI("wrkr_adv_a")
	if !ok {
		t.Fatal("getABI miss")
	}
	if v := got.Extra["spec"].(map[string]any)["tier"]; v == "ADMIN-SELF" {
		// Observed corruption — pinned finding stands.
		t.Logf("PIN A1 observed: caller's post-advertisement mutation leaked into the stored RAB (tier=%v)", v)
	} else {
		t.Fatalf("PIN INVALID (A1): stored record isolated from caller input (tier=%v) — deep copy was added; flip this into a regression assertion", v)
	}

	// Direction 2 (reader-side aliasing): mutate a nested Extra value
	// through a record handed out by getRuntimeABI.
	rec, ok := reg.getRuntimeABI("wrkr_adv_a")
	if !ok {
		t.Fatal("getRuntimeABI miss")
	}
	rec.RAB.Extra["spec"].(map[string]any)["tier"] = "READER-POISON"
	// The list snapshot must agree with an independent read — and both
	// must be the ORIGINAL advertisement if the copy-out law held.
	rec2, _ := reg.getRuntimeABI("wrkr_adv_a")
	all := reg.listABI()
	if v := rec2.RAB.Extra["spec"].(map[string]any)["tier"]; v != "READER-POISON" ||
		v != all[0].RAB.Extra["spec"].(map[string]any)["tier"] {
		t.Fatalf("PIN INVALID (A2): nested mutation through a handed-out record no longer reaches the store (rec2=%v, list=%v) — fix landed; flip to regression", v, all[0].RAB.Extra["spec"])
	}
	t.Logf("PIN A2 observed: reader mutation of nested Extra corrupts the advertisement served to every future consumer")

	// Control (proves the test is about EXTRA nesting, not general
	// breakage): the deep-copied fields ARE isolated — mutating a
	// handed-out Caps slice does NOT corrupt the store.
	got.Caps[0] = "input"
	if again, _ := reg.getABI("wrkr_adv_a"); again.Caps[0] != "observe" {
		t.Fatalf("unexpected: Caps copy-out also broken (%v) — different bug than pinned", again.Caps)
	}
}

// TestAdversary_RABFlattenDestroysCollidingAdvertisedField pins finding B.
//
// Scenario: a runtime advertises a rab/1.0 document that uses N-1
// tolerance for a field named "registered_at" (perfectly legal: the
// kernel's Extra map accepts any unknown top-level field, and
// abi.RAB.MarshalJSON round-trips it — "unknown top-level fields
// round-trip" is the frozen kernel law, proto.charter/1.0 ADR-0021).
// The k-053 GET/POST response flattens the record by OVERLAYING the
// server linkage keys runner_id/registered_at on top of the marshalled
// RAB map (RuntimeABI.MarshalJSON: m["runner_id"]=...; m["registered_at"]
// = ...). The kernel and the endpoint were each correct in isolation;
// the composition silently drops advertised contract data with no error.
//
// Expected: either POST rejects the colliding field (fail-closed), or
// the GET response carries the advertised value — the document must
// round-trip.
// Observed (pinned): POST accepts (200), GET returns server time, and
// the client's advertised value is gone everywhere — while a
// non-colliding extra field (x_meta) survives, proving the store kept
// the Extra and only the flatten-on-wire step destroys it.
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
	// Pin the collision-loss: the advertised value must be absent.
	got, _ := rec["registered_at"].(string)
	if got == clientStamp {
		t.Fatalf("PIN INVALID: advertised registered_at round-trips (%v) — the flatten collision was fixed; flip to regression", got)
	}
	if !strings.Contains(got, "T") {
		t.Fatalf("unexpected registered_at shape %q — different bug than pinned", got)
	}
	t.Logf("PIN B observed: advertised %q silently replaced by server stamp %q; N-1 round-trip law broken at the api flatten seam", clientStamp, got)
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
