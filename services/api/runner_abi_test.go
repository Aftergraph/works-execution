package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// rabTestServer wires the REAL router (srv.Routes()) over a temp store,
// matching the runner_heartbeat_test.go pattern. registerIdentity posts a
// minimal schema-valid runner.Identity so the RAB routes have their
// required prerequisite (integration-order law: RAB requires identity).
func rabTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "rab-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := &api.Server{Store: s}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

func rabRegister(t *testing.T, ts *httptest.Server, runnerID string) {
	t.Helper()
	body := `{"runner_id":"` + runnerID + `","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":["pool:rab-test"],"os":["linux"],"arch":["amd64"]}}`
	resp, err := http.Post(ts.URL+"/v1/runners/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s: status %d", runnerID, resp.StatusCode)
	}
}

func rabPost(t *testing.T, ts *httptest.Server, runnerID, rabBody string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1/runners/"+runnerID+"/abi", "application/json", strings.NewReader(rabBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func rabGet(t *testing.T, ts *httptest.Server, runnerID string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(ts.URL + "/v1/runners/" + runnerID + "/abi")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode abi for %s: %v", runnerID, err)
		}
	}
	return resp, out
}

// TestRunnerABIAdvertiseTable covers the POST/GET advertisement leg: the
// full rab/1.0 law table (accepted shapes, and each fail-closed rejection)
// plus N-1 unknown-field tolerance through the wire.
func TestRunnerABIAdvertiseTable(t *testing.T) {
	tests := []struct {
		name       string
		rabBody    string
		wantStatus int
		wantCode   string // expected errBody.error on non-200
		wantMsgHas string // substring of errBody.message on non-200
	}{
		{
			name:       "observe_only_accepted",
			rabBody:    `{"abi":"rab/1.0","caps":["observe"]}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "control_with_token_accepted",
			rabBody:    `{"abi":"rab/1.0","caps":["control"],"control_token_required":true}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "all_five_caps_accepted",
			rabBody:    `{"abi":"rab/1.0","caps":["screenshot","input","record","observe","control"],"control_token_required":true}`,
			wantStatus: http.StatusOK,
		},
		{
			// The law's teeth: schema-valid (field may be absent) but
			// kernel-illegal — Validate() closes the gap, POST must reject.
			name:       "control_without_token_rejected",
			rabBody:    `{"abi":"rab/1.0","caps":["control"]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "rab_law_violation",
			wantMsgHas: "control capability requires control_token_required=true",
		},
		{
			name:       "control_with_token_false_rejected",
			rabBody:    `{"abi":"rab/1.0","caps":["control"],"control_token_required":false}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "rab_law_violation",
			wantMsgHas: "control_token_required=true",
		},
		{
			name:       "unknown_cap_rejected",
			rabBody:    `{"abi":"rab/1.0","caps":["teleport"]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "rab_law_violation",
			wantMsgHas: "unknown capability",
		},
		{
			name:       "duplicate_cap_rejected",
			rabBody:    `{"abi":"rab/1.0","caps":["observe","observe"]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "rab_law_violation",
			wantMsgHas: "duplicate capability",
		},
		{
			name:       "unknown_abi_version_rejected",
			rabBody:    `{"abi":"rab/2.0","caps":["observe"]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "rab_law_violation",
			wantMsgHas: `abi version must be "rab/1.0"`,
		},
		{
			name:       "trailing_tokens_rejected",
			rabBody:    `{"abi":"rab/1.0","caps":["observe"]} {"abi":"rab/1.0","caps":["record"]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "rab_law_violation",
			wantMsgHas: "trailing tokens",
		},
		{
			name:       "empty_caps_legal",
			rabBody:    `{"abi":"rab/1.0","caps":[]}`,
			wantStatus: http.StatusOK,
		},
		{
			// N-1 tolerance: unknown top-level fields are carried in
			// Extra (asserted on the GET roundtrip below).
			name:       "unknown_fields_tolerated",
			rabBody:    `{"abi":"rab/1.0","caps":["observe"],"runtime_flavor":"chromium-rt","future":{"tier":2}}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "malformed_json_rejected",
			rabBody:    `{"abi":"rab/1.0",`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_json",
		},
	}
	ts := rabTestServer(t)
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rid := "wrkr_abi_" + strings.NewReplacer("_", "", " ", "").Replace(tc.name) + "_" + strconv.Itoa(i)
			rabRegister(t, ts, rid)
			resp, body := rabPost(t, ts, rid, tc.rabBody)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("POST /abi: status %d, want %d (body %s)", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.wantStatus != http.StatusOK {
				var eb struct {
					Error   string `json:"error"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(body, &eb); err != nil {
					t.Fatalf("decode error body: %v (%s)", err, body)
				}
				if eb.Error != tc.wantCode {
					t.Errorf("error code: got %q, want %q", eb.Error, tc.wantCode)
				}
				if tc.wantMsgHas != "" && !strings.Contains(eb.Message, tc.wantMsgHas) {
					t.Errorf("error message: got %q, want substring %q", eb.Message, tc.wantMsgHas)
				}
				// Fail-closed: nothing was stored.
				if g, _ := rabGet(t, ts, rid); g.StatusCode != http.StatusNotFound {
					t.Errorf("rejected RAB must not be stored: GET status %d", g.StatusCode)
				}
				return
			}
			// Accepted: the GET roundtrip must show the same contract.
			_, got := rabGet(t, ts, rid)
			if got["abi"] != "rab/1.0" {
				t.Errorf("roundtrip abi: got %v", got["abi"])
			}
			meta, ok := got["rab_runtime_meta"].(map[string]any)
			if !ok {
				t.Errorf("linkage rab_runtime_meta missing on GET: %v", got)
			} else {
				if meta["runner_id"] != rid {
					t.Errorf("linkage runner_id: got %v, want %s", meta["runner_id"], rid)
				}
				if _, ok := meta["registered_at"]; !ok {
					t.Error("rab_runtime_meta.registered_at missing on GET")
				}
			}
			// caps + control_token_required must round-trip exactly as sent.
			var sent struct {
				Caps []string `json:"caps"`
				CTR  *bool    `json:"control_token_required"`
			}
			if err := json.Unmarshal([]byte(tc.rabBody), &sent); err != nil {
				t.Fatal(err)
			}
			var gotCaps []string
			if arr, isArray := got["caps"].([]any); isArray {
				for _, c := range arr {
					gotCaps = append(gotCaps, c.(string))
				}
			}
			// Kernel representation: an empty (nil) caps slice marshals as
			// JSON null — empty request must round-trip to empty result.
			if strings.Join(gotCaps, ",") != strings.Join(sent.Caps, ",") {
				t.Errorf("roundtrip caps: got %v, want %v", gotCaps, sent.Caps)
			}
			if sent.CTR != nil {
				if got["control_token_required"] != *sent.CTR {
					t.Errorf("roundtrip control_token_required: got %v, want %v",
						got["control_token_required"], *sent.CTR)
				}
			}
			// N-1 unknown-field round-trip through the wire.
			if tc.name == "unknown_fields_tolerated" {
				if got["runtime_flavor"] != "chromium-rt" {
					t.Errorf("Extra unknown field lost: %v", got["runtime_flavor"])
				}
				if m, _ := got["future"].(map[string]any); m == nil || m["tier"] != float64(2) {
					t.Errorf("Extra structured field lost: %v", got["future"])
				}
			}
		})
	}
}

// TestRunnerABINegotiateTable pins the negotiate endpoint against the
// kernel Negotiate law: requested-order-preserving intersection,
// fail-closed unknown/duplicate requested caps, and the
// control_token_required flag on the response.
func TestRunnerABINegotiateTable(t *testing.T) {
	tests := []struct {
		name                string
		rabBody             string
		reqBody             string
		wantStatus          int
		wantCaps            []string
		wantControlTokenReq bool
		wantMsgHas          string
	}{
		{
			name:                "order_preserving_intersection",
			rabBody:             `{"abi":"rab/1.0","caps":["screenshot","observe","control"],"control_token_required":true}`,
			reqBody:             `{"caps":["control","record","observe","screenshot"]}`,
			wantStatus:          http.StatusOK,
			wantCaps:            []string{"control", "observe", "screenshot"},
			wantControlTokenReq: true,
		},
		{
			name:                "subset_grant_no_control",
			rabBody:             `{"abi":"rab/1.0","caps":["observe"]}`,
			reqBody:             `{"caps":["observe","record"]}`,
			wantStatus:          http.StatusOK,
			wantCaps:            []string{"observe"},
			wantControlTokenReq: false,
		},
		{
			name:       "unknown_requested_cap_fail_closed",
			rabBody:    `{"abi":"rab/1.0","caps":["observe"]}`,
			reqBody:    `{"caps":["teleport"]}`,
			wantStatus: http.StatusBadRequest,
			wantMsgHas: "requested \"teleport\"",
		},
		{
			name:       "duplicate_requested_cap_fail_closed",
			rabBody:    `{"abi":"rab/1.0","caps":["observe"]}`,
			reqBody:    `{"caps":["observe","observe"]}`,
			wantStatus: http.StatusBadRequest,
			wantMsgHas: "duplicate requested",
		},
		{
			name:                "empty_request_grants_nothing",
			rabBody:             `{"abi":"rab/1.0","caps":["observe"]}`,
			reqBody:             `{"caps":[]}`,
			wantStatus:          http.StatusOK,
			wantCaps:            []string{},
			wantControlTokenReq: false,
		},
	}
	ts := rabTestServer(t)
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rid := "wrkr_nego_" + strconv.Itoa(i)
			rabRegister(t, ts, rid)
			if resp, b := rabPost(t, ts, rid, tc.rabBody); resp.StatusCode != http.StatusOK {
				t.Fatalf("seed POST /abi: %d %s", resp.StatusCode, b)
			}
			resp, err := http.Post(ts.URL+"/v1/runners/"+rid+"/abi/negotiate", "application/json", strings.NewReader(tc.reqBody))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("negotiate: status %d, want %d (%s)", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.wantStatus != http.StatusOK {
				var eb struct {
					Error   string `json:"error"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(body, &eb); err != nil {
					t.Fatalf("decode negotiate error body: %v (%s)", err, body)
				}
				if !strings.Contains(eb.Message, tc.wantMsgHas) {
					t.Errorf("message: got %q, want substring %q", eb.Message, tc.wantMsgHas)
				}
				return
			}
			var out struct {
				Caps            []string `json:"caps"`
				ControlTokenReq bool     `json:"control_token_required"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("decode negotiate response: %v (%s)", err, body)
			}
			if strings.Join(out.Caps, ",") != strings.Join(tc.wantCaps, ",") {
				t.Errorf("caps: got %v, want %v (order-preserving)", out.Caps, tc.wantCaps)
			}
			if out.ControlTokenReq != tc.wantControlTokenReq {
				t.Errorf("control_token_required: got %v, want %v", out.ControlTokenReq, tc.wantControlTokenReq)
			}
		})
	}
}

