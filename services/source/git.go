// Package source handles "where does the code come from" for a Work.
// In M1 the only supported source is git, and the only supported
// host is GitHub. The source abstraction is here so that future
// slices (GitLab, Bitbucket, local repos, archives) can plug in
// behind the same interface without touching the API or worker.
//
// All operations in this file are *work-scoped*: the credentials
// passed in are bounded to a single Work, never reused, and never
// persisted in plaintext on disk. The token is held in memory and
// passed to the git CLI via a file descriptor (env, not argv) so it
// does not appear in `ps` output or process listings.
package source

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotGit is returned when the working directory is not a git
// repository after the clone attempt.
var ErrNotGit = errors.New("not a git repository")

// ErrWrongSHA is returned when the post-checkout HEAD does not
// match the requested SHA. This is the integrity check the RFC
// requires — we MUST verify we checked out exactly what the webhook
// said.
var ErrWrongSHA = errors.New("post-checkout HEAD != requested SHA")

// ErrNoGit is returned when the git binary is not on PATH.
var ErrNoGit = errors.New("git binary not on PATH")

// Source is the per-Work handle to a checked-out repository.
// The caller MUST call Cleanup when done. The workspace is on a
// tmpfs-equivalent path (os.TempDir) and will be removed on
// Cleanup; any work that needs persistence should have already
// copied evidence into the work's artifact directory.
type Source struct {
	// WorkDir is the absolute path to the checked-out tree.
	WorkDir string
	// Repo is "<owner>/<name>" — passed through from the webhook.
	Repo string
	// SHA is the 40-char commit that was actually checked out
	// (verified).
	SHA string
	// Ref is the original ref (refs/heads/main or
	// refs/pull/123/head).
	Ref string
	// Cleaned is set to true by Cleanup, so a double-Cleanup is
	// a no-op rather than an error.
	Cleaned bool
}

// Options configures a checkout. Token is REQUIRED for private
// repos. For public repos it can be empty.
type Options struct {
	RepoURL string // e.g. https://github.com/JonasAbde/works-execution.git
	Ref     string // refs/heads/main or refs/pull/123/head (used only for clone hint)
	SHA     string // exact 40-char commit to check out
	Token   string // installation token (not a PAT); "" for public repos
}

