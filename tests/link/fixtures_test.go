package linkconformance

// Fixture-level law: positive documents built from the real link.WireRequest
// structs (every POST/GET envelope the docs show) must validate against
// link.wire/1.0, and the frozen schemas must REJECT the adversarial shapes:
// endpoint off-enum, method PUT, auth api_token, sas_code lowercase/short,
// state 'DELETED', scopes containing T9_god. Where the frozen schema is
// TOOTHLESS (it cannot express the invariant), the law is asserted at Go
// level instead and the gap is pinned so a future schema amendment cannot
// silently regress it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/link"
)

const validHash = "a3f5b7c9d1e2f30415263748596a7b8c9d0e1f2031425364758697a8b9c0d1e2" // 64 lowercase hex

// ---- link.wire/1.0 positives (serialized from real link.WireRequest structs) ----

func TestWirePositivesFromRealStructs(t *testing.T) {
	wire := compile(t, "link.wire")
	endpoints := []string{link.EndpointPair, link.EndpointMounts, link.EndpointMissions, link.EndpointCommands, link.EndpointRevoke}
	for _, ep := range endpoints {
		for _, m := range []string{"POST", "GET"} {
			q := link.WireRequest{Endpoint: ep, Method: m, Auth: "mTLS+device_token"}
			mustPass(t, wire, "wire:positive:"+m+":"+ep, marshalThroughJSON(t, q))
		}
	}
	// The documented full envelope: scope + idempotency_key + payload_hash.
	full := link.WireRequest{
		Endpoint: link.EndpointMounts, Method: "POST", Auth: "mTLS+device_token",
		Scope: link.ScopeT2Action, IdempotencyKey: "k036-fixture-1", PayloadHash: validHash,
	}
	mustPass(t, wire, "wire:positive:full-envelope", marshalThroughJSON(t, full))
	// A mounts/missions envelope embedding the struct with every frozen scope tier.
	for _, sc := range []string{link.ScopeT1Read, link.ScopeT2Action, link.ScopeT3Privileged} {
		q := link.WireRequest{Endpoint: link.EndpointPair, Method: "POST", Auth: "mTLS+device_token", Scope: sc}
		mustPass(t, wire, "wire:positive:scope:"+sc, marshalThroughJSON(t, q))
	}
	// MountRequest / RevokeRequest as serialized on the wire (envelope + law fields).
	mr := link.MountRequest{
		WireRequest:     link.WireRequest{Endpoint: link.EndpointMounts, Method: "POST", Auth: "mTLS+device_token", Scope: link.ScopeT1Read},
		DeviceID:        "dev_fx",
		WorkID:          "work:fx",
		PurposeBindings: []string{"work:fx"},
	}
	mustPass(t, wire, "wire:positive:mount-request-doc", marshalThroughJSON(t, mr))
	rr := link.RevokeRequest{
		WireRequest: link.WireRequest{Endpoint: link.EndpointRevoke, Method: "POST", Auth: "mTLS+device_token"},
		DeviceID:    "dev_fx",
	}
	mustPass(t, wire, "wire:positive:revoke-request-doc", marshalThroughJSON(t, rr))
}

// ---- link.wire/1.0 adversarial: the schema MUST reject ----

