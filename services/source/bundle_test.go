package source

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func bundleTar(t *testing.T, entries map[string]string) []byte {
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

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestMaterializeBundleExtractsInsideIsolatedWorkspace(t *testing.T) {
	data := bundleTar(t, map[string]string{
		"package.json": "{}",
		"src/index.js": "console.log('ok')",
	})
	src, err := MaterializeBundle(data, digest(data), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Cleanup()

	got, err := os.ReadFile(filepath.Join(src.WorkDir, "src", "index.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "console.log('ok')" {
		t.Fatalf("unexpected content: %q", got)
	}
	if src.SHA != digest(data) {
		t.Fatalf("source digest: got %s", src.SHA)
	}
}

func TestMaterializeBundleRejectsDigestMismatch(t *testing.T) {
	data := bundleTar(t, map[string]string{"a.txt": "a"})
	_, err := MaterializeBundle(data, string(bytes.Repeat([]byte("f"), 64)), t.TempDir())
	if err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestMaterializeBundleRejectsTraversalAndLinks(t *testing.T) {
	for name, header := range map[string]tar.Header{
		"traversal": {Name: "../escape", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg},
		"symlink": {Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink},
		"hardlink": {Name: "hard", Linkname: "target", Typeflag: tar.TypeLink},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			h := header
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				_, _ = tw.Write([]byte("x"))
			}
			_ = tw.Close()
			data := buf.Bytes()
			if _, err := MaterializeBundle(data, digest(data), t.TempDir()); err == nil {
				t.Fatal("expected unsafe bundle rejection")
			}
		})
	}
}

func TestMaterializeBundleCleanupRemovesWorkspace(t *testing.T) {
	data := bundleTar(t, map[string]string{"a.txt": "a"})
	src, err := MaterializeBundle(data, digest(data), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := src.WorkDir
	if err := src.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after cleanup: %v", err)
	}
}