// randomTokenName returns a 12-char hex string used as a tmpdir
// suffix so concurrent checkouts don't collide and so the path
// cannot be guessed.
func randomTokenName() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// git is a small wrapper that always uses the same exec.Cmd shape
// and surfaces stderr on non-zero exit.
func git(ctx context.Context, dir string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Checkout clones the repo at the given ref, then checks out the
// exact SHA. Returns a Source whose WorkDir is ready for use. The
// caller must call Source.Cleanup when done.
//
// The function is safe to call concurrently: each call gets its own
// tmpdir, its own credential helper, and its own context.
func Checkout(ctx context.Context, opts Options) (*Source, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoGit, err)
	}
	if opts.RepoURL == "" {
		return nil, errors.New("RepoURL required")
	}
	if len(opts.SHA) != 40 {
		return nil, fmt.Errorf("SHA must be 40 hex chars, got %d", len(opts.SHA))
	}

	// Per-Work tmpdir. Includes the random suffix so concurrent
	// checkouts don't share paths.
	parent := filepath.Join(os.TempDir(), "works-sources")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create sources dir: %w", err)
	}
	workdir := filepath.Join(parent, randomTokenName())

	// Build a credential helper that injects the token only for
	// this single git invocation. We use the `git -c
	// http.<host>.extraheader=AUTHORIZATION: basic <token>` trick
	// which keeps the token out of argv AND out of the .git/config
	// file. This is the standard way to do per-invocation auth
	// without writing to disk.
	env := os.Environ()
	if opts.Token != "" {
		// Strip any prior credential helper env to avoid leaks.
		env = filterEnv(env, "GIT_ASKPASS", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0")
		// Pass the token via an env var we control, then use
		// `git -c credential.helper=` plus a one-shot
		// `credential fill` driven by our own helper. Simpler
		// approach: use the `url.<base>.insteadOf` rewrite plus
		// the basic-auth header. We do that by setting
		// http.<host>.extraheader once, inline.
		host := hostFromURL(opts.RepoURL)
		if host != "" {
			// We use a side-channel env var (WORKS_GIT_TOKEN) and
			// a tiny credential helper script we write next to
			// the workdir, then point git at it via
			// -c credential.helper=.
			if err := os.MkdirAll(workdir, 0o700); err != nil {
				return nil, fmt.Errorf("create workdir: %w", err)
			}
			helperPath := filepath.Join(parent, ".git-cred-helper-"+randomTokenName()+".sh")
			credScript := "#!/bin/sh\n" +
				"case \"$1\" in\n" +
				"  get) echo \"username=x-access-token\"; echo \"password=$WORKS_GIT_TOKEN\" ;;\n" +
				"  store) ;;\n" +
				"  erase) ;;\n" +
				"esac\n"
			// Write the script under parent so workdir is fresh
			// for the clone.
			if err := os.WriteFile(helperPath, []byte(credScript), 0o700); err != nil {
				_ = os.RemoveAll(workdir)
				return nil, fmt.Errorf("write cred helper: %w", err)
			}
			defer os.Remove(helperPath)
			env = append(env,
				"WORKS_GIT_TOKEN="+opts.Token,
				"GIT_TERMINAL_PROMPT=0",
			)
			// The clone call uses -c credential.helper=<abs path>
			cloneArgs := []string{
				"clone",
				"--no-tags",
				"--depth", "1",
				// Use --filter=blob:none to get a partial clone
				// for large repos; the worker can later do a
				// `git fetch --unshallow` if it needs history.
				"--filter=blob:none",
				"-c", "credential.helper=" + helperPath,
				opts.RepoURL,
				workdir,
			}
			// First attempt: assume the token is needed.
			if err := git(ctx, "", env, cloneArgs...); err != nil {
				// Fallback: try without credential helper
				// (e.g. public repo). The clone will succeed
				// only if the repo is actually public.
				if err2 := git(ctx, "", env,
					"clone", "--no-tags", "--depth", "1", "--filter=blob:none",
					opts.RepoURL, workdir,
				); err2 != nil {
					_ = os.RemoveAll(workdir)
					return nil, fmt.Errorf("clone failed (with auth: %v; without: %v)", err, err2)
				}
			}
		} else {
			if err := git(ctx, "", env,
				"clone", "--no-tags", "--depth", "1", "--filter=blob:none",
				opts.RepoURL, workdir,
			); err != nil {
				_ = os.RemoveAll(workdir)
				return nil, fmt.Errorf("clone: %w", err)
			}
		}
	} else {
		if err := git(ctx, "", env,
			"clone", "--no-tags", "--depth", "1", "--filter=blob:none",
			opts.RepoURL, workdir,
		); err != nil {
			_ = os.RemoveAll(workdir)
			return nil, fmt.Errorf("clone: %w", err)
		}
	}

	// A shallow clone starts from the default branch. Fetch the exact
	// webhook ref before checking out its SHA so feature branches and
	// pull-request heads cannot accidentally verify the default branch.
	if opts.Ref != "" {
		if err := git(ctx, workdir, env, "fetch", "--no-tags", "--depth", "1", "origin", opts.Ref); err != nil {
			_ = os.RemoveAll(workdir)
			return nil, fmt.Errorf("fetch %s: %w", opts.Ref, err)
		}
	}

	// Sanity: did we get a repo?
	if _, err := os.Stat(filepath.Join(workdir, ".git")); err != nil {
		_ = os.RemoveAll(workdir)
		return nil, fmt.Errorf("%w: %v", ErrNotGit, err)
	}

	// Checkout the exact SHA. We use `git checkout --detach <sha>`
	// which works regardless of branch state.
	if err := git(ctx, workdir, env, "checkout", "--detach", opts.SHA); err != nil {
		_ = os.RemoveAll(workdir)
		return nil, fmt.Errorf("checkout %s: %w", opts.SHA, err)
	}

	// Verify HEAD matches the requested SHA. This is the integrity
	// check RFC-0003 requires.
	got, err := headSHA(ctx, workdir, env)
	if err != nil {
		_ = os.RemoveAll(workdir)
		return nil, fmt.Errorf("verify HEAD: %w", err)
	}
	if got != opts.SHA {
		_ = os.RemoveAll(workdir)
		return nil, fmt.Errorf("%w: want %s, got %s", ErrWrongSHA, opts.SHA, got)
	}

	return &Source{
		WorkDir: workdir,
		SHA:     got,
		Ref:     opts.Ref,
	}, nil
}

