package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func TestCompleteLeaseSendsArtifactContent(t *testing.T) {
	content := []byte("worker-local artifact bytes\n")
	var received struct {
		ExitCode        int                  `json:"exit_code"`
		Artifact        *workgraph.Artifact  `json:"artifact"`
		ArtifactContent []byte               `json:"artifact_content"`
		Evidence        []workgraph.Evidence `json:"evidence"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/leases/lease-1/complete" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, HTTP: server.Client()}
	artifact := &workgraph.Artifact{ID: "abcd", NodeID: "node-1", MimeType: "text/plain", Size: int64(len(content)), Path: `C:\worker\artifacts\node-1.log`}
	if err := client.CompleteLease(context.Background(), "lease-1", 1, 0, artifact, content, nil); err != nil {
		t.Fatalf("CompleteLease: %v", err)
	}
	if received.Artifact == nil || received.Artifact.ID != artifact.ID {
		t.Fatalf("artifact metadata = %#v", received.Artifact)
	}
	if string(received.ArtifactContent) != string(content) {
		t.Fatalf("artifact content = %q, want %q", received.ArtifactContent, content)
	}
}
