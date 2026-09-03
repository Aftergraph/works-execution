package api

// k-064: composition-adversary sweep of the four-slice v0.3.4 authz stack.
//
// Fresh-context gate (the layer that caught k-050 / k-054): k-060 (claim
// owner gate), k-058+k-062 (claim RAB / control-token gate) and k-061 (bearer
// + ownership on the runner registry) are each green in isolation. This file
// probes only the SEAMS: between the four gates, and between them and the
// pre-existing surface (k-053 order law, the non-grant lease verbs, docs,
// production wiring).
//
// TEST CONVENTION (k-054, required): every test passes TODAY and pins
// OBSERVED behavior. Where the observation is a finding, the test asserts the
// broken composition, so it starts failing the moment the bug is fixed; each
// docstring states exactly what must flip. Nothing here fixes anything and no
// other file is touched.
//
// COVERAGE GAP CLOSED (a deliverable, not a finding): before k-064 no test in
// the repo set AuthEnabled=true together with RABControlKey — `grep -l
// RABControlKey **/*_test.go` hit only rab_control_token_test.go, whose
// fixture never sets AuthEnabled (dev mode). The (auth=on x key=on) cell of
// the claim gate is now covered by
// TestAdversary34_ClaimGateCombinedMatrix.
//
// SURFACES VERIFIED CLEAN (pinned; nothing to fix):
//   - Gate order at grant (TestAdversary34_ClaimGateOrderOwnerFirst):
//     400 malformed -> k-060 owner -> k-058/k-062 claim gate -> store. The
//     owner gate denies a mismatched claim even when the victim's RAB would
//     ALSO deny it, and no denial touches lease or work state.
//   - No normalization asymmetry reaches the store: the owner gate is exact
//     equality, so " wrkr_a", "WRKR_A" and "a/../x" are 403 under auth
//     (registry lookup is exact-match too — grep: nothing trims or lowers in
//     the claim/registry path; auth.go:328 trims only the bearer value and
//     enroll.go:78 only at enrollment, both on the trusted side).
//   - No control-token validity oracle (TestAdversary34_NoControlTokenOracle):
//     a well-formed token bound to ANOTHER runner, a malformed token and a
//     corrupted-MAC token yield byte-identical 403 bodies. A denial never
//     reveals "this token verified but for someone else", so the k-062 key
//     cannot be probed. Only missing-vs-present differs
//     (control_token_required vs control_token_invalid) for k-058 compat,
//     which leaks nothing about the key.
//   - Denial order never enumerates a runner for an UNAUTHENTICATED caller
//     (401 first, always). For an authenticated caller 404(runner_not_found)
//     precedes 403(not_runner_owner) — pinned as harmless in
//     TestAdversary34_ABISurfaceDenialOrder because GET /v1/runners and
//     GET /v1/runners/{id} are public on the same server, so registry
//     membership is already unauthenticated-readable.
//   - k-061 cannot be raced into a write: a denied register/abi leaves the
//     registry byte-identical, and a shape-invalid runner_id that PASSES the
//     equality gate is refused by Validate before any put
//     (TestAdversary34_RegisterGatePrecedesShape).
//   - Each denial writes exactly one JSON error object and returns
//     (TestAdversary34_SingleErrorWritePerDenial) — the grantLease seam has
//     one writeError per path, and the denial-branch log line that
//     dereferences ClaimsFrom is unreachable with nil claims, so no panic.
//   - k-062's runbook (docs/runbooks/control-tokens.md) matches the observed
//     wire behavior exactly in both key modes.
//   - No second grant path: Store.GrantLease has exactly one HTTP-reachable
//     caller (leases.go:214); GrantLeaseEventful has none.
//   - rabControlTokenHeader is defined once (claim_abi_gate.go:62, k-058's
//     file) and only referenced by k-062's file; no duplicated const/helper
//     and no shadowing between the two new gate files.
//
// FINDINGS (all reproduced above by a named test; severity per k-054):
//
//	A. HIGH — k-061 breaks the operator CLI in production.
//	   cmd/works-runner-id -register <api-url> (advertised in the tool's own
//	   usage block, main.go:8) POSTs /v1/runners/register with Content-Type
//	   and NO Authorization header (main.go:83-99: the only header it sets is
//	   Content-Type at :89, and any non-2xx answer is a log.Fatalf at :97;
//	   the tool has no enroll path and no token flag), while
//	   cmd/works-api/main.go:75 hardcodes AuthEnabled: true. Post-k-061 every
//	   prod run of that flag dies at "register: API returned 401
//	   Unauthorized". No runbook uses the flag (grep docs/), and CI cannot
//	   see it: e2e/e2e_test.go:44 builds the server with AuthEnabled=false.
//	   TestAdversary34_RunnerIDCLINoBearerBreaksInProd.
//
//	B. MEDIUM — the k-058/k-062 control-token law is evadable by a
//	   never-registrable token identity (prod posture: auth ON + key ON).
//	   Enrollment accepts [A-Za-z0-9_.-]{1,128} (enroll.go:130) but a runner
//	   id must match ^wrkr_[a-z0-9_-]{1,64}$ (services/runner/registry.go:74).
//	   A worker enrolled as "WRKR_A" passes k-060 (claims == body), can NEVER
//	   register a runner (400 validation_failed), so it permanently lands in
//	   k-058's legacy-pass class and its claims are granted with no control
//	   token at all. k-061's equality gate is satisfied by an identity that
//	   the registry structurally cannot hold.
//	   TestAdversary34_EnrollmentCharsetLegacyPass.
//
//	C. MEDIUM (dev-mode posture only, i.e. every e2e/local server) — the same
//	   law bypassed by a whitespace lookalike, plus identity spoofing in the
//	   durable record: with AuthEnabled=false, claiming as "wrkr_a " skips the
//	   gate (exact-match map lookup finds no RAB) and the store persists
//	   worker_id "wrkr_a " on the attempt and lease next to the real
//	   "wrkr_a". Unreachable with auth on (k-060 denies), so this is a
//	   defense-in-depth hole, not a prod hole.
//	   TestAdversary34_DevModeLookalikeWorkerIDEscapesGate (+ the
//	   padded-claim rows of the matrix).
//
//	D. MEDIUM residual — k-060's per-action authz covers the GRANT verb only:
//	   any valid worker token may revoke / release / complete / heartbeat
//	   ANOTHER worker's lease by id (heartbeatLease, completeLease,
//	   releaseLease, revokeLease never call ClaimsFrom). Reproduced
//	   cross-identity: attacker token revokes the victim's ACTIVE lease ->
//	   200 and the lease row is REVOKED. Mitigated (pinned in the same test):
//	   lease ids are crypto/rand 128-bit and appear on no read surface — not
//	   in the Work JSON, not in the audit stream even when read with an
//	   unrelated token. So exploitation needs an out-of-band id leak, which
//	   is exactly why the verbs should be bound. AUTH.md's `/v1/leases/*`
//	   row is true about bearer but reads as if the verbs were authorized.
//	   TestAdversary34_NonGrantLeaseVerbsUnbound.
//
//	E. MEDIUM doc-drift — docs/AUTH.md (edited by k-061) misstates the
//	   boundary in two rows and omits a whole law:
//	   - AUTH.md:8 puts `/v1/works/*` in the "Endpoints requiring Bearer
//	     auth" table, but api.go:177 mounts the "/v1/works/" prefix WITHOUT
//	     requireBearer: anonymous GET /v1/works/{id} -> 200 and anonymous
//	     POST /v1/works/{id}/cancel -> 200 (a real state mutation). The
//	     route comment in api.go even says so ("remain unauthenticated
//	     (operator surface)"): the doc row is false, not the code.
//	     TestAdversary34_AuthMDWorksSubtreeRowIsFalse.
//	   - AUTH.md:21 lists `/readyz` as a public endpoint; no such route
//	     exists (404 page not found), and the doc never mentions the
//	     k-058/k-062 control-token law, WORKS_RAB_CONTROL_TOKEN, or the
//	     bearer-gated /v1/cache/, /v1/works/{id}/events, /resume, /suspend,
//	     /handoff and /v1/brain/ surfaces.
//	     TestAdversary34_AuthMDOmitsControlLawAndBearerRows.
//
//	F. INFO — k-061 inlines its reason string ("not_runner_owner",
//	   runner_authz.go:62) where k-060/k-058/k-062 export stable constants.
//	   Pinned as a wire-constant assertion so a rename cannot silently break
//	   clients or the k-064 pins. TestAdversary34_StableReasonCodesOnTheWire.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/runner"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// a34AuthPrefix is the Authorization scheme value (auth.go:323 uses the same
// literal); kept in one place so no test repeats the string.
const a34AuthPrefix = "Bearer "

