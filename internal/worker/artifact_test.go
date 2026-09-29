package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/JonasAbde/works-execution/internal/workspaths"
)

func openTestArtifactsRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	root, path, err := workspaths.OpenArtifactsRoot(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close artifacts root: %v", err)
		}
	})
	return root, path
}

func TestWriteArtifactCreatesContentUnderPinnedRoot(t *testing.T) {
	root, rootPath := openTestArtifactsRoot(t)
	content := []byte("successful WORKS execution log")
	path, sum, size, err := writeArtifact(root, rootPath, "wrk-1", "node-1", content)
	if err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256(content)
	if sum != hex.EncodeToString(wantSum[:]) || size != int64(len(content)) {
		t.Fatalf("sum=%q size=%d", sum, size)
	}
	got, err := os.ReadFile(filepath.Join(rootPath, "wrk-1", "node-1.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) || path != filepath.Join(rootPath, "wrk-1", "node-1.log") {
		t.Fatalf("path=%q content=%q", path, got)
	}
}

func TestWriteArtifactRejectsTraversalIDs(t *testing.T) {
	root, rootPath := openTestArtifactsRoot(t)
	base := filepath.Dir(rootPath)
	outsidePath := filepath.Join(base, "escape.log")
	if err := os.WriteFile(outsidePath, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, workID, nodeID string
	}{
		{name: "work id", workID: "../escape", nodeID: "node-1"},
		{name: "node id", workID: "wrk-1", nodeID: "../../escape"},
		{name: "windows separator", workID: `wrk-1`, nodeID: `..\escape`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := writeArtifact(root, rootPath, test.workID, test.nodeID, []byte("overwrite")); err == nil {
				t.Fatal("writeArtifact accepted an unsafe ID")
			}
		})
	}
	got, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "preserve" {
		t.Fatalf("outside file was modified: %q", got)
	}
}

func TestWriteArtifactRejectsWorkDirectorySymlinkEscape(t *testing.T) {
	root, rootPath := openTestArtifactsRoot(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(rootPath, "wrk-1")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, _, _, err := writeArtifact(root, rootPath, "wrk-1", "node-1", []byte("outside")); err == nil {
		t.Fatal("writeArtifact followed a work-directory symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "node-1.log")); !os.IsNotExist(err) {
		t.Fatalf("external artifact was created: %v", err)
	}
}

func TestWriteArtifactReplacesLeafSymlinkWithoutTouchingTarget(t *testing.T) {
	root, rootPath := openTestArtifactsRoot(t)
	if err := os.Mkdir(filepath.Join(rootPath, "wrk-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "wrk-1", "node-1.log")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	content := []byte("inside")
	if _, _, _, err := writeArtifact(root, rootPath, "wrk-1", "node-1", content); err != nil {
		t.Fatal(err)
	}
	outsideBytes, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	insideBytes, err := os.ReadFile(filepath.Join(rootPath, "wrk-1", "node-1.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(outsideBytes) != "preserve" || string(insideBytes) != string(content) {
		t.Fatalf("outside=%q inside=%q", outsideBytes, insideBytes)
	}
}

func TestWriteArtifactReplacesHardlinkWithoutTouchingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard-link test uses Unix file semantics")
	}
	root, rootPath := openTestArtifactsRoot(t)
	workPath := filepath.Join(rootPath, "wrk-1")
	if err := os.Mkdir(workPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(rootPath), "outside.log")
	if err := os.WriteFile(outside, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(workPath, "node-1.log")); err != nil {
		t.Skipf("hard-link creation unavailable: %v", err)
	}
	content := []byte("inside")
	if _, _, _, err := writeArtifact(root, rootPath, "wrk-1", "node-1", content); err != nil {
		t.Fatal(err)
	}
	outsideBytes, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(outsideBytes) != "preserve" {
		t.Fatalf("outside hard-link target was modified: %q", outsideBytes)
	}
}
