// Package runner provides stack-specific execution strategies for
// real (non-synthetic) repository builds. Where the slice-1+2+5
// `internal/worker` runs arbitrary shell commands on a
// `Node.Run` string, this package detects the stack of the
// checked-out source and dispatches to the right runner.
//
// M1 supports Go only. Node/pnpm is slice 7+. The detection is
// conservative: if `go.mod` is present at the repo root we treat
// it as Go; otherwise we return ErrUnsupportedStack.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrUnsupportedStack is returned by Detect when the repo does not
// match any supported stack. Slice 7+ will add more stacks.
var ErrUnsupportedStack = errors.New("unsupported stack (no go.mod found)")

// ErrNoGo is returned when the go binary is not on PATH.
var ErrNoGo = errors.New("go binary not on PATH")

// Stack is the detected build/test stack of a repository.
type Stack string

const (
	StackGo Stack = "go"
	// Slice 7+:
	// StackNode  Stack = "node"
	// StackPnpm  Stack = "pnpm"
)

// Detect inspects the source directory and returns the stack. It
// does NOT run anything yet — just identifies what the repo is.
func Detect(workdir string) (Stack, error) {
	if _, err := os.Stat(filepath.Join(workdir, "go.mod")); err == nil {
		return StackGo, nil
	}
	return "", fmt.Errorf("%w (workdir=%s)", ErrUnsupportedStack, workdir)
}

// Step is a single named command in a Runner pipeline. Steps run
// sequentially. A non-zero exit fails the run.
type Step struct {
	Name    string            // "go vet", "go test"
	Cmd     string            // argv[0]
	Args    []string          // argv[1:]
	Env     map[string]string // extra env to merge into os.Environ()
	Timeout time.Duration     // per-step deadline
}

// Result is the per-Run outcome. We capture stdout+stderr per
// step so the evidence endpoint can show them later.
type Result struct {
	Stack       Stack           `json:"stack"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at"`
	DurationMs  int64           `json:"duration_ms"`
	Steps       []StepResult    `json:"steps"`
	Failed      bool            `json:"failed"`
	FailedStep  string          `json:"failed_step,omitempty"`
	BuildInfo   BuildInfo       `json:"build_info"`
	GoVersion   string          `json:"go_version,omitempty"`
}

// BuildInfo captures the runtime fingerprint the evidence bundle
// needs. RFC-0003 k-impl-025 reads these fields.
type BuildInfo struct {
	RepoURL  string `json:"repo_url,omitempty"`
	Commit   string `json:"commit,omitempty"`
	GoVer    string `json:"go_version,omitempty"`
	Platform string `json:"platform,omitempty"` // runtime.GOOS + "/" + runtime.GOARCH
	Hostname string `json:"hostname,omitempty"`
	Image    string `json:"image,omitempty"` // docker image digest if container
}

// StepResult is one step's outcome.
type StepResult struct {
	Name       string `json:"name"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"duration_ms"`
}

// Plan returns the default step list for a stack. M1 only knows
// Go: vet then test, both with a 10-minute budget.
func Plan(stack Stack, workdir string) ([]Step, error) {
	switch stack {
	case StackGo:
		return []Step{
			{
				Name:    "go vet",
				Cmd:     "go",
				Args:    []string{"vet", "./..."},
				Env:     map[string]string{"GOFLAGS": "-mod=readonly"},
				Timeout: 10 * time.Minute,
			},
			{
				Name:    "go test",
				Cmd:     "go",
				Args:    []string{"test", "./...", "-count=1", "-timeout", "10m"},
				Env:     map[string]string{"GOFLAGS": "-mod=readonly"},
				Timeout: 10 * time.Minute,
			},
		}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedStack, stack)
}

// Options configures Run.
type Options struct {
	Workdir string // absolute path to the checked-out repo
	Stack   Stack  // detected stack; must match the workdir
	// PlanOverride is optional. If non-nil, used instead of
	// Plan(Stack). Allows tests to inject custom commands.
	PlanOverride []Step
	// BuildInfo is the runtime fingerprint to attach to Result.
	BuildInfo BuildInfo
	// SecretResolver optionally resolves "secret://..." REFs appearing as
	// Step.Env values at execution time (ADR-0022). Nil (the default) keeps
	// the legacy behavior: such a string passes through into the child env
	// literally.
	SecretResolver SecretResolver
	// SecretScope is the lookup scope passed to SecretResolver. Empty means
	// the resolver's own default scope.
	SecretScope string
}