// a34EnrollSecret is the enrollment challenge for the HTTP enroll path.
const a34EnrollSecret = "k066-enrollment-charset-secret"

// a34CtlKey is a fixed control-token key: test-only, never leaves the process.
const a34CtlKey = "k064-composition-adversary-key-0123456789abcdef"

// Advertisements. These are local copies of the shapes in
// runner_authz_test.go / rab_control_token_test.go: this file must not depend
// on another file's identifiers for its wire expectations.
const (
	a34rabCtl   = `{"abi":"rab/1.0","caps":["observe","control"],"control_token_required":true}`
	a34rabPlain = `{"abi":"rab/1.0","caps":["observe"]}`
)

// a34res is one HTTP answer, raw.
type a34res struct {
	code int
	body []byte
}

func (r a34res) text() string { return string(r.body) }

func (r a34res) field(name string) string {
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		return ""
	}
	if v, ok := m[name].(string); ok {
		return v
	}
	return ""
}

// errCode is the stable error field of the api's errBody shape. A body that
// is not a single JSON object reports the raw bytes, which is what the
// single-write check wants to see.
func (r a34res) errCode() string {
	var b struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(r.body, &b); err != nil {
		return "<not-single-json>"
	}
	return b.Error
}

// a34 is a real router over a temp SQLite store in any (auth, key) posture.
type a34 struct {
	t   *testing.T
	ts  *httptest.Server
	srv *Server
	st  store.Store
	log *bytes.Buffer
}

func a34New(t *testing.T, authOn, keyOn bool) *a34 {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a34.db"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	srv := &Server{Store: st, AuthEnabled: authOn, Logger: log.New(&buf, "", 0)}
	// a34EnrollSecret lets the k-066 test drive the REAL enrollment
	// endpoint (charset law at the mint entry point, not Auth.Mint
	// directly — direct mint bypasses enrollment by fixture design).
	srv.EnrollSecret = a34EnrollSecret
	if keyOn {
		srv.RABControlKey = []byte(a34CtlKey)
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = st.Close() })
	return &a34{t: t, ts: ts, srv: srv, st: st, log: &buf}
}

// mint signs an enrollment (bearer) JWT through the same issuer the
// middleware verifies (Routes() constructs it lazily via ensureIssuer).
func (f *a34) mint(workerID string) string {
	f.t.Helper()
	tok, err := f.srv.Auth.Mint(context.Background(), workerID, time.Hour)
	if err != nil {
		f.t.Fatal(err)
	}
	return tok
}

// ctlToken mints a k-062 RAB control token under this server's key.
func (f *a34) ctlToken(runnerID string) string {
	f.t.Helper()
	tok, err := MintRABControlToken(f.srv.RABControlKey, runnerID)
	if err != nil {
		f.t.Fatal(err)
	}
	return tok
}

func (f *a34) do(method, path, body, bearer, controlHdr string, sendControl bool) a34res {
	f.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.ts.URL+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", a34AuthPrefix+bearer)
	}
	if sendControl {
		req.Header.Set("X-RAB-Control-Token", controlHdr)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return a34res{code: resp.StatusCode, body: b}
}

func (f *a34) get(path, bearer string) a34res {
	f.t.Helper()
	return f.do(http.MethodGet, path, "", bearer, "", false)
}

func (f *a34) post(path, body, bearer string) a34res {
	f.t.Helper()
	return f.do(http.MethodPost, path, body, bearer, "", false)
}

// a34IdentBody is a schema-valid runner.Identity — byte-for-byte the shape
// cmd/works-runner-id marshals and POSTs (with labels omitted, which the
// schema tolerates).
func a34IdentBody(runnerID string) string {
	return `{"runner_id":"` + runnerID + `","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":[],"os":["linux"],"arch":["amd64"]}}`
}

func (f *a34) registerAs(runnerID, bearer string) a34res {
	f.t.Helper()
	return f.post("/v1/runners/register", a34IdentBody(runnerID), bearer)
}

func (f *a34) advertise(runnerID, rab, bearer string) a34res {
	f.t.Helper()
	return f.post("/v1/runners/"+runnerID+"/abi", rab, bearer)
}

// mustAdvertiseControl registers runnerID and pins a control RAB on it using
// whatever identity the current posture requires (own token when auth is on).
func (f *a34) mustAdvertiseControl(runnerID string) {
	f.t.Helper()
	bearer := ""
	if f.srv.AuthEnabled {
		bearer = f.mint(runnerID)
	}
	if r := f.registerAs(runnerID, bearer); r.code != http.StatusCreated && r.code != http.StatusOK {
		f.t.Fatalf("register %s: %d %s", runnerID, r.code, r.text())
	}
	if r := f.advertise(runnerID, a34rabCtl, bearer); r.code != http.StatusOK {
		f.t.Fatalf("advertise %s: %d %s", runnerID, r.code, r.text())
	}
}

// work creates a QUEUED single-node ("a") work straight in the store, so the
// fixture never depends on the separately-tested /v1/works posture.
func (f *a34) work() string {
	f.t.Helper()
	w := &workgraph.Work{
		ID:        workgraph.NewID("w"),
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "echo a"}}},
		State:     workgraph.StateQueued,
	}
	if err := f.st.CreateWork(context.Background(), w); err != nil {
		f.t.Fatal(err)
	}
	return w.ID
}

