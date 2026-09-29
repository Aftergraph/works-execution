package api

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/JonasAbde/works-execution/internal/workspaths"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

var (
	errArtifactStoreUnavailable = errors.New("artifact storage is not configured")
	errArtifactContentMissing   = errors.New("artifact bytes are required for remote completion")
	errArtifactTooLarge         = errors.New("artifact exceeds the WORKS transfer limit")
	errArtifactMetadataMismatch = errors.New("artifact metadata does not match its lease or content")
)

// InitializeArtifacts pins the configured artifact directory before serving
// requests or starting workers. The root handle is retained for the lifetime
// of the Server, so replacing the directory entry cannot retarget later I/O.
func (s *Server) InitializeArtifacts() error {
	if s.ArtifactsDir == "" {
		return errArtifactStoreUnavailable
	}
	_, err := s.artifactRootHandle()
	return err
}

// CloseArtifactRoot releases the pinned artifact directory handle after the
// server has stopped accepting requests and drained active handlers.
func (s *Server) CloseArtifactRoot() error {
	s.artifactRootMu.Lock()
	defer s.artifactRootMu.Unlock()
	if s.artifactRoot == nil {
		return nil
	}
	err := s.artifactRoot.Close()
	s.artifactRoot = nil
	s.artifactRootPath = ""
	return err
}

func (s *Server) artifactRootHandle() (*os.Root, error) {
	s.artifactRootMu.Lock()
	defer s.artifactRootMu.Unlock()
	if s.ArtifactsDir == "" {
		return nil, errArtifactStoreUnavailable
	}
	rootPath, err := filepath.Abs(s.ArtifactsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if s.artifactRoot != nil {
		if s.artifactRootPath != rootPath {
			return nil, errors.New("artifact root configuration changed after initialization")
		}
		return s.artifactRoot, nil
	}
	root, rootPath, err := workspaths.OpenArtifactsRoot(rootPath)
	if err != nil {
		return nil, err
	}
	s.artifactRoot = root
	s.artifactRootPath = rootPath
	return root, nil
}

func artifactDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validArtifactDigest(digest string) bool {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func artifactCASRelativePath(digest string) string {
	if !validArtifactDigest(digest) {
		return ""
	}
	return filepath.ToSlash(filepath.Join("cas", "sha256", digest[:2], digest))
}

func safeArtifactPathSegment(value string) bool {
	return workspaths.SafeArtifactPathSegment(value)
}

// persistWorkerArtifact verifies a worker's bytes and places them in the API
// host's content-addressed store. A legacy worker may omit artifact_content
// only when the worker and API already share the canonical local artifact
// path; the client-supplied Path is never opened or trusted.
func (s *Server) persistWorkerArtifact(lease *workgraph.Lease, artifact *workgraph.Artifact, supplied []byte) error {
	if s.ArtifactsDir == "" {
		return errArtifactStoreUnavailable
	}
	if lease == nil || artifact == nil || artifact.NodeID == "" || artifact.NodeID != lease.NodeID {
		return errArtifactMetadataMismatch
	}
	// All names beneath ArtifactsDir are opened relative to its pinned root,
	// even when a worker-created symlink or junction is present.
	root, err := s.artifactRootHandle()
	if err != nil {
		return err
	}

	content := supplied
	if content == nil {
		if !safeArtifactPathSegment(lease.WorkID) || !safeArtifactPathSegment(lease.NodeID) {
			return errArtifactContentMissing
		}
		legacyPath := filepath.Join(lease.WorkID, lease.NodeID+".log")
		content, err = readLimitedArtifactFile(root, legacyPath)
		if errors.Is(err, errArtifactTooLarge) {
			return err
		}
		if err != nil {
			return errArtifactContentMissing
		}
	}
	if int64(len(content)) > workgraph.MaxArtifactBytes {
		return errArtifactTooLarge
	}
	if artifact.Size < 0 || artifact.Size != int64(len(content)) || !validArtifactDigest(artifact.ID) || artifact.ID != artifactDigest(content) {
		return errArtifactMetadataMismatch
	}

	destination := artifactCASRelativePath(artifact.ID)
	if destination == "" {
		return errArtifactMetadataMismatch
	}
	if err := persistCASBytes(root, filepath.FromSlash(destination), content); err != nil {
		return fmt.Errorf("persist verified artifact: %w", err)
	}
	artifact.Path = destination
	return nil
}

func persistCASBytes(root *os.Root, destination string, content []byte) error {
	dir := filepath.Dir(destination)
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if existing, err := readLimitedArtifactFile(root, destination); err == nil {
		if bytes.Equal(existing, content) {
			return nil
		}
		return errors.New("content-addressed path contains different bytes")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	randomName := make([]byte, 16)
	if _, err := cryptorand.Read(randomName); err != nil {
		return err
	}
	tmpName := filepath.Join(dir, ".artifact-"+hex.EncodeToString(randomName))
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmpName)
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmpName, destination); err != nil {
		if existing, readErr := readLimitedArtifactFile(root, destination); readErr == nil && bytes.Equal(existing, content) {
			return nil
		}
		return err
	}
	return nil
}

func readLimitedArtifactFile(root *os.Root, path string) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, workgraph.MaxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > workgraph.MaxArtifactBytes {
		return nil, errArtifactTooLarge
	}
	return content, nil
}

func readVerifiedCASArtifact(root *os.Root, artifact workgraph.Artifact) ([]byte, error) {
	if !validArtifactDigest(artifact.ID) || artifact.Path != artifactCASRelativePath(artifact.ID) ||
		artifact.Size < 0 || artifact.Size > workgraph.MaxArtifactBytes {
		return nil, errArtifactMetadataMismatch
	}
	file, err := root.Open(filepath.FromSlash(artifactCASRelativePath(artifact.ID)))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, workgraph.MaxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != artifact.Size || int64(len(content)) > workgraph.MaxArtifactBytes || artifactDigest(content) != artifact.ID {
		return nil, errArtifactMetadataMismatch
	}
	return content, nil
}

// workArtifactHandler serves an artifact only when its digest is attached to
// the requested Work. The bytes are read from the server-owned CAS path and
// re-hashed before they are returned.
func (s *Server) workArtifactHandler(w http.ResponseWriter, r *http.Request, workID, digest string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	if s.ArtifactsDir == "" {
		writeError(w, http.StatusServiceUnavailable, "artifacts_unavailable", "artifact storage is not configured")
		return
	}
	if !validArtifactDigest(digest) {
		writeError(w, http.StatusNotFound, "artifact_not_found", "artifact not found")
		return
	}
	work, err := s.Store.GetWork(r.Context(), workID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "work_not_found", workID)
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", "failed to load work")
		return
	}
	var matched *workgraph.Artifact
	for _, artifact := range work.Artifacts {
		if artifact.ID == digest && artifactCASRelativePath(artifact.ID) == artifact.Path {
			copy := artifact
			matched = &copy
			break
		}
	}
	if matched == nil {
		writeError(w, http.StatusNotFound, "artifact_not_found", "artifact not found")
		return
	}
	artifactRoot, err := s.artifactRootHandle()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact_store_failed", "artifact storage is unavailable")
		return
	}
	content, err := readVerifiedCASArtifact(artifactRoot, *matched)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "artifact_content_not_found", "artifact content not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "artifact_integrity_failed", "stored artifact content failed metadata or digest verification")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(content)))
	w.Header().Set("Content-Disposition", `attachment; filename="`+digest+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, bytes.NewReader(content))
}
