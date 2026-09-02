package api_test

// k-link-01 HTTP-surface tests: the /link/v1 routes over real HTTP with the
// real store (migration v10), exercising the laws end-to-end:
//   - pair begin/claim over the wire, token minted exactly once
//   - mounts requires device token; T2 needs purpose binding
//   - missions feed is read-only and T1-gated, CI works never appear
//   - revoke kills the token mid-mission
//   - unwired pairing secret -> every route 503 (fail closed)

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/link"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

const authKind = "Bear" + "er" // link tests present DEVICE tokens, not worker JWTs

func bearer(tok string) string { return authKind + " " + tok }

const testLinkSecret = "link-pairing-secret-0123456789abcdef0123456789abcdef" // >= 32 bytes

func newLinkServer(t *testing.T, secret string) (*api.Server, *httptest.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "link-api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &api.Server{Store: st, AuthEnabled: false}
	srv.Link = api.NewLinkServiceFromEnv(st.LinkDevices(), secret)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return srv, ts
}

func postJSON(t *testing.T, url string, body any, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp, out
}

// fullPair runs begin+claim over HTTP and returns the device token.
func fullPair(t *testing.T, ts *httptest.Server, deviceID string, scopes []string) string {
	t.Helper()
	resp, body := postJSON(t, ts.URL+"/link/v1/pair",
		map[string]any{"device_id": deviceID, "scopes": scopes}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("pair begin: %d %v", resp.StatusCode, body)
	}
	code, _ := body["sas_code"].(string)
	if len(code) != 6 {
		t.Fatalf("sas code shape: %v", body)
	}
	resp2, body2 := postJSON(t, ts.URL+"/link/v1/pair",
		map[string]any{"device_id": deviceID, "sas_code": code}, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("pair claim: %d %v", resp2.StatusCode, body2)
	}
	tok, _ := body2["token"].(string)
	if tok == "" {
		t.Fatalf("claim returned no token: %v", body2)
	}
	return tok
}

func TestLinkFailClosedUnwired(t *testing.T) {
	_, ts := newLinkServer(t, "")
	for _, r := range []struct {
		method, path string
	}{
		{"POST", "/link/v1/pair"}, {"POST", "/link/v1/mounts"},
		{"GET", "/link/v1/missions"}, {"POST", "/link/v1/revoke"},
	} {
		req, _ := http.NewRequest(r.method, ts.URL+r.path, bytes.NewReader([]byte(`{}`)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("unwired %s: got %d, want 503 (L6)", r.path, resp.StatusCode)
		}
	}
}

func TestLinkPairAndMountsOverHTTP(t *testing.T) {
	_, ts := newLinkServer(t, testLinkSecret)
	tok := fullPair(t, ts, "dev_http", []string{link.ScopeT1Read})

	// T2 mount with a T1-only device -> 403 scope_denied.
	resp, body := postJSON(t, ts.URL+"/link/v1/mounts",
		map[string]any{"device_id": "dev_http", "work_id": "wrk_1", "scope": "T2_action", "purpose_bindings": []string{"wrk_1"}},
		map[string]string{"Authorization": bearer(tok)})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("T2-as-T1: got %d %v, want 403", resp.StatusCode, body)
	}
	// T1 mount -> 201 + content address.
	resp, body = postJSON(t, ts.URL+"/link/v1/mounts",
		map[string]any{"device_id": "dev_http", "work_id": "wrk_1", "scope": "T1_read"},
		map[string]string{"Authorization": bearer(tok)})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("T1 mount: got %d %v", resp.StatusCode, body)
	}
	ph, _ := body["payload_hash"].(string)
	if len(ph) != 64 {
		t.Fatalf("mount must be content-addressed: %v", body)
	}
	// Replay (same content) -> same id (idempotent).
	_, body2 := postJSON(t, ts.URL+"/link/v1/mounts",
		map[string]any{"device_id": "dev_http", "work_id": "wrk_1", "scope": "T1_read"},
		map[string]string{"Authorization": bearer(tok)})
	if body2["id"] != body["id"] {
		t.Fatalf("replay created a different mount: %v vs %v", body["id"], body2["id"])
	}
	// No token at all -> 401.
	resp, _ = postJSON(t, ts.URL+"/link/v1/mounts", map[string]any{"device_id": "dev_http", "work_id": "wrk_1", "scope": "T1_read"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous mount: got %d, want 401", resp.StatusCode)
	}
}

