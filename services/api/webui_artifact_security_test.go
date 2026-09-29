package api

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func TestReadLogTailRejectsLegacyLogSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := []byte("outside log must not appear in the work summary")
	if err := os.WriteFile(filepath.Join(outside, "node-1.log"), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "work-1")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	server := &Server{ArtifactsDir: root}
	defer server.CloseArtifactRoot()
	got, err := server.readLogTail("work-1", "node-1")
	if err == nil || got != "" {
		t.Fatalf("readLogTail()=(%q, %v), want no data and a rooted-path error", got, err)
	}
}

func TestArtifactRootHandleSurvivesConfiguredPathReplacement(t *testing.T) {
	rootParent := t.TempDir()
	configuredRoot := filepath.Join(rootParent, "artifacts")
	if err := os.Mkdir(configuredRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	server := &Server{ArtifactsDir: configuredRoot}
	defer server.CloseArtifactRoot()
	lease := &workgraph.Lease{WorkID: "wrk_root_pin", NodeID: "node-1"}
	first := []byte("initialize and pin the configured root")
	firstSum := sha256.Sum256(first)
	firstArtifact := &workgraph.Artifact{
		ID: hex.EncodeToString(firstSum[:]), NodeID: lease.NodeID, Size: int64(len(first)),
	}
	if err := server.persistWorkerArtifact(lease, firstArtifact, first); err != nil {
		t.Fatalf("initialize artifact root: %v", err)
	}

	movedRoot := configuredRoot + "-original"
	if err := os.Rename(configuredRoot, movedRoot); err != nil {
		t.Skipf("configured root cannot be renamed while open: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, configuredRoot); err != nil {
		if restoreErr := os.Rename(movedRoot, configuredRoot); restoreErr != nil {
			t.Fatalf("symlink unavailable (%v) and could not restore artifact root (%v)", err, restoreErr)
		}
		t.Skipf("symlink creation unavailable: %v", err)
	}

	second := []byte("the pinned root must ignore its replacement path")
	secondSum := sha256.Sum256(second)
	secondArtifact := &workgraph.Artifact{
		ID: hex.EncodeToString(secondSum[:]), NodeID: lease.NodeID, Size: int64(len(second)),
	}
	if err := server.persistWorkerArtifact(lease, secondArtifact, second); err != nil {
		t.Fatalf("persist through pinned root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "cas")); !os.IsNotExist(err) {
		t.Fatalf("replacement directory received artifact data, stat error=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(movedRoot, filepath.FromSlash(secondArtifact.Path)))
	if err != nil {
		t.Fatalf("read from original pinned directory: %v", err)
	}
	if string(got) != string(second) {
		t.Fatalf("stored bytes=%q, want %q", got, second)
	}
}

func TestInitializeArtifactsRejectsPreexistingRootSymlink(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	configuredRoot := filepath.Join(parent, "artifacts")
	if err := os.Symlink(outside, configuredRoot); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	server := &Server{ArtifactsDir: configuredRoot}
	defer server.CloseArtifactRoot()
	if err := server.InitializeArtifacts(); err == nil {
		t.Fatal("InitializeArtifacts accepted a preexisting symlink as the artifact root")
	}
}

func TestInitializeArtifactsRejectsSharedWritableParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not represent Windows ACLs")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	server := &Server{ArtifactsDir: filepath.Join(parent, "artifacts")}
	defer server.CloseArtifactRoot()
	if err := server.InitializeArtifacts(); err == nil {
		t.Fatal("InitializeArtifacts accepted a group/world-writable artifact parent")
	}
}