func TestWireAdversarialRejectedBySchema(t *testing.T) {
	wire := compile(t, "link.wire")
	bad := []struct {
		label string
		doc   map[string]any
	}{
		{"endpoint-not-in-enum:/link/v1/execute", fixture(t, `{"endpoint":"/link/v1/execute","method":"POST","auth":"mTLS+device_token"}`)},
		{"endpoint-not-in-enum:/link/v2/pair", fixture(t, `{"endpoint":"/link/v2/pair","method":"POST","auth":"mTLS+device_token"}`)},
		{"endpoint-not-in-enum:pulse-takeover", fixture(t, `{"endpoint":"/kernel/v1/control","method":"POST","auth":"mTLS+device_token"}`)},
		{"method-PUT", fixture(t, `{"endpoint":"/link/v1/mounts","method":"PUT","auth":"mTLS+device_token"}`)},
		{"method-DELETE", fixture(t, `{"endpoint":"/link/v1/revoke","method":"DELETE","auth":"mTLS+device_token"}`)},
		{"method-lowercase-post", fixture(t, `{"endpoint":"/link/v1/pair","method":"post","auth":"mTLS+device_token"}`)},
		{"auth-api_token", fixture(t, `{"endpoint":"/link/v1/mounts","method":"POST","auth":"api_token"}`)},
		{"auth-worker-JWT", fixture(t, `{"endpoint":"/link/v1/revoke","method":"POST","auth":"Bearer JWT"}`)},
		{"auth-wrong-const-case", fixture(t, `{"endpoint":"/link/v1/pair","method":"POST","auth":"mtls+device_token"}`)},
		{"auth-missing(required)", fixture(t, `{"endpoint":"/link/v1/pair","method":"POST"}`)},
		{"endpoint-missing(required)", fixture(t, `{"method":"POST","auth":"mTLS+device_token"}`)},
		{"method-missing(required)", fixture(t, `{"endpoint":"/link/v1/pair","auth":"mTLS+device_token"}`)},
		{"scope-T9_god", fixture(t, `{"endpoint":"/link/v1/mounts","method":"POST","auth":"mTLS+device_token","scope":"T9_god"}`)},
		{"scope-lowercase", fixture(t, `{"endpoint":"/link/v1/mounts","method":"POST","auth":"mTLS+device_token","scope":"t1_read"}`)},
		{"endpoint-wrong-type", fixture(t, `{"endpoint":1,"method":"POST","auth":"mTLS+device_token"}`)},
	}
	for _, c := range bad {
		mustFail(t, wire, "wire:adversarial:"+c.label, c.doc)
	}
}

// ---- pairing/1.0 positives ----

func TestPairingPositives(t *testing.T) {
	pairing := compile(t, "pairing")
	for _, st := range []string{"UNPAIRED", "PAIRING_REQUEST", "DISPLAY_CODE", "KEY_EXCHANGE", "PAIRED", "RE_PAIR", "REVOKED"} {
		mustPass(t, pairing, "pairing:positive:state:"+st, map[string]any{"state": st, "device_id": "dev_fx"})
	}
	mustPass(t, pairing, "pairing:positive:display-code-doc", fixture(t,
		`{"state":"DISPLAY_CODE","device_id":"dev_fx","sas_code":"ABC234","expires_in":300}`))
	mustPass(t, pairing, "pairing:positive:paired-full-doc", fixture(t,
		`{"state":"PAIRED","device_id":"dev_fx","scopes":["T1_read","T2_action","T3_privileged"],"key_store":"DPAPI","token":"x.y","expires_in":86400}`))
}

// ---- pairing/1.0 adversarial: the schema MUST reject ----

func TestPairingAdversarialRejectedBySchema(t *testing.T) {
	pairing := compile(t, "pairing")
	bad := []struct {
		label string
		doc   map[string]any
	}{
		{"state-DELETED", fixture(t, `{"state":"DELETED","device_id":"dev_fx"}`)},
		{"state-paired-lowercase", fixture(t, `{"state":"paired","device_id":"dev_fx"}`)},
		{"state-PENDING-off-enum", fixture(t, `{"state":"PENDING","device_id":"dev_fx"}`)},
		{"sas-lowercase", fixture(t, `{"state":"DISPLAY_CODE","device_id":"dev_fx","sas_code":"abc123"}`)},
		{"sas-5-chars", fixture(t, `{"state":"DISPLAY_CODE","device_id":"dev_fx","sas_code":"ABC23"}`)},
		{"sas-7-chars", fixture(t, `{"state":"DISPLAY_CODE","device_id":"dev_fx","sas_code":"ABC2345"}`)},
		{"sas-dash-inside", fixture(t, `{"state":"DISPLAY_CODE","device_id":"dev_fx","sas_code":"AB-234"}`)},
		{"sas-ambiguous-chars", fixture(t, `{"state":"DISPLAY_CODE","device_id":"dev_fx","sas_code":"I0O1L!"}`)},
		{"scopes-T9_god", fixture(t, `{"state":"PAIRED","device_id":"dev_fx","scopes":["T9_god"]}`)},
		{"scopes-mixed-widened", fixture(t, `{"state":"PAIRED","device_id":"dev_fx","scopes":["T1_read","root_all"]}`)},
		{"scopes-string-not-array", fixture(t, `{"state":"PAIRED","device_id":"dev_fx","scopes":"T1_read"}`)},
		{"missing-device_id(required)", fixture(t, `{"state":"PAIRED"}`)},
		{"missing-state(required)", fixture(t, `{"device_id":"dev_fx"}`)},
		{"key_store-Keychain", fixture(t, `{"state":"PAIRED","device_id":"dev_fx","key_store":"Keychain"}`)},
		{"device_id-wrong-type", fixture(t, `{"state":"PAIRED","device_id":42}`)},
	}
	for _, c := range bad {
		mustFail(t, pairing, "pairing:adversarial:"+c.label, c.doc)
	}
}

