package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func createArtifactLease(t *testing.T) (*httptest.Server, store.Store, *workgraph.Work, *workgraph.Lease, string) {
	t.Helper()
	srv, ts, st := newTestServer(t)
	artifactRoot := t.TempDir()
	srv.ArtifactsDir = artifactRoot
	work := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateQueued,
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"node-1": {ID: "node-1", Run: "Write-Output ready"},
		}},
	}
	if err := st.CreateWork(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	lease, _, err := st.GrantLease(context.Background(), work.ID, "node-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return ts, st, work, lease, artifactRoot
}

func TestCompleteLeaseUploadsAndServesArtifact(t *testing.T) {
	ts, st, work, lease, artifactRoot := createArtifactLease(t)
	content := []byte("Jonas-Lenovo remote artifact\n")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	artifact := workgraph.Artifact{
		ID:       digest,
		NodeID:   "node-1",
		MimeType: "text/plain",
		Size:     int64(len(content)),
		Path:     `C:\worker\artifacts\node-1.log`,
	}
	body, err := json.Marshal(map[string]any{
		"exit_code":        0,
		"artifact":         artifact,
		"artifact_content": content,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/v1/leases/"+lease.ID+"/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete status=%d", resp.StatusCode)
	}

	storedWork, err := st.GetWork(context.Background(), work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedWork.Artifacts) != 1 {
		t.Fatalf("stored artifacts=%d, want 1", len(storedWork.Artifacts))
	}
	storedArtifact := storedWork.Artifacts[0]
	wantPath := filepath.ToSlash(filepath.Join("cas", "sha256", digest[:2], digest))
	if storedArtifact.Path != wantPath {
		t.Fatalf("artifact path=%q, want API CAS locator %q", storedArtifact.Path, wantPath)
	}
	localBytes, err := os.ReadFile(filepath.Join(artifactRoot, filepath.FromSlash(storedArtifact.Path)))
	if err != nil {
		t.Fatalf("read API-owned artifact: %v", err)
	}
	if !bytes.Equal(localBytes, content) {
		t.Fatalf("stored bytes=%q, want %q", localBytes, content)
	}

	for _, path := range []string{
		"/v1/works/" + work.ID + "/artifacts/" + digest,
		"/v1/works/" + work.ID + "/nodes/node-1/logs",
	} {
		got, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		gotBytes, readErr := io.ReadAll(got.Body)
		got.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got.StatusCode != http.StatusOK || !bytes.Equal(gotBytes, content) {
			t.Fatalf("GET %s status=%d bytes=%q", path, got.StatusCode, gotBytes)
		}
	}

	casPath := filepath.Join(artifactRoot, filepath.FromSlash(storedArtifact.Path))
	if err := os.WriteFile(casPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v1/works/" + work.ID + "/artifacts/" + digest,
		"/v1/works/" + work.ID + "/nodes/node-1/logs",
	} {
		got, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		got.Body.Close()
		if got.StatusCode != http.StatusInternalServerError {
			t.Fatalf("GET %s status=%d, want corrupt stored artifact rejected", path, got.StatusCode)
		}
	}
}

func TestCompleteLeaseRejectsSuccessfulResultWithoutArtifact(t *testing.T) {
	ts, st, work, lease, _ := createArtifactLease(t)
	resp, err := http.Post(ts.URL+"/v1/leases/"+lease.ID+"/complete", "application/json", bytes.NewBufferString(`{"exit_code":0}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("complete status=%d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}

	// The failed request cannot complete or release the lease.
	storedLease, err := st.GetLease(context.Background(), lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedLease.Status != workgraph.LeaseActive {
		t.Fatalf("lease status=%q, want active (work=%s)", storedLease.Status, work.ID)
	}
}

func TestCompleteLeaseRejectsArtifactForInactiveLeaseBeforePersisting(t *testing.T) {
	ts, st, work, lease, artifactRoot := createArtifactLease(t)
	if err := st.ReleaseLease(context.Background(), lease.ID, "test release"); err != nil {
		t.Fatal(err)
	}
	content := []byte("stale worker output")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	artifact := workgraph.Artifact{ID: digest, NodeID: "node-1", Size: int64(len(content))}
	body, err := json.Marshal(map[string]any{
		"exit_code":        0,
		"artifact":         artifact,
		"artifact_content": content,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/v1/leases/"+lease.ID+"/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("complete status=%d, want inactive lease conflict (work=%s)", resp.StatusCode, work.ID)
	}
	if _, err := os.Stat(filepath.Join(artifactRoot, "cas")); !os.IsNotExist(err) {
		t.Fatalf("inactive lease wrote CAS content, stat error=%v", err)
	}
}
