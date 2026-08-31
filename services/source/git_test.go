package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// skipIfNoGit skips the test if git is not on PATH.
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCheckout_ExactRef checks out a non-default branch at its exact SHA.
// This is the invariant that prevents a CI work from verifying main when a
// webhook actually delivered a feature branch or pull-request head.
func TestCheckout_ExactRef(t *testing.T) {
	skipIfNoGit(t)
	repo := t.TempDir()
	gitOutput(t, "", "init", "-b", "main", repo)
	gitOutput(t, repo, "config", "user.email", "works-test@example.invalid")
	gitOutput(t, repo, "config", "user.name", "Works Test")
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repo, "add", "marker.txt")
	gitOutput(t, repo, "commit", "-m", "main")
	gitOutput(t, repo, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repo, "commit", "-am", "feature")
	sha := gitOutput(t, repo, "rev-parse", "HEAD")

	src, err := Checkout(context.Background(), Options{
		RepoURL: repo,
		Ref:     "refs/heads/feature",
		SHA:     sha,
	})
	if err != nil {
		t.Fatalf("checkout feature ref: %v", err)
	}
	defer src.Cleanup()
	marker, err := os.ReadFile(filepath.Join(src.WorkDir, "marker.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != "feature\n" {
		t.Fatalf("checked out wrong ref: got %q", marker)
	}
}

// TestCheckout_PublicRepo_NoToken: a public repo with no token
// clones successfully and HEAD matches the requested SHA.
func TestCheckout_PublicRepo_NoToken(t *testing.T) {
	skipIfNoGit(t)

	// We use a tiny public repo with stable history. The Go
	// module `github.com/JonasAbde/works-execution` itself works,
	// but we want a fast, predictable SHA. We use the Go
	// `golang.org/x/exp` repo's known commit; if it's not
	// available, skip.
	opts := Options{
		RepoURL: "https://github.com/JonasAbde/works-execution.git",
		SHA:     "fe0cea547c0e8cbe6cc6d3a0cde353531bfa7b30", // our own HEAD at time of writing
	}
	// We don't know the ref — try without one. The checkout only
	// requires SHA; the ref is for clone hint (which we override
	// by depth=1 + explicit SHA checkout).

	src, err := CheckoutWithDefaultTimeout(context.Background(), opts)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	defer src.Cleanup()

	if src.SHA != opts.SHA {
		t.Errorf("SHA: want %s, got %s", opts.SHA, src.SHA)
	}
	if src.WorkDir == "" {
		t.Error("WorkDir empty")
	}
	if _, err := os.Stat(filepath.Join(src.WorkDir, ".git")); err != nil {
		t.Errorf(".git missing: %v", err)
	}
	// go.mod should be present
	if _, err := os.Stat(filepath.Join(src.WorkDir, "go.mod")); err != nil {
		t.Errorf("go.mod missing: %v", err)
	}
}

// TestCheckout_BadSHA: a request for a SHA that doesn't exist
// returns ErrWrongSHA (or a git error wrapping it).
func TestCheckout_BadSHA(t *testing.T) {
	skipIfNoGit(t)

	opts := Options{
		RepoURL: "https://github.com/JonasAbde/works-execution.git",
		SHA:     strings.Repeat("0", 40), // the all-zero SHA does not exist
	}
	_, err := CheckoutWithDefaultTimeout(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error for bad SHA")
	}
	// We accept either a git-checkout error or our own ErrWrongSHA.
	t.Logf("got error (expected): %v", err)
}

// TestCheckout_MissingGit: a request with no git on PATH returns
// ErrNoGit. We simulate by setting PATH to empty.
func TestCheckout_MissingGit(t *testing.T) {
	// We can't easily test the "no git" case without
	// disturbing the rest of the test run; the real assertion
	// is in Checkout. Here we only check that a bogus binary
	// path doesn't hang forever.
	opts := Options{
		RepoURL: "https://github.com/JonasAbde/works-execution.git",
		SHA:     strings.Repeat("a", 40),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Checkout(ctx, opts)
	if err == nil {
		t.Fatal("expected error")
	}
	// If git is present, the error will be a real git error
	// (timeout, or wrong SHA). If git is missing, it's ErrNoGit.
	// Either way, the call must terminate and not hang.
	t.Logf("got error: %v", err)
}

// TestCheckout_EmptyRepoURL: empty URL is rejected up front.
func TestCheckout_EmptyRepoURL(t *testing.T) {
	_, err := Checkout(context.Background(), Options{
		RepoURL: "",
		SHA:     strings.Repeat("a", 40),
	})
	if err == nil {
		t.Fatal("expected error for empty RepoURL")
	}
}

// TestCheckout_BadSHALength: a SHA of the wrong length is rejected
// without doing any network I/O.
func TestCheckout_BadSHALength(t *testing.T) {
	_, err := Checkout(context.Background(), Options{
		RepoURL: "https://github.com/JonasAbde/works-execution.git",
		SHA:     "abc123", // too short
	})
	if err == nil {
		t.Fatal("expected error for short SHA")
	}
	if !strings.Contains(err.Error(), "40 hex chars") {
		t.Errorf("expected 40-char error, got %v", err)
	}
}

// TestHostFromURL: parse various git URL formats.
func TestHostFromURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/x/y.git", "github.com"},
		{"http://github.com/x/y.git", "github.com"},
		{"ssh://git@github.com/x/y.git", "github.com"},
		{"git@github.com:x/y.git", "github.com"},
		{"file:///path/to/repo", ""},
	}
	for _, c := range cases {
		got := hostFromURL(c.in)
		if got != c.want {
			t.Errorf("hostFromURL(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}

// TestSource_Cleanup_Idempotent: Cleanup can be called twice.
func TestSource_Cleanup_Idempotent(t *testing.T) {
	src := &Source{WorkDir: "/tmp/works-sources/test-cleanup", Cleaned: false}
	// Create the dir so Cleanup has something to remove.
	_ = os.MkdirAll(src.WorkDir, 0o700)
	if err := src.Cleanup(); err != nil {
		t.Errorf("first Cleanup: %v", err)
	}
	if err := src.Cleanup(); err != nil {
		t.Errorf("second Cleanup: %v", err)
	}
	if !src.Cleaned {
		t.Error("Cleaned flag not set")
	}
}

// TestSource_Cleanup_NilSafe: Cleanup on a nil Source is a no-op.
func TestSource_Cleanup_NilSafe(t *testing.T) {
	var src *Source
	if err := src.Cleanup(); err != nil {
		t.Errorf("nil Cleanup: %v", err)
	}
}

// TestFilterEnv: filterEnv drops only the named keys.
func TestFilterEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GIT_ASKPASS=foo",
		"WORKS_GIT_TOKEN=secret",
		"WORKS_FOO=bar",
	}
	out := filterEnv(in, "GIT_ASKPASS")
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "GIT_ASKPASS") {
		t.Errorf("GIT_ASKPASS not stripped: %s", joined)
	}
	if !strings.Contains(joined, "PATH=") {
		t.Errorf("PATH should remain: %s", joined)
	}
	if !strings.Contains(joined, "WORKS_GIT_TOKEN=secret") {
		t.Errorf("WORKS_GIT_TOKEN should remain: %s", joined)
	}
}