// ---- Go-level law where the frozen schema is TOOTHLESS ----
//
// The schema cannot express these; each gap is PINNED (asserting the schema
// still accepts the bad doc, so a future amendment that closes the gap is
// caught) and the invariant is enforced in Go / at the live surface instead.

// Toothless #1: payload_hash is `type: string` in link.wire/1.0 — no 64-hex
// pattern. The 64-char sha256-hex law lives in WireRequest.Validate.
func TestToothlessPayloadHashPatternIsGoLaw(t *testing.T) {
	wire := compile(t, "link.wire")
	notHash := link.WireRequest{
		Endpoint: link.EndpointMounts, Method: "POST", Auth: "mTLS+device_token",
		PayloadHash: "deadbeef", // not 64-hex
	}
	// Pinned gap: the frozen schema ACCEPTS it (string is a string).
	mustPass(t, wire, "wire:gap:payload_hash-deadbeef-passes-schema", marshalThroughJSON(t, notHash))
	// Go law: the implementation must reject it.
	if err := notHash.Validate(); err == nil {
		t.Error("WireRequest.Validate accepted payload_hash=deadbeef; 64-hex law not enforced in Go")
	} else if !errors.Is(err, link.ErrBadRequest) {
		t.Errorf("payload_hash rejection err = %v, want link.ErrBadRequest", err)
	}
	good := notHash
	good.PayloadHash = validHash
	if err := good.Validate(); err != nil {
		t.Errorf("WireRequest.Validate rejected a lawful 64-hex payload_hash: %v", err)
	}
	// Uppercase hex is not sha256 canonical lowercase either — Go rejects.
	up := notHash
	up.PayloadHash = strings.ToUpper(validHash)
	if err := up.Validate(); err == nil {
		t.Error("WireRequest.Validate accepted UPPERCASE payload_hash")
	}
}

// Toothless #2: the endpoint enum CONTAINS /link/v1/commands and the scope
// enum CONTAINS T3_privileged — the schema cannot express "T3 is
// unrepresentable on the link surface" (L1 request-only law) nor "commands
// is not mounted in v1". Go/HTTP law:
//   - WireRequest.Validate refuses endpoint=commands + scope=T3.
//   - Service.Mount refuses any T3 mount (scope law L2).
//   - The live HTTP surface answers 404 for /link/v1/commands.
func TestToothlessT3RequestOnlyLawIsGoLaw(t *testing.T) {
	wire := compile(t, "link.wire")
	t3 := link.WireRequest{
		Endpoint: link.EndpointCommands, Method: "POST", Auth: "mTLS+device_token",
		Scope: link.ScopeT3Privileged,
	}
	// Pinned gap: schema accepts the T3-on-commands envelope.
	mustPass(t, wire, "wire:gap:commands+T3-passes-schema", marshalThroughJSON(t, t3))
	// Go law 1: struct validation refuses it (fail closed, never downgrade).
	if err := t3.Validate(); err == nil {
		t.Error("WireRequest.Validate accepted commands+T3 — L1 request-only law broken")
	} else if !errors.Is(err, link.ErrBadRequest) {
		t.Errorf("commands+T3 err = %v, want link.ErrBadRequest", err)
	}

	// Go law 2: the live surface — a fully-T3 device still cannot mount T3
	// and cannot reach /link/v1/commands at all.
	_, ts := newLinkServer(t, testLinkSecret)
	_, _, _, token := pairLive(t, ts, "dev_t3", []string{link.ScopeT1Read, link.ScopeT2Action, link.ScopeT3Privileged})
	resp, body := postJSON(t, ts.URL+"/link/v1/mounts",
		map[string]any{"endpoint": link.EndpointMounts, "method": "POST", "auth": "mTLS+device_token",
			"device_id": "dev_t3", "work_id": "work:t3", "scope": link.ScopeT3Privileged,
			"purpose_bindings": []string{"work:t3"}},
		map[string]string{"Authorization": bearer(token)})
	wantStatus(t, "T3 mount on a T3 device", resp, body, http.StatusForbidden)

	resp2, body2 := postJSON(t, ts.URL+"/link/v1/commands",
		map[string]any{"endpoint": link.EndpointCommands, "method": "POST", "auth": "mTLS+device_token",
			"scope": link.ScopeT2Action},
		map[string]string{"Authorization": bearer(token)})
	wantStatus(t, "commands route mounted?", resp2, body2, http.StatusNotFound)
}