// claim is POST /v1/leases/grant for workerID on node "a". sendControl=false
// omits the header entirely; sendControl=true with "" presents an empty one.
func (f *a34) claim(workerID, workID, bearer, controlHdr string, sendControl bool) a34res {
	f.t.Helper()
	body, err := json.Marshal(map[string]any{
		"work_id": workID, "node_id": "a", "worker_id": workerID, "ttl_seconds": 25,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return f.do(http.MethodPost, "/v1/leases/grant", string(body), bearer, controlHdr, sendControl)
}

// mustGrant claims and requires 201, returning the lease id.
func (f *a34) mustGrant(workerID, workID, bearer, controlHdr string, sendControl bool) string {
	f.t.Helper()
	r := f.claim(workerID, workID, bearer, controlHdr, sendControl)
	if r.code != http.StatusCreated {
		f.t.Fatalf("claim as %s: %d %s", workerID, r.code, r.text())
	}
	var out struct {
		Lease workgraph.Lease `json:"lease"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		f.t.Fatal(err)
	}
	if out.Lease.ID == "" {
		f.t.Fatalf("claim returned no lease id: %s", r.text())
	}
	return out.Lease.ID
}

// mustAssertDenial pins a denial AND its side-effect-freedom: no active
// lease, work state unmoved. That is the deterministic proof the gate
// precedes the store.
func (f *a34) mustAssertDenial(label string, r a34res, wantCode int, wantErr, workID string) {
	f.t.Helper()
	if r.code != wantCode {
		f.t.Fatalf("%s: status %d want %d (%s)", label, r.code, wantCode, r.text())
	}
	if got := r.errCode(); got != wantErr {
		f.t.Fatalf("%s: error %q want %q (%s)", label, got, wantErr, r.text())
	}
	active, err := f.st.ActiveLeasesByWorkID(context.Background(), workID)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(active) != 0 {
		f.t.Fatalf("%s: denial must grant no lease, active=%v", label, active)
	}
	wk, err := f.st.GetWork(context.Background(), workID)
	if err != nil {
		f.t.Fatal(err)
	}
	if wk.State != workgraph.StateQueued {
		f.t.Fatalf("%s: denial must not move work state: %s", label, wk.State)
	}
}

// ---------------------------------------------------------------------------
// Seam 1: gate ordering at grantLease (k-060 -> k-058/k-062 -> store).
// ---------------------------------------------------------------------------

// TestAdversary34_ClaimGateOrderOwnerFirst pins the observed-clean ordering:
// malformed body (400) is answered before authz; the k-060 owner gate is
// answered before the k-058/k-062 claim gate even when the victim's RAB would
// also deny; and the same worker claiming as ITSELF with a control RAB gets
// the claim-gate reason. A reordering that lets a mismatched claim reach the
// RAB gate or the store fails this test.
func TestAdversary34_ClaimGateOrderOwnerFirst(t *testing.T) {
	f := a34New(t, true, false) // auth ON, key OFF
	tokA := f.mint("wrkr_a")
	f.mustAdvertiseControl("wrkr_b") // victim: control RAB on file, no token presented
	w := f.work()

	// Owner gate wins: identity problem, not capability problem.
	f.mustAssertDenial("A-as-B", f.claim("wrkr_b", w, tokA, "", false),
		http.StatusForbidden, ReasonWorkerIDMismatch, w)

	// Malformed precedes authz: empty worker_id is 400, not 403.
	if r := f.post("/v1/leases/grant", `{"work_id":"`+w+`","node_id":"a"}`, tokA); r.code != http.StatusBadRequest || r.errCode() != "missing_field" {
		t.Fatalf("missing_field must precede the owner gate: %d %s", r.code, r.text())
	}

	// Same caller, own identity: now the RAB gate answers, proving both
	// gates are live and the order is deterministic.
	f.mustAdvertiseControl("wrkr_a")
	w2 := f.work()
	f.mustAssertDenial("A-as-A-no-token", f.claim("wrkr_a", w2, tokA, "", false),
		http.StatusForbidden, ReasonControlTokenRequired, w2)

	// Weird inputs cannot reach the store when auth is on: the owner gate is
	// exact equality against a verified identity, so every variant is 403
	// (no trim/lower asymmetry to exploit) — pinning the FULL set, not just
	// the clean mismatch case.
	// k-067 split the old law into two deterministic classes:
	// whitespace-padded variants TRIM to the canonical id and then behave
	// exactly like the canonical claim (here: own identity + control RAB +
	// no token => capability denial, not identity denial — the spoof is
	// dead because padded and canonical are ONE identity); every other
	// variant still mismatches the verified identity exactly. Pinning both
	// classes, no silent accepts.
	for _, padded := range []string{" wrkr_a", "wrkr_a ", "wrkr_a\n"} {
		f.mustAssertDenial("padded:"+padded, f.claim(padded, f.work(), tokA, "", false),
			http.StatusForbidden, ReasonControlTokenRequired, w)
	}
	for _, weird := range []string{"WRKR_A", "Wrkr_a", "a/../x", "wrkr_b//", "wrkr_a%20"} {
		f.mustAssertDenial("weird:"+weird, f.claim(weird, f.work(), tokA, "", false),
			http.StatusForbidden, ReasonWorkerIDMismatch, w)
	}
}

// TestAdversary34_ClaimGateCombinedMatrix is the cross-configuration table
// over (AuthEnabled, WORKS_RAB_CONTROL_TOKEN) x (claim presentation). The
// expectations are the hand-written oracle in a34WantClaim, derived from the
// four laws — the fixture never recomputes them. Two composition facts are
// what the table is really about:
//
//	(1) control-token STRICTNESS depends on the key alone, and key-on is
//	    never weaker than the v0.3.3 presence-only law: garbage and
//	    cross-runner tokens flip 201 -> 403 in BOTH auth modes. Dev-mode +
//	    key-on is therefore a strict tightening, not a hole.
//	(2) k-067 closed the padded-claim asymmetry: whitespace-padded ids
//	    trim to canonical BEFORE the owner gate, so the padded row answers
//	    like the canonical row (201 with a valid control token) in BOTH
//	    auth modes. Finding C (dev-mode lookalike identity forgery) is
//	    pinned as a regression in
//	    TestAdversary34_DevModeLookalikeWorkerIDEscapesGate.
func TestAdversary34_ClaimGateCombinedMatrix(t *testing.T) {
	const (
		pAbsent = iota
		pEmptyHdr
		pGarbage
		pTokA
		pTokB
		pPaddedTokA
	)
	names := map[int]string{
		pAbsent: "absent-header", pEmptyHdr: "empty-header", pGarbage: "garbage",
		pTokA: "token-bound-to-claimer", pTokB: "token-bound-to-other-runner",
		pPaddedTokA: "padded-lookalike-claim",
	}

	// a34WantClaim is the oracle table: (keyOn, presentation) -> answer,
	// with the single auth-dependent exception marked below.
	want := func(authOn, keyOn bool, pres int) (int, string) {
		switch pres {
		case pAbsent, pEmptyHdr:
			return http.StatusForbidden, ReasonControlTokenRequired
		case pGarbage:
			if keyOn {
				return http.StatusForbidden, ReasonControlTokenInvalid
			}
			return http.StatusCreated, ""
		case pTokA:
			return http.StatusCreated, ""
		case pTokB:
			if keyOn {
				return http.StatusForbidden, ReasonControlTokenInvalid
			}
			return http.StatusCreated, ""
		case pPaddedTokA:
			// k-067: the padded body trims to wrkr_a — the canonical
			// claimer — and carries wrkr_a's control token, so the grant is
			// legal in EVERY posture. The old authOn row (403
			// worker_id_mismatch) was the asymmetry finding C exploited;
			// one identity now answers identically in both modes.
			return http.StatusCreated, ""
		}
		t.Fatalf("unknown presentation %d", pres)
		return 0, ""
	}

	for _, authOn := range []bool{true, false} {
		for _, keyOn := range []bool{true, false} {
			for _, pres := range []int{pAbsent, pEmptyHdr, pGarbage, pTokA, pTokB, pPaddedTokA} {
				label := "auth=" + a34BoolStr(authOn) + ",key=" + a34BoolStr(keyOn) + "," + names[pres]
				f := a34New(t, authOn, keyOn)
				f.mustAdvertiseControl("wrkr_a")
				f.mustAdvertiseControl("wrkr_b")
				bearer := ""
				if authOn {
					bearer = f.mint("wrkr_a")
				}
				claimAs, send, hdr := "wrkr_a", true, ""
				switch pres {
				case pAbsent:
					send = false
				case pEmptyHdr:
					hdr = ""
				case pGarbage:
					hdr = "not-even-close"
				case pTokA:
					hdr = f.controlOrPresence(keyOn, "wrkr_a")
				case pTokB:
					hdr = f.controlOrPresence(keyOn, "wrkr_b")
				case pPaddedTokA:
					claimAs = "wrkr_a "
					hdr = f.controlOrPresence(keyOn, "wrkr_a")
				}
				code, reason := want(authOn, keyOn, pres)
				w := f.work()
				r := f.claim(claimAs, w, bearer, hdr, send)
				if code == http.StatusCreated {
					if r.code != http.StatusCreated {
						t.Fatalf("%s: expected grant, got %d %s", label, r.code, r.text())
					}
					continue
				}
				f.mustAssertDenial(label, r, code, reason, w)
			}
		}
	}
}

// controlOrPresence returns a credential that is VALID for runnerID when the
// k-062 key is configured, and any non-empty value when it is not (the k-058
// law accepts any value, so the matrix must not depend on the key there).
func (f *a34) controlOrPresence(keyOn bool, runnerID string) string {
	f.t.Helper()
	if !keyOn {
		return "presence-only-value"
	}
	return f.ctlToken(runnerID)
}

func a34BoolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// ---------------------------------------------------------------------------
// Seam 2: identity normalization vs the control-token gate (findings B, C).
// ---------------------------------------------------------------------------

// TestAdversary34_DevModeLookalikeWorkerIDEscapesGate reproduces finding C:
// on a dev-mode server (AuthEnabled=false — the posture of every e2e run)
// with WORKS_RAB_CONTROL_TOKEN SET, a claim that appends a space to a
// registered control runner's id bypasses the k-058/k-062 gate entirely (the
// registry lookup is exact-match, so no RAB is found => legacy pass), and the
// store durably records the lookalike worker_id on the attempt and the lease
// next to the real one. One space is the difference between 403 and 201.
//
// PIN: if a canonicalization law is added (trim/reject non-canonical
// worker_id at grant, or resolve the RAB by a normalized id), the padded
// claim stops being 201 and this test fails by design — flip it into a
// regression assertion that requires the denial.
func TestAdversary34_DevModeLookalikeWorkerIDEscapesGate(t *testing.T) {
	f := a34New(t, false, true) // dev mode + k-062 verification ON
	f.mustAdvertiseControl("wrkr_a")

	exact := f.work()
	f.mustAssertDenial("exact-id-no-token", f.claim("wrkr_a", exact, "", "", false),
		http.StatusForbidden, ReasonControlTokenRequired, exact)

	lookalike := f.work()
	// k-067 closed: the padded id is trimmed (k-060/k-067 canonicalization in
	// grantLease) before the gate, so "wrkr_a " resolves to the registered
	// control runner wrkr_a and hits the k-062 control-token-required gate
	// instead of sailing through as a second identity.
	r := f.claim("wrkr_a ", lookalike, "", "", false)
	f.mustAssertDenial("padded-id-normalized", r,
		http.StatusForbidden, ReasonControlTokenRequired, lookalike)
	// No lease (and no attempt) is recorded under the spoofed padded form —
	// the denial short-circuits before any store write (k-060/k-067: the
	// trim happens prior to the owner check, and a denied claim touches no
	// state). The real runner wrkr_a is unaffected.
}

func mustLeaseID(t *testing.T, r a34res) string {
	t.Helper()
	var out struct {
		Lease workgraph.Lease `json:"lease"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Lease.ID
}

// TestAdversary34_EnrollmentCharsetLegacyPass reproduces finding B in the
// PRODUCTION posture (auth ON + key ON): the enrollment charset is a strict
// superset of the runner-id charset, so a worker can hold a verified identity
// that the registry can never hold. Such a token passes k-060 (claims ==
// body), can never be RAB-advertised (400), and therefore sits permanently in
// k-058's legacy-pass class: its claims are granted with no control token at
// all.
//
// Premises asserted from the code, not from reading: validWorkerID accepts the
// id, runner.BuildIdentity (the registry pattern) rejects it.
//
// PIN: flips when enrollment and the runner pattern are aligned (or when the
// claim gate stops treating "no RAB on file" as a pass for an authenticated
// identity). Then this test must be rewritten to require the denial.
func TestAdversary34_EnrollmentCharsetLegacyPass(t *testing.T) {
	// k-066 closed: enrollment now enforces the SAME charset as the registry
	// (runner.RunnerIDPattern). An id the registry rejects (WRKR_A, uppercase)
	// can no longer be enrolled, so the token that k-061's equality gate would
	// bind is never minted. The k-058 legacy-pass path for unregistrable but
	// authenticated ids is therefore unreachable by construction.
	if validWorkerID("WRKR_A") {
		t.Fatal("PIN INVALID: enrollment still accepts WRKR_A that the registry rejects — charset not aligned")
	}

	f := a34New(t, true, true) // auth ON, k-062 key ON
	// The law bites at the MINT entry point: the real enrollment endpoint
	// refuses the unregistrable id, so no bearer token for it can exist.
	// (f.mint bypasses enrollment on purpose — every other test uses it for
	// clean identity fixtures; here we must not.)
	ereq, _ := json.Marshal(map[string]any{"worker_id": "WRKR_A", "challenge": a34EnrollSecret})
	eresp := f.do(http.MethodPost, "/v1/workers/enroll", string(ereq), "", "", false)
	if eresp.code != http.StatusBadRequest || eresp.errCode() != "invalid_worker_id" {
		t.Fatalf("enrollment of WRKR_A must be refused 400 invalid_worker_id, got %d %s",
			eresp.code, eresp.text())
	}
	// Defense in depth: even a hand-minted token (issuer secret, out-of-band)
	// cannot register the id — the registry's own Validate refuses it.
	tok := f.mint("WRKR_A")
	r := f.registerAs("WRKR_A", tok)
	if r.code != http.StatusBadRequest || r.errCode() != "validation_failed" {
		t.Fatalf("registry must still refuse an unregistrable id, got %d %s", r.code, r.text())
	}
	// The asymmetry survives: a properly registered control runner is still gated.
	f.mustAdvertiseControl("wrkr_a")
	w2 := f.work()
	f.mustAssertDenial("registered-runner-still-gated", f.claim("wrkr_a", w2, f.mint("wrkr_a"), "", false),
		http.StatusForbidden, ReasonControlTokenRequired, w2)
}

// ---------------------------------------------------------------------------
// Seam 3: k-062 verification vs k-061 ownership (findings F, plus clean pins).
// ---------------------------------------------------------------------------

// TestAdversary34_NoControlTokenOracle pins the observed-clean answer to the
// oracle question: in k-062 mode the server does NOT distinguish a valid
// token bound to another runner from a malformed one — same status, same
// code, same message bytes, and no echo of the presented value. If a future
// change splits those reasons, this test fails and the new distinction must
// be assessed as a key-probing oracle.
func TestAdversary34_NoControlTokenOracle(t *testing.T) {
	f := a34New(t, true, true)
	f.mustAdvertiseControl("wrkr_a")
	f.mustAdvertiseControl("wrkr_b")
	tokA := f.mint("wrkr_a")

	foreign := f.ctlToken("wrkr_b") // valid, well-formed, bound to B
	cases := map[string]string{
		"bound-to-other-runner": foreign,
		"malformed-no-sep":      "aaaaaa",
		"bad-base64":            "!!!." + strings.Repeat("00", 32),
		"short-mac":             "d3Jrcl9h." + strings.Repeat("ab", 8),
		"corrupted-mac":         foreign[:len(foreign)-2] + "ff",
		"empty-segments":        "..",
		"scheme-prefixed":       a34AuthPrefix + foreign,
	}
	var wantBody string
	for name, presented := range cases {
		r := f.claim("wrkr_a", f.work(), tokA, presented, true)
		if r.code != http.StatusForbidden || r.errCode() != ReasonControlTokenInvalid {
			t.Fatalf("%s: expected uniform 403 %s, got %d %s", name, ReasonControlTokenInvalid, r.code, r.text())
		}
		if strings.Contains(r.text(), presented) {
			t.Fatalf("%s: the presented token value must never be echoed back: %s", name, r.text())
		}
		if wantBody == "" {
			wantBody = r.text()
			continue
		}
		if r.text() != wantBody {
			t.Fatalf("%s: denial body differs from the other failures — oracle introduced.\n got %s\nwant %s",
				name, r.text(), wantBody)
		}
	}
	if strings.Contains(f.log.String(), foreign) || strings.Contains(f.log.String(), a34CtlKey) {
		t.Fatalf("neither the token nor the key may be logged: %s", f.log.String())
	}
}

// TestAdversary34_ControlTokenIsUselessWithoutTheIdentity pins the k-061 x
// k-062 combination from the task: A cannot write B's RAB (403
// not_runner_owner, advertisement unchanged) and B's valid control token buys
// A nothing — the credential and the identity are both required.
func TestAdversary34_ControlTokenIsUselessWithoutTheIdentity(t *testing.T) {
	f := a34New(t, true, true)
	tokA, tokB := f.mint("wrkr_a"), f.mint("wrkr_b")
	f.mustAdvertiseControl("wrkr_a")
	// B exists but has advertised nothing yet — the exact pre-k-061 target an
	// attacker would aim at (a foreign runner_id with an established
	// identity).
	if r := f.registerAs("wrkr_b", tokB); r.code != http.StatusCreated {
		t.Fatalf("register wrkr_b: %d %s", r.code, r.text())
	}
	before := f.get("/v1/runners/wrkr_b/abi", tokB)
	if before.code != http.StatusNotFound {
		t.Fatalf("B should have no RAB yet: %d %s", before.code, before.text())
	}

	// A cannot write B's RAB.
	r := f.advertise("wrkr_b", a34rabCtl, tokA)
	if r.code != http.StatusForbidden || r.errCode() != "not_runner_owner" {
		t.Fatalf("token A writing runner B must be 403 not_runner_owner, got %d %s", r.code, r.text())
	}
	after := f.get("/v1/runners/wrkr_b/abi", tokB)
	if after.code != http.StatusNotFound {
		t.Fatalf("denied write must leave B without any advertisement: %d %s", after.code, after.text())
	}

	// B's RAB, posted by B, is now under control law — and B's control token
	// is useless to A even though A can read it off the wire in a test.
	f.mustAdvertiseControl("wrkr_b")
	tokForB := f.ctlToken("wrkr_b")
	wa := f.work()
	f.mustAssertDenial("A-presenting-Bs-control-token", f.claim("wrkr_a", wa, tokA, tokForB, true),
		http.StatusForbidden, ReasonControlTokenInvalid, wa)

	// And A can never claim AS B at all (k-060), so B's identity plus B's
	// credential is unreachable for A by either half.
	wb := f.work()
	f.mustAssertDenial("A-claiming-as-B", f.claim("wrkr_b", wb, tokA, tokForB, true),
		http.StatusForbidden, ReasonWorkerIDMismatch, wb)
}

// TestAdversary34_ABISurfaceDenialOrder pins the k-053 order law against the
// k-061 bearer/ownership gates: 401 always precedes 404/403 (so an
// unauthenticated caller learns nothing), and for an authenticated caller the
// 404 (order law) precedes the 403 (ownership). That ordering is an
// existence oracle — pinned as harmless here because the identity reads are
// public on the same server.
func TestAdversary34_ABISurfaceDenialOrder(t *testing.T) {
	f := a34New(t, true, false)
	unknown := "wrkr_never_registered"
	anonTok := f.mint(unknown) // a valid token for an id with no runner record

	// Anonymous: middleware first on all three abi routes (POST, GET,
	// negotiate) — never 404, so no unauthenticated enumeration.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/runners/" + unknown + "/abi", a34rabPlain},
		{http.MethodGet, "/v1/runners/" + unknown + "/abi", ""},
		{http.MethodPost, "/v1/runners/" + unknown + "/abi/negotiate", `{"caps":["observe"]}`},
	} {
		r := f.do(tc.method, tc.path, tc.body, "", "", false)
		if r.code != http.StatusUnauthorized {
			t.Fatalf("%s %s: unauthenticated caller must hit auth first, got %d %s",
				tc.method, tc.path, r.code, r.text())
		}
	}

	// Authenticated, unknown runner: order law (404) fires BEFORE ownership.
	// The same request for a KNOWN foreign runner answers 403 — the pair is
	// the oracle, and it is only available to holders of a valid token.
	if r := f.advertise(unknown, a34rabPlain, anonTok); r.code != http.StatusNotFound || r.errCode() != "runner_not_found" {
		t.Fatalf("expected 404 runner_not_found before the owner gate, got %d %s", r.code, r.text())
	}
	if r := f.get("/v1/runners/"+unknown+"/abi", anonTok); r.code != http.StatusNotFound {
		t.Fatalf("GET abi unknown with a valid token: %d %s", r.code, r.text())
	}
	f.mustAdvertiseControl("wrkr_a")
	foreign := f.mint("wrkr_other")
	if r := f.advertise("wrkr_a", a34rabPlain, foreign); r.code != http.StatusForbidden || r.errCode() != "not_runner_owner" {
		t.Fatalf("expected 403 not_runner_owner for a known foreign runner, got %d %s", r.code, r.text())
	}

	// Mitigation, pinned: runner existence needs no token at all, so the
	// 404-vs-403 pair discloses nothing that is not already public.
	if r := f.get("/v1/runners/wrkr_a", ""); r.code != http.StatusOK {
		t.Fatalf("GET /v1/runners/{id} must stay public (AUTH.md row): %d %s", r.code, r.text())
	}
	if r := f.get("/v1/runners", ""); r.code != http.StatusOK || !strings.Contains(r.text(), "wrkr_a") {
		t.Fatalf("GET /v1/runners must list publicly: %d %s", r.code, r.text())
	}
	if r := f.get("/v1/runners/wrkr_never_registered", ""); r.code != http.StatusNotFound {
		t.Fatalf("unknown runner must be 404 publicly: %d %s", r.code, r.text())
	}
}

// TestAdversary34_RegisterGatePrecedesShape pins that k-061's gate runs
// BEFORE mint/parse/validate, and that passing the gate is not enough to
// write: an identity the caller owns but that the registry cannot hold is
// refused by Validate with zero registry effect. Also pins the reflected-id
// shape of the denial (the caller-supplied id is echoed, JSON-escaped, and
// nothing else).
func TestAdversary34_RegisterGatePrecedesShape(t *testing.T) {
	f := a34New(t, true, false)
	own := "WRKR_UPPER"
	tokOwn := f.mint(own)
	r := f.registerAs(own, tokOwn)
	if r.code != http.StatusBadRequest || r.errCode() != "validation_failed" {
		t.Fatalf("own-token gate pass must still be refused by shape validation: %d %s", r.code, r.text())
	}
	if g := f.get("/v1/runners/"+own, ""); g.code != http.StatusNotFound {
		t.Fatalf("refused registration must leave the registry unchanged: %d %s", g.code, g.text())
	}

	// Foreign id: the gate answers 403 before the shape check, and the body
	// echoes the id back as a JSON string (no raw injection into the body).
	other := f.mint("wrkr_a")
	fr := f.registerAs("../escape", other)
	if fr.code != http.StatusForbidden || fr.errCode() != "not_runner_owner" {
		t.Fatalf("expected 403 not_runner_owner before validation, got %d %s", fr.code, fr.text())
	}
	var eb struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(fr.body, &eb); err != nil {
		t.Fatalf("denial body must be one JSON object: %v (%s)", err, fr.text())
	}
	if !strings.Contains(eb.Message, "../escape") {
		t.Fatalf("denial message is expected to name the runner: %s", eb.Message)
	}

	// Legacy mint mode: omitting runner_id is allowed and the minted id is
	// NOT bound to the minting token, so the caller cannot then advertise
	// for it. Documented in AUTH.md; pinned because it is the one place the
	// ownership law deliberately lets an identity float free.
	minted := f.registerAs("", f.mint("wrkr_free"))
	if minted.code != http.StatusCreated {
		t.Fatalf("legacy registration (no runner_id) must be accepted: %d %s", minted.code, minted.text())
	}
	newID := minted.field("runner_id")
	if newID == "" {
		t.Fatalf("no minted runner_id in %s", minted.text())
	}
	if adv := f.advertise(newID, a34rabPlain, f.mint("wrkr_free")); adv.code != http.StatusForbidden {
		t.Fatalf("minted id must not be auto-owned by the minting token, got %d %s", adv.code, adv.text())
	}
}

// TestAdversary34_StableReasonCodesOnTheWire pins the four stable authz
// reason codes as observed on the wire (finding F: k-061's is an inline
// literal with no exported constant, so only this test protects it) and pins
// that the k-060 denial names both worker ids — deliberate surface, per
// claim_owner_authz.go's header.
func TestAdversary34_StableReasonCodesOnTheWire(t *testing.T) {
	f := a34New(t, true, true)
	f.mustAdvertiseControl("wrkr_a")
	tokA := f.mint("wrkr_a")
	// B must exist for the ownership gate to be the one that answers (the
	// k-053 order law 404s an unknown runner first; that order is pinned in
	// TestAdversary34_ABISurfaceDenialOrder).
	if r := f.registerAs("wrkr_b", f.mint("wrkr_b")); r.code != http.StatusCreated {
		t.Fatalf("register wrkr_b: %d %s", r.code, r.text())
	}

	owner := f.claim("wrkr_b", f.work(), tokA, "", false)
	if owner.errCode() != "worker_id_mismatch" {
		t.Fatalf("k-060 wire code changed: %s", owner.text())
	}
	if !strings.Contains(owner.text(), "wrkr_a") || !strings.Contains(owner.text(), "wrkr_b") {
		t.Fatalf("k-060 denial must name both ids (documented): %s", owner.text())
	}
	if c := f.advertise("wrkr_b", a34rabPlain, tokA); c.code != http.StatusForbidden || c.errCode() != "not_runner_owner" {
		t.Fatalf("k-061 wire code changed: %d %s", c.code, c.text())
	}
	if c := f.claim("wrkr_a", f.work(), tokA, "", false); c.errCode() != "control_token_required" {
		t.Fatalf("k-058 wire code changed: %s", c.text())
	}
	if c := f.claim("wrkr_a", f.work(), tokA, "junk", true); c.errCode() != "control_token_invalid" {
		t.Fatalf("k-062 wire code changed: %s", c.text())
	}
	// The exported constants must equal the pinned literals (one source of
	// truth for the codes that do have constants).
	if ReasonWorkerIDMismatch != "worker_id_mismatch" || ReasonControlTokenRequired != "control_token_required" ||
		ReasonControlTokenInvalid != "control_token_invalid" {
		t.Fatal("an exported reason constant drifted from its wire value")
	}
}

// TestAdversary34_SingleErrorWritePerDenial pins the seam's error hygiene:
// each denial path emits exactly one JSON error object (a double writeError
// would produce either two concatenated objects or a "superfluous
// WriteHeader" trace in the body), and the header count is one.
func TestAdversary34_SingleErrorWritePerDenial(t *testing.T) {
	f := a34New(t, true, true)
	f.mustAdvertiseControl("wrkr_a")
	tokA := f.mint("wrkr_a")

	cases := []a34res{
		f.claim("wrkr_b", f.work(), tokA, "", false),                    // k-060
		f.claim("wrkr_a", f.work(), tokA, "", false),                    // k-058
		f.claim("wrkr_a", f.work(), tokA, "junk", true),                 // k-062
		f.claim("wrkr_a", "w_missing", tokA, "", false),                 // store: work_not_found
		f.do(http.MethodPost, "/v1/leases/grant", "{", tokA, "", false), // 400 invalid_json
	}
	for i, r := range cases {
		txt := strings.TrimSpace(r.text())
		if !strings.HasPrefix(txt, "{") || !strings.HasSuffix(txt, "}") {
			t.Fatalf("case %d: expected exactly one JSON object, got %q", i, txt)
		}
		if n := strings.Count(txt, `"error":`); n != 1 {
			t.Fatalf("case %d: %d error fields (double writeError?) in %q", i, n, txt)
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(txt), &probe); err != nil {
			t.Fatalf("case %d: body is not a single JSON document: %v (%q)", i, err, txt)
		}
		if _, ok := probe["error"]; !ok {
			t.Fatalf("case %d: denial without an error code: %q", i, txt)
		}
	}
	// The owner-gate denial path logs (leases.go:110) and writes; prove the
	// log dereference cannot panic: the gate only denies when claims exist,
	// so a nil-claims (dev) server never reaches that line at all.
	dev := a34New(t, false, true)
	dev.mustAdvertiseControl("wrkr_a")
	if r := dev.claim("wrkr_a", dev.work(), "", "", false); r.code != http.StatusForbidden {
		t.Fatalf("dev-mode claim gate denial expected, got %d %s", r.code, r.text())
	}
}

// ---------------------------------------------------------------------------
// Seam 4: k-060's scope vs the rest of the lease verbs (finding D).
// ---------------------------------------------------------------------------

// TestAdversary34_NonGrantLeaseVerbsUnbound reproduces finding D: k-060
// authorizes the claim verb only. With auth ON, a token for wrkr_x revokes
// the ACTIVE lease of wrkr_victim and the store confirms the transition —
// cross-identity state mutation on the same path prefix the k-060 header
// describes as safe because those verbs "bind to a lease created under the
// verified identity". They bind to an ID, not to an identity.
//
// The mitigation is pinned too, because it is what keeps this MEDIUM rather
// than HIGH: lease ids are crypto/rand 128-bit and appear on no read surface
// (public work read, audit stream read with an unrelated token, work event
// stream), so the attack needs an out-of-band id leak.
//
// PIN: when heartbeat/complete/release/revoke grow the same ClaimsFrom check,
// the revoke below turns 403 and this test fails by design — rewrite it as
// the regression pin.
func TestAdversary34_NonGrantLeaseVerbsUnbound(t *testing.T) {
	f := a34New(t, true, false)
	victim := f.mint("wrkr_victim")
	f.mustAdvertiseControl("wrkr_victim")
	// Give the victim a clean pass: key off in this server, so its claim
	// needs only presence — keep the finding orthogonal to k-062.
	vw := f.work()
	leaseID := f.mustGrant("wrkr_victim", vw, victim, "presence", true)

	attacker := f.mint("wrkr_attacker")
	r := f.post("/v1/leases/"+leaseID+"/revoke", `{"reason":"adversary"}`, attacker)
	// k-065 closed: revoke (and all non-grant lease verbs) is now owner-bound
	// by k-059/065 gateLeaseOwner; a foreign token is denied 403
	// lease_not_owner, NOT 200. The pin above asserted the OLD permissive
	// behaviour and flipped to this regression check when the gate landed.
	if r.code != http.StatusForbidden || r.errCode() != ReasonLeaseNotOwner {
		t.Fatalf("PIN INVALID: foreign revoke was allowed (%d %s) — finding D is NOT closed", r.code, r.text())
	}
	// The victim's lease is unchanged by the denial.
	l2, err := f.st.GetLease(context.Background(), leaseID)
	if err != nil {
		t.Fatal(err)
	}
	if l2.Status != workgraph.LeaseActive || l2.WorkerID != "wrkr_victim" {
		t.Fatalf("lease mutated by the denial: status=%s worker=%s", l2.Status, l2.WorkerID)
	}

	// Mitigation sweep: the id must be unobtainable on every read surface.
	for _, probe := range []struct{ path, bearer string }{
		{"/v1/works/" + vw, ""},
		{"/v1/works/" + vw + "/evidence", ""},
		{"/v1/works/" + vw + "/nodes/a/logs", ""},
		{"/v1/audit-events?work_id=" + vw, attacker},
		{"/v1/audit-events", victim},
		{"/v1/runners/wrkr_victim", ""},
		{"/v1/runners", ""},
	} {
		got := f.get(probe.path, probe.bearer)
		if got.code == http.StatusOK && strings.Contains(got.text(), leaseID) {
			t.Fatalf("lease id leaked on %s — finding D escalates to HIGH", probe.path)
		}
	}

	// k-060's own verb is still bound (the asymmetry is the point).
	gw := f.work()
	f.mustAssertDenial("grant-is-bound", f.claim("wrkr_victim", gw, attacker, "", false),
		http.StatusForbidden, ReasonWorkerIDMismatch, gw)
}

// ---------------------------------------------------------------------------
// Seam 5: production wiring (finding A).
// ---------------------------------------------------------------------------

// TestAdversary34_RunnerIDCLINoBearerBreaksInProd reproduces finding A at the
// wire: cmd/works-runner-id -register sends exactly this request — the
// schema-shaped identity, Content-Type only, no Authorization (main.go:82-95
// never sets the header; the tool has no token flag and no enroll call) —
// against a server in the production posture (cmd/works-api/main.go:75
// hardcodes AuthEnabled: true). Post-k-061 the answer is 401 and the tool
// log.Fatalf()s. On a dev-mode server it is 201, which is why every CI gate
// is green.
//
// PIN: this test must be inverted when the CLI grows a token path (enroll +
// Authorization) or when -register is removed from the tool and its docs.
func TestAdversary34_RunnerIDCLINoBearerBreaksInProd(t *testing.T) {
	// The tool always supplies runner_id (minted, or -id), so k-061's legacy
	// mint mode is not an escape hatch for it.
	runnerID := runner.MintRunnerID()
	body := a34IdentBody(runnerID)

	prod := a34New(t, true, false)
	req, err := http.NewRequest(http.MethodPost, prod.ts.URL+"/v1/runners/register", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	// Identical to the CLI's request construction: Content-Type, nothing else.
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PIN INVALID: the works-runner-id request no longer 401s in prod posture (%d %s) — "+
			"the CLI gained auth; update this test to require success", resp.StatusCode, raw)
	}
	var eb struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &eb); err != nil || eb.Error != "missing_authorization" {
		t.Fatalf("expected missing_authorization, got %s", raw)
	}

	dev := a34New(t, false, false)
	if r := dev.do(http.MethodPost, "/v1/runners/register", body, "", "", false); r.code != http.StatusCreated {
		t.Fatalf("the same request must still succeed on a dev server (that is why CI is green): %d %s",
			r.code, r.text())
	}

	// Even WITH a token the tool cannot work unless it enrolls under the
	// exact runner id it registers — the documented k-061 rule, shown here so
	// the fix scope is unambiguous: a foreign token is 403, and the tool has
	// no way to know its runner_id in advance to enroll as it.
	withTok := prod.registerAs(runnerID, prod.mint("wrkr_someone_else"))
	if withTok.code != http.StatusForbidden || withTok.errCode() != "not_runner_owner" {
		t.Fatalf("a token that does not own the supplied runner_id must be refused: %d %s",
			withTok.code, withTok.text())
	}
	// The tool's only working shape today: enroll with worker_id == the
	// runner_id it will register, and attach the bearer it never asks for.
	own := prod.registerAs(runnerID, prod.mint(runnerID))
	if own.code != http.StatusCreated {
		t.Fatalf("the owning-token path must work (that is the fix the CLI is missing): %d %s",
			own.code, own.text())
	}
}