// Run executes the plan for the given stack inside workdir. Returns
// a Result even on failure (with Failed=true and the failing
// step's name) so the caller can build evidence either way.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if _, err := exec.LookPath(string(opts.Stack)); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoGo, opts.Stack)
	}
	plan := opts.PlanOverride
	if plan == nil {
		var err error
		plan, err = Plan(opts.Stack, opts.Workdir)
		if err != nil {
			return nil, err
		}
	}

	res := &Result{
		Stack:     opts.Stack,
		StartedAt: time.Now().UTC(),
		BuildInfo: opts.BuildInfo,
		GoVersion: detectGoVersion(ctx, opts.Workdir),
	}
	for _, step := range plan {
		sr := runStep(ctx, opts.Workdir, step, opts.SecretResolver, opts.SecretScope)
		res.Steps = append(res.Steps, sr)
		if sr.ExitCode != 0 {
			res.Failed = true
			res.FailedStep = step.Name
			break
		}
	}
	res.FinishedAt = time.Now().UTC()
	res.DurationMs = res.FinishedAt.Sub(res.StartedAt).Milliseconds()
	return res, nil
}

// runStep runs a single step and returns its result. Stdout and
// stderr are captured in full (we have no log streaming in M1;
// the worker writes them to the artifact dir under the work id).
//
// resolver/scope implement ADR-0022 execution-time REF resolution: when a
// resolver is configured, Step.Env values shaped like "secret://..." refs
// are resolved just before exec and the values placed only into the child's
// env. A nil resolver leaves step.Env untouched (legacy pass-through).
func runStep(ctx context.Context, workdir string, step Step, resolver SecretResolver, scope string) StepResult {
	start := time.Now().UTC()
	sctx, cancel := context.WithTimeout(ctx, step.Timeout)
	defer cancel()

	stepEnv := step.Env
	if resolver != nil {
		resolved, err := resolver.ResolveEnv(sctx, scope, step.Env)
		if err != nil {
			// Resolution failed: the step fails without exec'ing, reported
			// exactly like the non-ExitError path below (exit -1, message in
			// stderr). The message names the REF - never a value.
			end := time.Now().UTC()
			return StepResult{
				Name:       step.Name,
				ExitCode:   -1,
				Stderr:     truncate(err.Error(), 64*1024),
				DurationMs: end.Sub(start).Milliseconds(),
			}
		}
		stepEnv = resolved
	}

	cmd := exec.CommandContext(sctx, step.Cmd, step.Args...)
	cmd.Dir = workdir
	cmd.Env = mergeEnv(os.Environ(), stepEnv)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	end := time.Now().UTC()
	sr := StepResult{
		Name:       step.Name,
		Stdout:     truncate(stdout.String(), 64*1024),
		Stderr:     truncate(stderr.String(), 64*1024),
		DurationMs: end.Sub(start).Milliseconds(),
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			sr.ExitCode = ee.ExitCode()
		} else {
			// A non-ExitError usually means the process was
			// killed (signal). Surface it as -1 and put the
			// error in stderr so the evidence shows it.
			sr.ExitCode = -1
			if stderr.Len() == 0 {
				sr.Stderr = err.Error()
			}
		}
	}
	return sr
}

// truncate keeps the first max bytes of s, appending a marker if
// anything was cut. This protects the evidence endpoint from
// runaway log sizes.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n\n[truncated, original length %d bytes]", len(s))
}

// mergeEnv returns base plus overrides, with overrides winning.
func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, e := range base {
		for k := range overrides {
			if strings.HasPrefix(e, k+"=") {
				goto skip
			}
		}
		out = append(out, e)
		continue
	skip:
	}
	for k, v := range overrides {
		_ = seen
		out = append(out, k+"="+v)
	}
	return out
}

// detectGoVersion returns the go version string for evidence, or
// empty if it can't be determined.
func detectGoVersion(ctx context.Context, workdir string) string {
	cmd := exec.CommandContext(ctx, "go", "version")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// FormatResult renders a Result as a human-readable string for the
// `works pilot` CLI and the evidence page.
func FormatResult(r *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Stack:      %s\n", r.Stack)
	fmt.Fprintf(&b, "Duration:   %dms\n", r.DurationMs)
	fmt.Fprintf(&b, "Failed:     %v", r.Failed)
	if r.Failed {
		fmt.Fprintf(&b, " (step=%q)", r.FailedStep)
	}
	b.WriteString("\n")
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "  - %-12s exit=%d  %dms\n", s.Name, s.ExitCode, s.DurationMs)
	}
	return b.String()
}

// MarshalJSON is overridden so timestamps are RFC3339 rather than
// the default Go time format. Keeps evidence.json human-friendly.
func (r Result) MarshalJSON() ([]byte, error) {
	type alias Result
	return json.Marshal(&struct {
		StartedAt  string `json:"started_at"`
		FinishedAt string `json:"finished_at"`
		*alias
	}{
		StartedAt:  r.StartedAt.Format(time.RFC3339Nano),
		FinishedAt: r.FinishedAt.Format(time.RFC3339Nano),
		alias:      (*alias)(&r),
	})
}