// Toothless #3: pairing/1.0 has no per-state required lists — it cannot say
// "a DISPLAY_CODE response must carry sas_code" or "PAIRED must carry
// token". Go-level shape law (live): a begin response with the sas_code field
// REMOVED is still schema-valid — proving the pattern only bites when the
// field is present; the presence law is asserted on the live shape.
func TestToothlessStateConditionalPresenceIsGoLaw(t *testing.T) {
	pairing := compile(t, "pairing")
	// Pinned gap: DISPLAY_CODE without sas_code sails through the schema.
	mustPass(t, pairing, "pairing:gap:DISPLAY_CODE-without-sas-is-schema-valid",
		map[string]any{"state": "DISPLAY_CODE", "device_id": "dev_fx"})
	// PAIRED without token likewise (token is not even a schema property).
	mustPass(t, pairing, "pairing:gap:PAIRED-without-token-is-schema-valid",
		map[string]any{"state": "PAIRED", "device_id": "dev_fx"})
	// Live Go law: the real begin/claim responses DO carry the fields.
	_, ts := newLinkServer(t, testLinkSecret)
	begin, claim, code, token := pairLive(t, ts, "dev_present", []string{link.ScopeT1Read})
	if begin["state"] != "DISPLAY_CODE" || code == "" {
		t.Errorf("live begin must be DISPLAY_CODE with a non-empty sas_code: %v", begin)
	}
	if claim["state"] != "PAIRED" || token == "" {
		t.Errorf("live claim must be PAIRED with a non-empty token: %v", claim)
	}
}

// Toothless #4: link.wire/1.0 cannot bind method to endpoint (POST|GET is the
// global enum) — it accepts {"endpoint":"/link/v1/revoke","method":"GET"}.
// The HTTP surface is the law: GET /link/v1/revoke must not 2xx.
func TestToothlessMethodEndpointBindingIsHTTPLaw(t *testing.T) {
	wire := compile(t, "link.wire")
	// Pinned gap:
	mustPass(t, wire, "wire:gap:GET-on-revoke-passes-schema", fixture(t,
		`{"endpoint":"/link/v1/revoke","method":"GET","auth":"mTLS+device_token"}`))
	// Live law: wrong-method endpoints are 404 (surface shape is not
	// discoverable, so it answers like an unknown path — never a mutation).
	_, ts := newLinkServer(t, testLinkSecret)
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/link/v1/revoke", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		t.Fatalf("GET /link/v1/revoke must never 2xx (schema cannot express endpoint-method binding)")
	}
}

// The service layer's scope law on mounts (L2) with a REAL store-backed
// service: T2 without a purpose binding naming the work is refused — the
// envelope itself is schema-valid, so this invariant is pure Go law.
func TestMountsTLawPurposeBindingIsGoLevel(t *testing.T) {
	st := openRealStore(t)
	svc := link.NewService(st.LinkDevices(), link.NewTokenIssuer())
	ctx := context.Background()
	d := &link.Device{DeviceID: "dev_law", Scopes: []string{link.ScopeT1Read, link.ScopeT2Action}, State: link.StatePaired}
	if err := svc.Devices.PutDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	unbound := link.MountRequest{
		WireRequest: link.WireRequest{Endpoint: link.EndpointMounts, Method: "POST", Auth: "mTLS+device_token", Scope: link.ScopeT2Action},
		DeviceID:    "dev_law", WorkID: "work:orphan",
	}
	if _, err := svc.Mount(ctx, d, unbound); err == nil || !errors.Is(err, link.ErrScopeDenied) {
		t.Errorf("ambient T2 mount without purpose binding: got %v, want ErrScopeDenied", err)
	}
	bound := unbound
	bound.PurposeBindings = []string{"work:orphan"}
	if _, err := svc.Mount(ctx, d, bound); err != nil {
		t.Errorf("purpose-bound T2 mount refused: %v", err)
	}
}

// openRealStore is defined in harness_test.go (store + filepath imports live there).

// keep encoding/json import used even if tables above are pruned
var _ = json.Marshal