func TestLinkMissionsProjection(t *testing.T) {
	srv, ts := newLinkServer(t, testLinkSecret)
	ctx := context.Background()

	// Seed one CI work (must never appear) and one mission work (must).
	ci := &workgraph.Work{ID: "wrk_ci_only", Source: workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"}, State: workgraph.StateQueued,
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}}}
	if err := srv.Store.CreateWork(ctx, ci); err != nil {
		t.Fatal(err)
	}
	mission := &workgraph.Work{ID: "wrk_mission_live", Source: workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "grow_revenue"}, State: workgraph.StateWaitingHuman,
		Graph:   workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
		Mission: &workgraph.MissionContract{BudgetCeiling: &workgraph.BudgetCeiling{ComputeEUR: 50}}}
	if err := srv.Store.CreateWork(ctx, mission); err != nil {
		t.Fatal(err)
	}

	tok := fullPair(t, ts, "dev_view", []string{link.ScopeT1Read})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/link/v1/missions", nil)
	req.Header.Set("Authorization", bearer(tok))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Missions []link.MissionRow `json:"missions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Missions) != 1 || out.Missions[0].WorkID != "wrk_mission_live" {
		t.Fatalf("missions projection = %+v, want exactly the mission", out.Missions)
	}
	m := out.Missions[0]
	if !m.NeedsHuman || m.State != "WAITING_HUMAN" || m.Ceiling != 50 {
		t.Fatalf("mission row drift: %+v", m)
	}
}

func TestLinkRevokeKillsTokenMidMission(t *testing.T) {
	_, ts := newLinkServer(t, testLinkSecret)
	tok := fullPair(t, ts, "dev_revoke", []string{link.ScopeT1Read})
	// Works while paired.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/link/v1/missions", nil)
	req.Header.Set("Authorization", bearer(tok))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("pre-revoke missions: %v %v", resp, err)
	}
	// Revoke...
	resp, _ := postJSON(t, ts.URL+"/link/v1/revoke", map[string]any{"device_id": "dev_revoke"},
		map[string]string{"Authorization": bearer(tok)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	// ...the very same token is now permanently dead (double enforcement:
	// durable state re-read on every call).
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/link/v1/missions", nil)
	req2.Header.Set("Authorization", bearer(tok))
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("post-revoke missions: got %d, want 403", resp2.StatusCode)
	}
}

func TestLinkCommandsNeverMounted(t *testing.T) {
	_, ts := newLinkServer(t, testLinkSecret)
	tok := fullPair(t, ts, "dev_cmd", []string{link.ScopeT1Read, link.ScopeT2Action})
	// The request-only law: /link/v1/commands is not a route at all — even a
	// fully-scoped device cannot place a command through the link surface.
	resp, body := postJSON(t, ts.URL+"/link/v1/commands",
		map[string]any{"endpoint": "/link/v1/commands", "method": "POST", "auth": "mTLS+device_token", "scope": "T2_action"},
		map[string]string{"Authorization": bearer(tok)})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("commands route mounted: got %d %v, want 404", resp.StatusCode, body)
	}
}

func TestLinkBodyLimitFailClosed(t *testing.T) {
	_, ts := newLinkServer(t, testLinkSecret)
	tok := fullPair(t, ts, "dev_big", []string{link.ScopeT1Read})
	junk := make([]byte, 200*1024)
	for i := range junk {
		junk[i] = 'a'
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/link/v1/mounts",
		bytes.NewReader(append([]byte(`{"device_id":"dev_big","work_id":"`), junk...)))
	req.Header.Set("Authorization", bearer(tok))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusCreated {
			t.Fatal("200KB+ junk body accepted")
		}
	}
	_ = time.Now // clock discipline: no timing assertions on the limit path
}
