package api_test

import (
	"bytes"
	"context"
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

func newTestServer(t *testing.T) (*api.Server, *httptest.Server, store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Store: s}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})
	return srv, ts, s
}

func sampleWorkJSON() string {
	w := workgraph.Work{
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{
				"a": {ID: "a", Run: "echo a"},
			},
		},
	}
	b, _ := json.Marshal(w)
	return string(b)
}

func TestCreateWork_201(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(sampleWorkJSON()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want 201", resp.StatusCode)
	}
	var got workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || !strings.HasPrefix(got.ID, "wrk_") {
		t.Errorf("expected server-generated wrk_ id, got %q", got.ID)
	}
	if got.State != workgraph.StateCreated {
		t.Errorf("state: got %s want CREATED", got.State)
	}
}

func TestCreateWork_ValidationError_400(t *testing.T) {
	_, ts, _ := newTestServer(t)
	// missing objective.type and graph.nodes
	bad := `{"source":{"type":"cli"}}`
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestGetWork_RoundTrip(t *testing.T) {
	_, ts, st := newTestServer(t)
	// Create one
	w := &workgraph.Work{
		ID: workgraph.NewID("wrk"),
		Source:    workgraph.Source{Type: "cli", Repository: "acme/x"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}},
		},
	}
	if err := st.CreateWork(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/v1/works/" + w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d", resp.StatusCode)
	}
	var got workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Source.Repository != "acme/x" {
		t.Errorf("source.repository: %q", got.Source.Repository)
	}
}

func TestGetWork_404(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/v1/works/wrk_nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestCancelWork(t *testing.T) {
	_, ts, st := newTestServer(t)
	w := &workgraph.Work{
		ID:       workgraph.NewID("wrk"),
		Source:   workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}},
		},
	}
	if err := st.CreateWork(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/v1/works/"+w.ID+"/cancel", "application/json", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	var got workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.State != workgraph.StateCancelled {
		t.Errorf("state: got %s, want CANCELLED", got.State)
	}
}

func TestHealthz(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d", resp.StatusCode)
	}
}

func TestListWorks(t *testing.T) {
	_, ts, _ := newTestServer(t)
	// create two
	for i := 0; i < 2; i++ {
		resp, err := http.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(sampleWorkJSON()))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %d: status %d", i, resp.StatusCode)
		}
	}
	resp, err := http.Get(ts.URL + "/v1/works")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Count int               `json:"count"`
		Works []*workgraph.Work `json:"works"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 {
		t.Errorf("count: got %d, want 2", body.Count)
	}
}