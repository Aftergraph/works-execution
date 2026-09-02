package linkconformance

// LIVE-Surface conformance: every response the /link/v1 HTTP layer emits that
// has a frozen shape is validated against the compiled schema — pairing begin
// (DISPLAY_CODE), pairing claim (PAIRED, including the sas_code pattern
// applied to the real offered code), the revoke response (REVOKED) — plus the
// full link.wire/1.0 POST envelopes (built from link.WireRequest structs) as
// actually serialized onto the wire and accepted by the server.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/link"
)

var sasPatternLive = regexp.MustCompile(`^[A-Z0-9]{6}$`)
var sha256HexLive = regexp.MustCompile(`^[a-f0-9]{64}$`)

// The frozen contract identity itself is law: the schema files compiled in
// this suite must carry the registered contract $id, so a swapped or
// re-versioned file fails here before any fixture does.
func TestFrozenContractIdentity(t *testing.T) {
	compile(t, "link.wire") // must at least compile
	compile(t, "pairing")
	for _, c := range []struct {
		file, want string
	}{
		{"link.wire.schema.json", link.ContractVersion},
		{"pairing.schema.json", link.PairingContractVersion},
	} {
		b, err := os.ReadFile(filepath.Join(schemaDir, c.file))
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("parse %s: %v", c.file, err)
		}
		if doc["$id"] != c.want {
			t.Errorf("%s $id = %v, want %q", c.file, doc["$id"], c.want)
		}
	}
}

//  1. pair-begin response must satisfy pairing/1.0 DISPLAY_CODE shape, and the
//  2. claim response must satisfy pairing/1.0 PAIRED shape with the sas_code
//     pattern enforced on the REAL offered code.
func TestLivePairingResponsesValidateAgainstPairing10(t *testing.T) {
	_, ts := newLinkServer(t, testLinkSecret)
	pairing := compile(t, "pairing")

	begin, claim, code, token := pairLive(t, ts, "dev_conf", []string{link.ScopeT1Read, link.ScopeT2Action})

	// The begin response, exactly as emitted by the live server:
	mustPass(t, pairing, "live:pair-begin(DISPLAY_CODE)", begin)
	if begin["state"] != "DISPLAY_CODE" {
		t.Errorf("begin state = %v, want DISPLAY_CODE", begin["state"])
	}
	// The pairing schema cannot express state-conditional presence (a
	// DISPLAY_CODE response MUST carry sas_code) — Go-level law:
	mustString(t, begin, "sas_code")
	mustString(t, begin, "device_id")
	if !sasPatternLive.MatchString(code) {
		t.Errorf("live offered code %q violates pairing/1.0 sas_code pattern", code)
	}

	// The real offered code validated through the schema's own pattern:
	mustPass(t, pairing, "live:offer-code-shapes(6-upper-alnum)", map[string]any{
		"state": "PAIRED", "device_id": "dev_conf", "sas_code": code,
	})
	// And the pattern has teeth on that exact value — lowercased and
	// truncated variants of the REAL code must be schema-rejected:
	mustFail(t, pairing, "live:offer-code-lowercase", map[string]any{
		"state": "PAIRED", "device_id": "dev_conf", "sas_code": strings.ToLower(code),
	})
	mustFail(t, pairing, "live:offer-code-truncated-5", map[string]any{
		"state": "PAIRED", "device_id": "dev_conf", "sas_code": code[:5],
	})

	// The claim response, exactly as emitted:
	mustPass(t, pairing, "live:pair-claim(PAIRED)", claim)
	if claim["state"] != "PAIRED" {
		t.Errorf("claim state = %v, want PAIRED", claim["state"])
	}
	// token + expires_in are required by ADR-0027 §6 but invisible to the
	// frozen schema (no per-state required lists) — Go-level law:
	if token == "" {
		t.Error("claim response carries no device token")
	}
	if ei, _ := claim["expires_in"].(float64); ei != 86400 {
		t.Errorf("claim expires_in = %v, want 86400 (TokenTTL)", claim["expires_in"])
	}
	// scopes echoed on the response must equal what was requested:
	scopes, _ := claim["scopes"].([]any)
	if len(scopes) != 2 {
		t.Errorf("claim scopes = %v, want the 2 requested", claim["scopes"])
	}
}

