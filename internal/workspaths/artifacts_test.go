package workspaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenArtifactsRootRejectsSymlinkAncestor(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if root, _, err := OpenArtifactsRoot(filepath.Join(link, "artifacts")); err == nil {
		root.Close()
		t.Fatal("OpenArtifactsRoot accepted a symlinked ancestor")
	}
}

func TestOpenArtifactsRootRejectsWritableDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not Windows ACLs")
	}
	base := t.TempDir()
	t.Run("root", func(t *testing.T) {
		rootPath := filepath.Join(base, "writable-root")
		if err := os.Mkdir(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(rootPath, 0o777); err != nil {
			t.Fatal(err)
		}
		if root, _, err := OpenArtifactsRoot(rootPath); err == nil {
			root.Close()
			t.Fatal("OpenArtifactsRoot accepted a group/world-writable root")
		}
	})
	t.Run("parent", func(t *testing.T) {
		parent := filepath.Join(base, "writable-parent")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		if root, _, err := OpenArtifactsRoot(filepath.Join(parent, "artifacts")); err == nil {
			root.Close()
			t.Fatal("OpenArtifactsRoot accepted a group/world-writable parent")
		}
	})
}
