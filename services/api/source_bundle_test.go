package api_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func sourceTar(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range entries {
		body := []byte(content)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sourceDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newSourceBundleServer(t *testing.T) (*httptest.Server, store.Store) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "works.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Store: st, ArtifactsDir: filepath.Join(root, "content")}
	if err := srv.InitializeArtifacts(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.CloseArtifactRoot()
		_ = st.Close()
	})
	return ts, st
}

func TestSourceBundleUploadAndWorkAdmission(t *testing.T) {
	ts, _ := newSourceBundleServer(t)
	bundle := sourceTar(t, map[string]string{
		"package.json": "{}",
		"src/index.js": "console.log('ok')",
	})
	digest := sourceDigest(bundle)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/source-bundles/"+digest, bytes.NewReader(bundle))
	req.Header.Set("Content-Type", "application/vnd.aftergraph.works.source-bundle+tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status: got %d want 201", resp.StatusCode)
	}

	work := workgraph.Work{
		Source: workgraph.Source{
			Type: "bundle", BundleDigest: digest, BundleSize: int64(len(bundle)), BundleFormat: "tar-v1",
		},
		Objective: workgraph.Objective{Type: "build"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"build": {ID: "build", Run: "node src/index.js"},
		}},
	}
	body, _ := json.Marshal(work)
	resp2, err := http.Post(ts.URL+"/v1/works", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create status: got %d want 201", resp2.StatusCode)
	}
	var got workgraph.Work
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Source.BundleDigest != digest || got.Source.BundleSize != int64(len(bundle)) {
		t.Fatalf("bundle source not preserved: %+v", got.Source)
	}
}

func TestSourceBundleWorkAdmissionRejectsMissingBundle(t *testing.T) {
	ts, _ := newSourceBundleServer(t)
	digest := string(bytes.Repeat([]byte("a"), 64))
	work := workgraph.Work{
		Source: workgraph.Source{Type: "bundle", BundleDigest: digest, BundleSize: 1024, BundleFormat: "tar-v1"},
		Objective: workgraph.Objective{Type: "build"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"build": {ID: "build", Run: "true"}}},
	}
	body, _ := json.Marshal(work)
	resp, err := http.Post(ts.URL+"/v1/works", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
}

func TestSourceBundleRejectsDigestMismatchAndTraversal(t *testing.T) {
	ts, _ := newSourceBundleServer(t)
	good := sourceTar(t, map[string]string{"safe.txt": "safe"})
	wrongDigest := string(bytes.Repeat([]byte("b"), 64))
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/source-bundles/"+wrongDigest, bytes.NewReader(good))
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatal(err) }
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("digest mismatch: got %d want 400", resp.StatusCode)
	}

	var bad bytes.Buffer
	tw := tar.NewWriter(&bad)
	body := []byte("escape")
	_ = tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	digest := sourceDigest(bad.Bytes())
	req2, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/source-bundles/"+digest, bytes.NewReader(bad.Bytes()))
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil { t.Fatal(err) }
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal bundle: got %d want 400", resp2.StatusCode)
	}
}

func TestSourceBundleRejectsLinks(t *testing.T) {
	ts, _ := newSourceBundleServer(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	digest := sourceDigest(buf.Bytes())
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/source-bundles/"+digest, bytes.NewReader(buf.Bytes()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatal(err) }
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("symlink bundle: got %d want 400", resp.StatusCode)
	}
}