// 3) Full link.wire/1.0 POST envelopes as serialized from the real
// link.WireRequest-embedding structs, round-tripped through the live server:
// what we marshal must (a) validate against the frozen wire schema and (b) be
// accepted (2xx) by the real HTTP surface.
func TestLiveWireEnvelopesValidateAndAreAcceptedByServer(t *testing.T) {
	_, ts := newLinkServer(t, testLinkSecret)
	wire := compile(t, "link.wire")
	pairing := compile(t, "pairing")

	_, _, _, token := pairLive(t, ts, "dev_wire", []string{link.ScopeT1Read, link.ScopeT2Action})
	const workID = "work:k036-conformance"

	// --- mounts envelope (T2 with purpose binding) ---
	mount := link.MountRequest{
		WireRequest: link.WireRequest{
			Endpoint:       link.EndpointMounts,
			Method:         "POST",
			Auth:           "mTLS+device_token",
			Scope:          link.ScopeT2Action,
			IdempotencyKey: "k036-mount-001",
		},
		DeviceID:        "dev_wire",
		WorkID:          workID,
		PurposeBindings: []string{workID},
	}
	// payload_hash = canonical content address, same law the server applies.
	hash, err := link.PayloadHash(map[string]any{
		"device_id": mount.DeviceID, "work_id": mount.WorkID,
		"scope": mount.Scope, "purpose_bindings": mount.PurposeBindings,
	})
	if err != nil {
		t.Fatalf("payload hash: %v", err)
	}
	mount.PayloadHash = hash

	raw, err := json.Marshal(mount)
	if err != nil {
		t.Fatal(err)
	}
	var envDoc map[string]any
	if err := json.Unmarshal(raw, &envDoc); err != nil {
		t.Fatal(err)
	}
	mustPass(t, wire, "live:mounts-envelope(T2+idem+hash)", envDoc)

	resp, rec := postRaw(t, http.MethodPost, ts.URL+"/link/v1/mounts", raw,
		map[string]string{"Authorization": bearer(token)})
	wantStatus(t, "live mounts POST", resp, rec, http.StatusCreated)
	// The MountRecord response is not a pairing-state document (no frozen
	// schema covers it) — its content-address shape is Go-level law:
	if ph, _ := rec["payload_hash"].(string); !sha256HexLive.MatchString(ph) {
		t.Errorf("mount record payload_hash %q is not 64-char lowercase sha256 hex", ph)
	}
	if id, _ := rec["id"].(string); !strings.HasPrefix(id, "mnt_") {
		t.Errorf("mount record id %q lacks mnt_ prefix", id)
	}

	// --- revoke envelope, then the REVOKED response ---
	revoke := link.RevokeRequest{
		WireRequest: link.WireRequest{
			Endpoint:       link.EndpointRevoke,
			Method:         "POST",
			Auth:           "mTLS+device_token",
			IdempotencyKey: "k036-revoke-001",
		},
		DeviceID: "dev_wire",
	}
	rawR, err := json.Marshal(revoke)
	if err != nil {
		t.Fatal(err)
	}
	var revDoc map[string]any
	if err := json.Unmarshal(rawR, &revDoc); err != nil {
		t.Fatal(err)
	}
	mustPass(t, wire, "live:revoke-envelope", revDoc)

	resp2, rev := postRaw(t, http.MethodPost, ts.URL+"/link/v1/revoke", rawR,
		map[string]string{"Authorization": bearer(token)})
	wantStatus(t, "live revoke POST", resp2, rev, http.StatusOK)
	mustPass(t, pairing, "live:revoke-response(REVOKED)", rev)
	if rev["state"] != "REVOKED" {
		t.Errorf("revoke state = %v, want REVOKED", rev["state"])
	}

	// L4 double enforcement, live: the very same token is dead after revoke.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/link/v1/missions", nil)
	req.Header.Set("Authorization", bearer(token))
	dead, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	dead.Body.Close()
	if dead.StatusCode != http.StatusForbidden {
		t.Errorf("post-revoke missions = %d, want 403", dead.StatusCode)
	}
}

// 4) The missions feed has NO frozen schema (its top-level document carries no
// pairing `state` — it would be schema-REJECTED by pairing/1.0, which proves
// only pair/revoke responses are pairing-law documents). This test pins that
// boundary: missions shape is checked as Go-level structure, and its GET
// envelope is checked against link.wire/1.0.
func TestLiveMissionsFeedShapeAndEnvelope(t *testing.T) {
	_, ts := newLinkServer(t, testLinkSecret)
	wire := compile(t, "link.wire")
	pairing := compile(t, "pairing")

	_, _, _, token := pairLive(t, ts, "dev_missions", []string{link.ScopeT1Read})

	getEnv := link.WireRequest{Endpoint: link.EndpointMissions, Method: "GET", Auth: "mTLS+device_token"}
	mustPass(t, wire, "live:missions-GET-envelope", marshalThroughJSON(t, getEnv))

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/link/v1/missions", nil)
	req.Header.Set("Authorization", bearer(token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if _, isList := out["missions"].([]any); !isList {
		t.Fatalf("missions feed shape: %v", out)
	}
	if out["device_id"] != "dev_missions" {
		t.Errorf("missions device_id = %v", out["device_id"])
	}
	// The missions response document must NOT validate as pairing/1.0 (no
	// state field) — pinning that the pairing contract governs pairing
	// documents only, not the status feed.
	mustFail(t, pairing, "live:missions-response-is-not-a-pairing-doc", out)
}
