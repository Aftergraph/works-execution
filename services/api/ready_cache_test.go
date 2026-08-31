package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// TestReadyCacheKey verifies that a cache-enabled node gets a
// cache_key in the /v1/workers/ready response (RFC-0005).
func TestReadyCacheKey(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	w := &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateQueued,
		Source: workgraph.Source{
			Type:       "github_push",
			Repository: "acme/widgets",
			Ref:        "refs/heads/main",
			SHA:        "0123456789abcdef0123456789abcdef01234567",
		},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"vet": {
				ID:    "vet",
				Run:   "go vet ./...",
				Cache: true,
			},
			"test": {
				ID:    "test",
				Run:   "go test ./...",
				Needs: []string{"vet"},
			},
		}},
	}
	if err := s.CreateWork(t.Context(), w); err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{Store: s}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	// Register a runner so the scheduler pool is non-empty (matches
	// production: wrkr_prod_1 in pool avc-core). Without a runner
	// the ready handler takes the legacy no-scheduler path and
	// skips cache fingerprinting entirely.
	regBody := `{"runner_id":"wrkr_test_1","trust_class":"standard","lifecycle_state":"active","capabilities":{"labels":["pool:avc-core"],"os":["linux"],"arch":["amd64"]}}`
	regResp, err := http.Post(ts.URL+"/v1/runners/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	regResp.Body.Close()
	if regResp.StatusCode != http.StatusOK && regResp.StatusCode != http.StatusCreated {
		t.Fatalf("register runner: status %d", regResp.StatusCode)
	}

	resp, err := http.Get(ts.URL + "/v1/workers/ready?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body struct {
		Items []struct {
			WorkID   string `json:"work_id"`
			NodeID   string `json:"node_id"`
			CacheKey string `json:"cache_key"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want 1 (vet)", len(body.Items))
	}
	it := body.Items[0]
	if it.NodeID != "vet" {
		t.Fatalf("node = %q, want vet", it.NodeID)
	}
	if it.CacheKey == "" {
		t.Error("cache_key EMPTY for cache-enabled node — RFC-0005 broken")
	}
	t.Logf("cache_key = %s", it.CacheKey)
}