// ---------------------------------------------------------------------------
// Seam 6: docs/AUTH.md vs the actual Routes() table (finding E).
// ---------------------------------------------------------------------------

// a34AuthMD loads docs/AUTH.md from the package directory.
func a34AuthMD(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "AUTH.md"))
	if err != nil {
		t.Fatalf("cannot read docs/AUTH.md: %v", err)
	}
	return strings.Split(string(b), "\n")
}

func a34FindLine(lines []string, needle string) (int, string) {
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i + 1, l
		}
	}
	return 0, ""
}

// TestAdversary34_AuthMDWorksSubtreeRowIsFalse reproduces finding E1:
// docs/AUTH.md:8 lists `/v1/works/*` under "Endpoints requiring Bearer auth",
// but Routes() mounts the "/v1/works/" prefix with NO requireBearer (api.go:177
// — whose own comment says these routes "remain unauthenticated (operator
// surface)"). On an auth-ON server, an anonymous caller can READ any work and
// CANCEL any work. The doc row is the lie; the code is the (documented in
// code, not in docs) intent.
//
// PIN: fails when either the row is corrected (moved to the public table, or
// narrowed to the routes that ARE bearer-gated) or the subtree is actually
// wrapped — both correct fixes, both require this test to be rewritten as the
// matching regression assertion.
func TestAdversary34_AuthMDWorksSubtreeRowIsFalse(t *testing.T) {
	lines := a34AuthMD(t)
	no, row := a34FindLine(lines, "| `/v1/works/*` |")
	if no == 0 {
		t.Fatal("PIN INVALID: AUTH.md no longer has a `/v1/works/*` row — check whether the drift was " +
			"fixed (then assert the corrected posture here) or just renamed (then fix this needle)")
	}
	t.Logf("pinned doc row docs/AUTH.md:%d: %s", no, row)
	bearerHdr, _ := a34FindLine(lines, "## Endpoints requiring Bearer auth")
	publicHdr, _ := a34FindLine(lines, "## Public endpoints")
	if !(bearerHdr < no) || (publicHdr != 0 && no > publicHdr) {
		t.Fatalf("PIN INVALID: the `/v1/works/*` row moved out of the bearer table (line %d between %d and %d) — "+
			"the doc is fixed; rewrite this test to assert the corrected posture", no, bearerHdr, publicHdr)
	}

	f := a34New(t, true, false) // production posture: bearer enforced
	w := f.work()
	if r := f.get("/v1/works/"+w, ""); r.code != http.StatusOK {
		t.Fatalf("PIN INVALID: anonymous work read is no longer served (%d %s) — subtree is gated now",
			r.code, r.text())
	}
	cancel := f.post("/v1/works/"+w+"/cancel", `{}`, "")
	if cancel.code != http.StatusOK || cancel.field("state") != string(workgraph.StateCancelled) {
		t.Fatalf("unexpected cancel answer (%d %s) — re-derive this finding", cancel.code, cancel.text())
	}
	// And the exact-match parent IS gated, which is what makes the doc row
	// read as a general statement about the subtree.
	if r := f.get("/v1/works", ""); r.code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/works must be bearer-gated (AUTH.md row 7): %d %s", r.code, r.text())
	}
}

