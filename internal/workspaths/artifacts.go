package workspaths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultArtifactsDir returns a private directory shared by co-located WORKS
// API and worker processes. It prepares and checks each candidate before
// returning it so a service that cannot use /var/lib/works can fall back to
// its own cache directory.
func DefaultArtifactsDir() string {
	if info, err := os.Stat("/var/lib/works"); err == nil && info.IsDir() {
		if dir := usableArtifactsDir(filepath.Join("/var/lib/works", "artifacts")); dir != "" {
			return dir
		}
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return usableArtifactsDir(filepath.Join(cacheDir, "works", "artifacts"))
	}
	return ""
}

// OpenArtifactsRoot validates and pins an artifact directory. Every existing
// path component must be a real directory, the immediate parent and root must
// not be group- or world-writable on Unix, and all later file access can be
// confined to the returned root handle.
func OpenArtifactsRoot(path string) (*os.Root, string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, "", errors.New("artifact directory is empty")
	}
	rootPath, err := filepath.Abs(path)
	if err != nil {
		return nil, "", fmt.Errorf("resolve artifact root: %w", err)
	}
	rootPath = filepath.Clean(rootPath)
	parentPath := filepath.Dir(rootPath)
	if err := ensureRealDirectoryPath(parentPath); err != nil {
		return nil, "", fmt.Errorf("prepare artifact root parent: %w", err)
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		return nil, "", fmt.Errorf("inspect artifact root parent: %w", err)
	}
	if runtime.GOOS != "windows" && parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, "", fmt.Errorf("artifact root parent %q is group- or world-writable", parentPath)
	}
	if err := makeOrValidateDirectory(rootPath, 0o700); err != nil {
		return nil, "", fmt.Errorf("prepare artifact root: %w", err)
	}
	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		return nil, "", fmt.Errorf("inspect artifact root: %w", err)
	}
	if runtime.GOOS != "windows" && rootInfo.Mode().Perm()&0o022 != 0 {
		return nil, "", fmt.Errorf("artifact root %q is group- or world-writable", rootPath)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", fmt.Errorf("open artifact root: %w", err)
	}
	return root, rootPath, nil
}

// SafeArtifactPathSegment reports whether value is a portable, single path
// component suitable for the legacy <work>/<node>.log layout.
func SafeArtifactPathSegment(value string) bool {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func usableArtifactsDir(path string) string {
	root, absolutePath, err := OpenArtifactsRoot(path)
	if err != nil {
		return ""
	}
	_ = root.Close()
	return absolutePath
}

func makeOrValidateDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%q must be a real directory, not a symlink or file", path)
	}
	return nil
}

func ensureRealDirectoryPath(path string) error {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absolutePath = filepath.Clean(absolutePath)
	volume := filepath.VolumeName(absolutePath)
	current := volume + string(os.PathSeparator)
	if volume == "" {
		current = string(os.PathSeparator)
	}
	remainder := strings.TrimLeft(strings.TrimPrefix(absolutePath, volume), string(os.PathSeparator))
	if remainder == "" {
		return nil
	}
	for _, component := range strings.Split(remainder, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if err := makeOrValidateDirectory(current, 0o700); err != nil {
			return err
		}
	}
	return nil
}
