package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func makeRepo(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "ci@example.test")
	gitRun(t, dir, "config", "user.name", "CI")
	gitRun(t, dir, "commit", "--allow-empty", "-m", "fixture")
	gitRun(t, dir, "remote", "add", "origin", remote)
	return filepath.Clean(dir)
}
func TestDeriveGitSourceFromHTTPSRemote(t *testing.T) {
	dir := makeRepo(t, "https://github.com/Aftergraph/work-intelligence-v2.git")
	src, err := deriveGitSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if src.Type != "cli" || src.Repository != "Aftergraph/work-intelligence-v2" {
		t.Fatalf("unexpected source identity: %+v", src)
	}
	if src.CloneURL != "https://github.com/Aftergraph/work-intelligence-v2.git" {
		t.Fatalf("unexpected clone URL: %q", src.CloneURL)
	}
	if len(src.SHA) != 40 {
		t.Fatalf("expected exact SHA, got %q", src.SHA)
	}
}

func TestDeriveGitSourceNormalizesSSHRemote(t *testing.T) {
	dir := makeRepo(t, "git@github.com:Aftergraph/work-intelligence-v2.git")
	src, err := deriveGitSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if src.Repository != "Aftergraph/work-intelligence-v2" {
		t.Fatalf("unexpected repository: %q", src.Repository)
	}
	if src.CloneURL != "https://github.com/Aftergraph/work-intelligence-v2.git" {
		t.Fatalf("unexpected clone URL: %q", src.CloneURL)
	}
}
func TestDeriveGitSourceFailsClosedWithoutOrigin(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "ci@example.test")
	gitRun(t, dir, "config", "user.name", "CI")
	gitRun(t, dir, "commit", "--allow-empty", "-m", "fixture")
	if _, err := deriveGitSource(dir); err == nil {
		t.Fatal("expected missing origin to fail closed")
	}
}


func TestStableCIIdempotencyKeyIsDeterministicAndInputBound(t *testing.T) {
	src := workgraph.Source{
		Repository: "Aftergraph/works-execution",
		SHA:        "1111111111111111111111111111111111111111",
	}
	config := []byte("version: 1\nwork:\n  verify: {}\n")

	a := stableCIIdempotencyKey(src, config)
	b := stableCIIdempotencyKey(src, append([]byte(nil), config...))
	if a != b {
		t.Fatalf("same inputs produced different keys: %q != %q", a, b)
	}
	if len(a) != len("works-ci-")+64 {
		t.Fatalf("unexpected key shape: %q", a)
	}

	changedConfig := stableCIIdempotencyKey(src, []byte("version: 1\nwork:\n  verify:\n    nodes: {}\n"))
	if changedConfig == a {
		t.Fatal("config change did not change idempotency key")
	}
	src.SHA = "2222222222222222222222222222222222222222"
	changedSHA := stableCIIdempotencyKey(src, config)
	if changedSHA == a {
		t.Fatal("source SHA change did not change idempotency key")
	}
}