// headSHA returns the 40-char commit at HEAD in the given repo.
func headSHA(ctx context.Context, dir string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Cleanup removes the source's working directory. Safe to call
// multiple times. Returns any os.RemoveAll error, but does not
// return ErrWrongSHA or similar — Cleanup is best-effort.
func (s *Source) Cleanup() error {
	if s == nil || s.Cleaned {
		return nil
	}
	s.Cleaned = true
	if s.WorkDir == "" {
		return nil
	}
	if err := os.RemoveAll(s.WorkDir); err != nil {
		return fmt.Errorf("cleanup %s: %w", s.WorkDir, err)
	}
	return nil
}

// filterEnv returns a copy of env with the named keys removed.
func filterEnv(env []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		for k := range drop {
			if strings.HasPrefix(e, k+"=") {
				goto skip
			}
		}
		out = append(out, e)
	skip:
	}
	return out
}

// hostFromURL extracts the bare host (no port, no path) from a git URL.
// "https://github.com/x/y.git" -> "github.com"
// "ssh://git@github.com:22/x/y.git" -> "github.com"
// "git@github.com:x/y.git"         -> "github.com"
// "file:///path/to/repo"           -> ""
func hostFromURL(u string) string {
	// Strip the scheme first.
	afterScheme := u
	for _, prefix := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(afterScheme, prefix) {
			afterScheme = strings.TrimPrefix(afterScheme, prefix)
			break
		}
	}
	// "file://" is special — we don't need a host for file URLs.
	if strings.HasPrefix(u, "file://") {
		return ""
	}
	// "user@host:path" scp-style.
	if i := strings.Index(afterScheme, "@"); i >= 0 {
		rest := afterScheme[i+1:]
		// rest may be "host:path", "host/path", "host:22", or "host"
		if j := strings.Index(rest, ":"); j >= 0 {
			portOrPath := rest[j+1:]
			if isAllDigits(portOrPath) {
				return rest[:j]
			}
			return rest[:j]
		}
		if j := strings.Index(rest, "/"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	// Otherwise take everything before the first "/" or ":".
	if i := strings.Index(afterScheme, "/"); i >= 0 {
		return afterScheme[:i]
	}
	if i := strings.Index(afterScheme, ":"); i >= 0 {
		return afterScheme[:i]
	}
	return afterScheme
}

// isAllDigits returns true if s is non-empty and every byte is an ASCII digit.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// checkoutTimeout is the per-checkout deadline. Long enough for
// large repos with `git lfs` or sparse checkouts, short enough
// that a stuck clone fails fast.
const checkoutTimeout = 5 * time.Minute

// CheckoutWithDefaultTimeout is a convenience wrapper that applies
// checkoutTimeout to ctx. Useful when the caller has a longer-
// lived context.
func CheckoutWithDefaultTimeout(parent context.Context, opts Options) (*Source, error) {
	ctx, cancel := context.WithTimeout(parent, checkoutTimeout)
	defer cancel()
	return Checkout(ctx, opts)
}
