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
	t.Cleanup(func() {
		ts.Close()
		if err := srv.CloseArtifactRoot(); err != nil {
			t.Errorf("close artifact root: %v", err)
		}
	})
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

func TestCompleteLeaseAcceptsLegacySharedLogArtifact(t *testing.T) {
	ts, st, work, lease, artifactRoot := createArtifactLease(t)
	content := []byte("legacy shared artifact bytes")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	legacyDir := filepath.Join(artifactRoot, work.ID)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "node-1.log"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"exit_code": 0,
		"artifact": workgraph.Artifact{
			ID: digest, NodeID: "node-1", MimeType: "text/plain", Size: int64(len(content)), Path: "../../untrusted.log",
		},
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
		t.Fatalf("complete status=%d, want valid shared legacy artifact accepted", resp.StatusCode)
	}
	storedWork, err := st.GetWork(context.Background(), work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedWork.Artifacts) != 1 || storedWork.Artifacts[0].Path != filepath.ToSlash(filepath.Join("cas", "sha256", digest[:2], digest)) {
		t.Fatalf("stored artifacts=%+v, want canonical CAS locator", storedWork.Artifacts)
	}
	storedBytes, err := os.ReadFile(filepath.Join(artifactRoot, filepath.FromSlash(storedWork.Artifacts[0].Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedBytes, content) {
		t.Fatalf("stored legacy bytes=%q, want %q", storedBytes, content)
	}
}

func TestCompleteLeaseRejectsCASParentSymlinkEscape(t *testing.T) {
	ts, _, _, lease, artifactRoot := createArtifactLease(t)
	content := []byte("artifact must stay within the configured root")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	prefixDir := filepath.Join(artifactRoot, "cas", "sha256")
	if err := os.MkdirAll(prefixDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(prefixDir, digest[:2])); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"exit_code": 0,
		"artifact": workgraph.Artifact{
			ID: digest, NodeID: "node-1", MimeType: "text/plain", Size: int64(len(content)),
		},
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
	if resp.StatusCode == http.StatusOK {
		t.Fatal("completion succeeded through a CAS symlink that escapes ArtifactsDir")
	}
	if _, err := os.Stat(filepath.Join(outside, digest)); !os.IsNotExist(err) {
		t.Fatalf("escaped CAS file was created, stat error=%v", err)
	}
}

func TestCompleteLeaseRejectsLegacyLogSymlinkEscape(t *testing.T) {
	ts, _, work, lease, artifactRoot := createArtifactLease(t)
	content := []byte("legacy worker bytes outside the artifact root")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "node-1.log"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(artifactRoot, work.ID)); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"exit_code": 0,
		"artifact": workgraph.Artifact{
			ID: digest, NodeID: "node-1", MimeType: "text/plain", Size: int64(len(content)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/v1/leases/"+lease.ID+"/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("complete status=%d, want missing legacy artifact rejected with 422", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(artifactRoot, "cas")); !os.IsNotExist(err) {
		t.Fatalf("legacy symlink content was persisted, stat error=%v", err)
	}
}

func TestCompleteLeaseRejectsLegacyLogFileSymlinkEscape(t *testing.T) {
	ts, _, work, lease, artifactRoot := createArtifactLease(t)
	content := []byte("legacy leaf symlink bytes")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	legacyDir := filepath.Join(artifactRoot, work.ID)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideLog := filepath.Join(outside, "outside.log")
	if err := os.WriteFile(outsideLog, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideLog, filepath.Join(legacyDir, "node-1.log")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"exit_code": 0,
		"artifact": workgraph.Artifact{
			ID: digest, NodeID: "node-1", MimeType: "text/plain", Size: int64(len(content)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/v1/leases/"+lease.ID+"/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("complete status=%d, want external legacy symlink rejected", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(artifactRoot, "cas")); !os.IsNotExist(err) {
		t.Fatalf("legacy leaf symlink content was persisted, stat error=%v", err)
	}
}

func TestArtifactHandlersRejectCASSymlinkEscape(t *testing.T) {
	ts, _, work, lease, artifactRoot := createArtifactLease(t)
	content := []byte("artifact reads must stay within the configured root")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	body, err := json.Marshal(map[string]any{
		"exit_code": 0,
		"artifact": workgraph.Artifact{
			ID: digest, NodeID: "node-1", MimeType: "text/plain", Size: int64(len(content)),
		},
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

	prefixDir := filepath.Join(artifactRoot, "cas", "sha256")
	if err := os.RemoveAll(filepath.Join(prefixDir, digest[:2])); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, digest), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(prefixDir, digest[:2])); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
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
		if got.StatusCode == http.StatusOK || bytes.Contains(gotBytes, content) {
			t.Fatalf("GET %s served content through a CAS symlink escape", path)
		}
	}
}

func TestWorkLogsRejectsLegacyLogSymlinkEscape(t *testing.T) {
	ts, _, work, _, artifactRoot := createArtifactLease(t)
	secret := []byte("outside log must not be served")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "node-1.log"), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(artifactRoot, work.ID)); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	resp, err := http.Get(ts.URL + "/v1/works/" + work.ID + "/nodes/node-1/logs")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusOK || bytes.Contains(got, secret) {
		t.Fatal("work logs handler served a legacy log through a symlink escape")
	}
}
