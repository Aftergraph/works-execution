package source

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// MaterializeBundle expands one admitted WORKS source TAR into an isolated
// per-execution workspace. The digest is checked again at the worker boundary.
func MaterializeBundle(data []byte, digest, root string) (*Source, error) {
	if len(data) == 0 || int64(len(data)) > workgraph.MaxSourceBundleBytes {
		return nil, errors.New("source bundle exceeds transfer limit")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, errors.New("source bundle digest mismatch")
	}
	if root == "" {
		root = os.TempDir()
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("source root must be absolute: %q", root)
	}
	parent := filepath.Join(filepath.Clean(root), "works-sources")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create sources dir: %w", err)
	}
	workdir := filepath.Join(parent, randomTokenName())
	if err := os.Mkdir(workdir, 0o700); err != nil {
		return nil, fmt.Errorf("create bundle workdir: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(workdir)
		}
	}()

	reader := tar.NewReader(bytes.NewReader(data))
	var files int
	var expanded int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read source tar: %w", err)
		}
		name := strings.ReplaceAll(header.Name, "\\", "/")
		clean := path.Clean(name)
		if name == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || strings.ContainsRune(name, 0) {
			return nil, fmt.Errorf("unsafe source path %q", header.Name)
		}
		target := filepath.Join(workdir, filepath.FromSlash(clean))
		rel, err := filepath.Rel(workdir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("source path escapes workspace: %q", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			if files > workgraph.MaxSourceBundleFiles {
				return nil, fmt.Errorf("source bundle exceeds %d files", workgraph.MaxSourceBundleFiles)
			}
			if header.Size < 0 {
				return nil, errors.New("negative source file size")
			}
			expanded += header.Size
			if expanded > workgraph.MaxSourceExpandedBytes {
				return nil, fmt.Errorf("source bundle exceeds expanded size limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return nil, err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return nil, err
			}
			written, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if written != header.Size {
				return nil, io.ErrUnexpectedEOF
			}
		default:
			return nil, fmt.Errorf("unsupported source tar entry type %d for %q", header.Typeflag, header.Name)
		}
	}
	if files == 0 {
		return nil, errors.New("source bundle contains no files")
	}
	ok = true
	return &Source{WorkDir: workdir, SHA: digest, Ref: "works-source://sha256/" + digest}, nil
}
