package ops_test

import (
	"time"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func tmpfsHygieneScriptPath(t *testing.T) string {
	t.Helper()
	p := filepath.Clean(filepath.Join("..", "..", "scripts", "ops", "works-tmpfs-hygiene.sh"))
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("tmpfs hygiene helper missing: %v", err)
	}
	return p
}

func TestWorksTmpfsHygieneScriptSyntax(t *testing.T) {
	p := tmpfsHygieneScriptPath(t)
	if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

func TestWorksTmpfsHygieneSafetyContract(t *testing.T) {
	p := tmpfsHygieneScriptPath(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	required := []string{
		"WORKS_TMPFS_EXCLUDES:-/tmp/works-sources",
		"MIN_AGE_DAYS",
		"WORKS_TMPFS_MIN_AGE_DAYS:-2",
		"status|dry-run|clean",
		"deleted-but-open",
		"protected_paths",
		"rm -rf -- \"$entry\"",
		"find \"$TARGET_ROOT\" -mindepth 1 -maxdepth 1",
	}
	for _, needle := range required {
		if !strings.Contains(s, needle) {
			t.Errorf("missing safety contract fragment %q", needle)
		}
	}
	forbidden := []string{
		"rm -rf -- \"$TARGET_ROOT\"",
		"rm -rf /tmp",
		"set -x",
	}
	for _, needle := range forbidden {
		if strings.Contains(s, needle) {
			t.Errorf("forbidden fragment %q", needle)
		}
	}
	// The age filter must apply before anything is deleted: candidates are
	// the only rm input.
	if !strings.Contains(s, "< <(candidates)") && !strings.Contains(s, "<<(candidates)") {
		t.Errorf("clean must consume candidates() output only")
	}
}

// TestWorksTmpfsHygieneCleanProtectsLivePaths exercises the actual clean
// path against a fixture tree: stale entries are removed, the excluded
// WORKS checkout root and fresh entries survive.
func TestWorksTmpfsHygieneCleanProtectsLivePaths(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	p := tmpfsHygieneScriptPath(t)
	root := t.TempDir()
	stale := filepath.Join(root, "stale-build-dir")
	fresh := filepath.Join(root, "fresh-checkout")
	live := filepath.Join(root, "works-sources")
	for _, d := range []string{stale, fresh, live} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stale, "old.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	for _, d := range []string{stale, live} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("bash", p, "clean")
	cmd.Env = append(os.Environ(),
		"WORKS_TMPFS_ROOT="+root,
		"WORKS_TMPFS_MIN_AGE_DAYS=2",
		"WORKS_TMPFS_EXCLUDES="+live,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("clean failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale entry must be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh entry must survive: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("excluded WORKS checkout root must survive: %v", err)
	}
}