// TestAdversary34_AuthMDOmitsControlLawAndBearerRows reproduces finding E2:
// AUTH.md's two tables are not the Routes() table. It asserts the live
// posture for routes the doc never lists, and it asserts the doc's own
// omissions, so a doc fix flips it deliberately.
//
// PIN: add the missing rows (control-token law + WORKS_RAB_CONTROL_TOKEN,
// /v1/cache/, /v1/works/{id}/events, /resume, /suspend, /handoff,
// /v1/brain/) and delete the bogus `/readyz` mention, and the doc-side
// assertions below start failing — rewrite them as positive assertions.
func TestAdversary34_AuthMDOmitsControlLawAndBearerRows(t *testing.T) {
	doc := strings.Join(a34AuthMD(t), "\n")

	if strings.Contains(doc, "X-RAB-Control-Token") || strings.Contains(doc, "WORKS_RAB_CONTROL_TOKEN") {
		t.Fatal("PIN INVALID: AUTH.md now documents the control-token law — rewrite these assertions to " +
			"require the law's rows to match the code")
	}
	if strings.Contains(doc, "worker_id_mismatch") {
		t.Fatal("PIN INVALID: AUTH.md now documents k-060's per-action authz code; assert the row instead")
	}
	for _, undocumented := range []string{"/v1/cache", "/v1/brain", "/suspend", "/handoff", "/events"} {
		if strings.Contains(doc, undocumented) {
			t.Fatalf("PIN INVALID: AUTH.md now lists %s — drop it from the omission pin and assert the row",
				undocumented)
		}
	}
	readyNo, readyRow := a34FindLine(strings.Split(doc, "\n"), "/readyz")
	if readyNo == 0 {
		t.Fatal("PIN INVALID: the bogus /readyz row is gone; remaining assertions need re-derivation")
	}
	if !strings.Contains(readyRow, "Public") && !strings.Contains(readyRow, "Liveness") {
		t.Fatalf("unexpected /readyz row: %s", readyRow)
	}

	// Live posture of the routes the doc omits: all bearer-gated on a prod
	// server, while `/readyz` — which the doc calls public — does not exist.
	f := a34New(t, true, false)
	w := f.work()
	for _, path := range []string{
		"/v1/cache/anykey",
		"/v1/works/" + w + "/events",
		"/v1/works/" + w + "/handoff",
	} {
		if r := f.get(path, ""); r.code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401 (bearer-gated, undocumented in AUTH.md), got %d %s",
				path, r.code, r.text())
		}
	}
	// negotiate is bearer-gated on its own verb; a GET on the POST-only route
	// 404s in the mux before the middleware, which discloses nothing.
	if r := f.post("/v1/runners/wrkr_a/abi/negotiate", `{"caps":["observe"]}`, ""); r.code != http.StatusUnauthorized {
		t.Fatalf("negotiate: expected 401, got %d %s", r.code, r.text())
	}
	if r := f.get("/readyz", ""); r.code == http.StatusOK {
		t.Fatalf("/readyz is documented as public and now answers 200 — re-derive this finding")
	}
	// The k-061 ownership code IS documented (must stay — it is the one row
	// k-061 got right).
	if !strings.Contains(doc, "not_runner_owner") {
		t.Fatal("AUTH.md no longer documents not_runner_owner — k-061's own doc row was lost")
	}
}
