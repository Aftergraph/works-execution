package api

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

var (
	errSourceBundleTooLarge = errors.New("source bundle exceeds transfer limit")
	errSourceBundleInvalid  = errors.New("source bundle is invalid")
)

func validSourceBundleDigest(digest string) bool {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func sourceBundleRelativePath(digest string) string {
	if !validSourceBundleDigest(digest) {
		return ""
	}
	return filepath.ToSlash(filepath.Join("sources", "sha256", digest[:2], digest+".tar"))
}

func sourceBundleDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateSourceBundleTar(data []byte) error {
	if len(data) == 0 || int64(len(data)) > workgraph.MaxSourceBundleBytes {
		return errSourceBundleTooLarge
	}
	reader := tar.NewReader(bytes.NewReader(data))
	var files int
	var expanded int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: read tar: %v", errSourceBundleInvalid, err)
		}
		name := strings.ReplaceAll(header.Name, "\\", "/")
		clean := path.Clean(name)
		if name == "" || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || path.IsAbs(clean) || strings.ContainsRune(name, 0) {
			return fmt.Errorf("%w: unsafe path %q", errSourceBundleInvalid, header.Name)
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			files++
			if header.Size < 0 {
				return fmt.Errorf("%w: negative file size", errSourceBundleInvalid)
			}
			expanded += header.Size
		case tar.TypeDir:
			// directories do not count against the file budget
		default:
			return fmt.Errorf("%w: entry %q uses unsupported type %d", errSourceBundleInvalid, header.Name, header.Typeflag)
		}
		if files > workgraph.MaxSourceBundleFiles {
			return fmt.Errorf("%w: more than %d files", errSourceBundleInvalid, workgraph.MaxSourceBundleFiles)
		}
		if expanded > workgraph.MaxSourceExpandedBytes {
			return fmt.Errorf("%w: expanded bytes exceed %d", errSourceBundleInvalid, workgraph.MaxSourceExpandedBytes)
		}
	}
	if files == 0 {
		return fmt.Errorf("%w: bundle contains no files", errSourceBundleInvalid)
	}
	return nil
}

func (s *Server) persistSourceBundle(digest string, content []byte) error {
	if sourceBundleDigest(content) != digest {
		return fmt.Errorf("%w: digest mismatch", errSourceBundleInvalid)
	}
	if err := validateSourceBundleTar(content); err != nil {
		return err
	}
	root, err := s.artifactRootHandle()
	if err != nil {
		return err
	}
	return persistCASBytes(root, filepath.FromSlash(sourceBundleRelativePath(digest)), content)
}

func (s *Server) readSourceBundle(digest string) ([]byte, error) {
	if !validSourceBundleDigest(digest) {
		return nil, os.ErrNotExist
	}
	root, err := s.artifactRootHandle()
	if err != nil {
		return nil, err
	}
	file, err := root.Open(filepath.FromSlash(sourceBundleRelativePath(digest)))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, workgraph.MaxSourceBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > workgraph.MaxSourceBundleBytes {
		return nil, errSourceBundleTooLarge
	}
	if sourceBundleDigest(content) != digest {
		return nil, errSourceBundleInvalid
	}
	if err := validateSourceBundleTar(content); err != nil {
		return nil, err
	}
	return content, nil
}

func (s *Server) validateSourceAdmission(source workgraph.Source) error {
	if source.Type != "bundle" {
		return nil
	}
	content, err := s.readSourceBundle(source.BundleDigest)
	if err != nil {
		return fmt.Errorf("bundle source unavailable: %w", err)
	}
	if int64(len(content)) != source.BundleSize {
		return fmt.Errorf("bundle source size mismatch: got %d want %d", len(content), source.BundleSize)
	}
	return nil
}

// sourceBundleHandler is the authenticated content-addressed source ingress.
// PUT is idempotent by digest; GET is used by workers to materialize admitted source.
func (s *Server) sourceBundleHandler(w http.ResponseWriter, r *http.Request) {
	digest := strings.TrimPrefix(r.URL.Path, "/v1/source-bundles/")
	if !validSourceBundleDigest(digest) {
		writeError(w, http.StatusNotFound, "source_bundle_not_found", "source bundle not found")
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, workgraph.MaxSourceBundleBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "source_bundle_read_failed", err.Error())
			return
		}
		if int64(len(body)) > workgraph.MaxSourceBundleBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "source_bundle_too_large", errSourceBundleTooLarge.Error())
			return
		}
		if err := s.persistSourceBundle(digest, body); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errArtifactStoreUnavailable) {
				status = http.StatusServiceUnavailable
			}
			writeError(w, status, "source_bundle_rejected", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"digest": digest,
			"size": len(body),
			"format": "tar-v1",
			"source_ref": "works-source://sha256/" + digest,
		})
	case http.MethodGet:
		content, err := s.readSourceBundle(digest)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, "source_bundle_not_found", "source bundle not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "source_bundle_read_failed", "source bundle failed integrity validation")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.aftergraph.works.source-bundle+tar")
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Header().Set("ETag", `"`+digest+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, bytes.NewReader(content))
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
	}
}
