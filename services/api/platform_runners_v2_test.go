package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

const platformSnapshotToken = "works-platform-snapshot-token-0123456789abcdef"
const platformSnapshotBridge = "works-platform-snapshot-bridge-0123456789abcdef"

func platformSnapshotServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", platformSnapshotBridge)
	st, err := store.Open(filepath.Join(t.TempDir(), "platform-snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := &api.Server{
		Store: st,
		PlatformAPIToken: []byte(platformSnapshotToken),
		AuthEnabled: false,
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	register := `{"runner_id":"wrkr_vds_1","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":["pool:vds"],"os":["linux"],"arch":["amd64"]}}`
	resp, err := http.Post(ts.URL+"/v1/runners/register", "application/json", strings.NewReader(register))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner register status=%d", resp.StatusCode)
	}
	resp, err = http.Post(ts.URL+"/v1/runners/wrkr_vds_1/abi", "application/json",
		strings.NewReader(`{"abi":"rab/1.0","caps":["observe"]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runner abi status=%d", resp.StatusCode)
	}
	return ts
}

func platformGet(t *testing.T, ts *httptest.Server, path, token, bridge string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if bridge != "" {
		req.Header.Set("X-Works-Platform-Bridge", bridge)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestPlatformRunnerSnapshotV2_RequiresPlatformBindingAndReturnsReadOnlyTruth(t *testing.T) {
	ts := platformSnapshotServer(t)

	resp, _ := platformGet(t, ts, "/v2/platform/runners", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d want 401", resp.StatusCode)
	}
	resp, _ = platformGet(t, ts, "/v2/platform/runners", platformSnapshotToken, "wrong-bridge")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bridge status=%d want 401", resp.StatusCode)
	}

	resp, body := platformGet(t, ts, "/v2/platform/runners", platformSnapshotToken, platformSnapshotBridge)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runner snapshot status=%d body=%s", resp.StatusCode, body)
	}
	var list struct {
		Runners []struct {
			RunnerID string `json:"runner_id"`
		} `json:"runners"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Runners) != 1 || list.Runners[0].RunnerID != "wrkr_vds_1" {
		t.Fatalf("unexpected runner snapshot: %s", body)
	}

	resp, body = platformGet(t, ts, "/v2/platform/runners/wrkr_vds_1/abi", platformSnapshotToken, platformSnapshotBridge)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runner abi status=%d body=%s", resp.StatusCode, body)
	}
	var abi map[string]any
	if err := json.Unmarshal(body, &abi); err != nil {
		t.Fatal(err)
	}
	if abi["abi"] != "rab/1.0" {
		t.Fatalf("unexpected abi: %s", body)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/platform/runners", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+platformSnapshotToken)
	req.Header.Set("X-Works-Platform-Bridge", platformSnapshotBridge)
	post, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed && post.StatusCode != http.StatusNotFound {
		t.Fatalf("platform runner mutation unexpectedly mounted: %d", post.StatusCode)
	}
}