// TestRunnerABIOrderLawAndMissing proves the integration-order law: a RAB
// requires a registered identity (404 runner_not_found on unregistered
// ids), and get/negotiate before post answer 404 abi_not_found.
func TestRunnerABIOrderLawAndMissing(t *testing.T) {
	ts := rabTestServer(t)

	// POST /abi against a never-registered runner => 404, and nothing is
	// stored as a side effect (the id also 404s on identity GET).
	resp, body := rabPost(t, ts, "wrkr_orphan_1", `{"abi":"rab/1.0","caps":["observe"]}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("orphan POST /abi: status %d, want 404 (%s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "runner_not_found") {
		t.Errorf("orphan POST body: %s", body)
	}
	for _, u := range []string{"/v1/runners/wrkr_orphan_1/abi", "/v1/runners/wrkr_orphan_1"} {
		g, err := http.Get(ts.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		g.Body.Close()
		if g.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s after rejected orphan post: status %d, want 404", u, g.StatusCode)
		}
	}

	// Identity registered, no RAB yet: get + negotiate are 404 abi_not_found.
	rabRegister(t, ts, "wrkr_nodefault_1")
	if g, _ := rabGet(t, ts, "wrkr_nodefault_1"); g.StatusCode != http.StatusNotFound {
		t.Errorf("GET /abi before POST: status %d, want 404", g.StatusCode)
	}
	n, err := http.Post(ts.URL+"/v1/runners/wrkr_nodefault_1/abi/negotiate", "application/json", strings.NewReader(`{"caps":["observe"]}`))
	if err != nil {
		t.Fatal(err)
	}
	nb, _ := io.ReadAll(n.Body)
	n.Body.Close()
	if n.StatusCode != http.StatusNotFound || !strings.Contains(string(nb), "abi_not_found") {
		t.Errorf("negotiate before POST: status %d body %s", n.StatusCode, nb)
	}
}

// TestRunnerABIOverwriteSemantics pins the documented overwrite model: a
// re-POST replaces the advertisement in place (no DELETE endpoint).
func TestRunnerABIOverwriteSemantics(t *testing.T) {
	ts := rabTestServer(t)
	rid := "wrkr_over_1"
	rabRegister(t, ts, rid)
	if resp, b := rabPost(t, ts, rid, `{"abi":"rab/1.0","caps":["observe","record"]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("first POST: %d %s", resp.StatusCode, b)
	}
	if resp, b := rabPost(t, ts, rid, `{"abi":"rab/1.0","caps":["screenshot"]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("second POST: %d %s", resp.StatusCode, b)
	}
	_, got := rabGet(t, ts, rid)
	caps, _ := got["caps"].([]any)
	if len(caps) != 1 || caps[0] != "screenshot" {
		t.Fatalf("after overwrite caps: got %v, want [screenshot]", got["caps"])
	}
	// Negotiation reflects the replaced advertisement.
	resp, err := http.Post(ts.URL+"/v1/runners/"+rid+"/abi/negotiate", "application/json", strings.NewReader(`{"caps":["record","screenshot"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Caps []string `json:"caps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Caps) != 1 || out.Caps[0] != "screenshot" {
		t.Errorf("negotiate after overwrite: got %v, want [screenshot]", out.Caps)
	}
}

// TestRunnerABIComposesWithProductionRegistry is the k-050 lesson made
// executable: everything above already runs on the REAL router, but this
// test additionally asserts the RAB leg composes with the identity leg on
// the SAME registry instance that production constructs —
// newRunnerRegistry() is allocated lazily by POST /v1/runners/register
// inside registerRunner (the production path), and the advertisement
// stored beside it must coexist with every existing identity read/write
// behavior (backward-compat: identity-only flows stay identical).
func TestRunnerABIComposesWithProductionRegistry(t *testing.T) {
	ts := rabTestServer(t)
	rid := "wrkr_compose_1"

	// Identity leg via the production register endpoint (this is what
	// allocates the registry through newRunnerRegistry()).
	rabRegister(t, ts, rid)

	// RAB leg through the real router on the same server.
	rabBody := `{"abi":"rab/1.0","caps":["observe","control"],"control_token_required":true}`
	if resp, b := rabPost(t, ts, rid, rabBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /abi: %d %s", resp.StatusCode, b)
	}

	// Both legs readable side by side, same runner id.
	idResp, err := http.Get(ts.URL + "/v1/runners/" + rid)
	if err != nil {
		t.Fatal(err)
	}
	var identity map[string]any
	if err := json.NewDecoder(idResp.Body).Decode(&identity); err != nil {
		t.Fatal(err)
	}
	idResp.Body.Close()
	if identity["runner_id"] != rid {
		t.Fatalf("identity leg broken: %v", identity)
	}
	_, abiRec := rabGet(t, ts, rid)
	meta, ok := abiRec["rab_runtime_meta"].(map[string]any)
	if !ok || meta["runner_id"] != rid || abiRec["control_token_required"] != true {
		t.Fatalf("RAB leg not stored beside identity: %v", abiRec)
	}
	if ts, ok := meta["registered_at"].(string); !ok || ts == "" {
		t.Error("rab_runtime_meta.registered_at missing on composed record")
	} else if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("rab_runtime_meta.registered_at not RFC3339: %q", ts)
	}

	// The heartbeat upsert (worker re-POST) must NOT drop the RAB.
	regBody := `{"runner_id":"` + rid + `","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":["pool:rab-test"],"os":["linux"],"arch":["amd64"]}}`
	hb, err := http.Post(ts.URL+"/v1/runners/register", "application/json", bytes.NewReader([]byte(regBody)))
	if err != nil {
		t.Fatal(err)
	}
	hb.Body.Close()
	if g, got := rabGet(t, ts, rid); g.StatusCode != http.StatusOK || got["abi"] != "rab/1.0" {
		t.Errorf("heartbeat upsert dropped the RAB: status %d rec %v", g.StatusCode, got)
	}

	// And the list endpoint (identity leg) still answers — backward-compat.
	l, err := http.Get(ts.URL + "/v1/runners")
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := io.ReadAll(l.Body)
	l.Body.Close()
	if l.StatusCode != http.StatusOK || !strings.Contains(string(lb), rid) {
		t.Errorf("GET /v1/runners broken by RAB wiring: %d %s", l.StatusCode, lb)
	}
}
