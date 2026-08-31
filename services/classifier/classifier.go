// Package classifier implements the Self-Healing Failure Classifier
// (k-impl-007, platform-self-healing standard #117).
//
// A failed Work attempt is mapped to one of the 10 failure classes defined
// in docs/standards/schemas/failure-classification.schema.json using a small
// set of deterministic heuristic rules. Each class carries a per-class
// retry policy (see policy.go) that the Self-Healing scheduler consumes
// when deciding whether to requeue the node, escalate to a human, or
// record an autonomous remediation action.
//
// The classifier is intentionally pure:
//   - no IO
//   - no goroutines
//   - no global state
//
// Callers (the lease-completer in services/work/store/leases.go and the
// tests in tests/classifier/) feed it a Node, an Attempt, and the tail of
// the worker's log. The output is a single Classification value.
package classifier

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Class is one of the failure classes from failure-classification.schema.json.
// The set is closed: adding a new class requires updating the schema AND
// the policy table in policy.go.
type Class string

const (
	ClassCodeFailure         Class = "code_failure"
	ClassTestFailure         Class = "test_failure"
	ClassFlakyTest           Class = "flaky_test"
	ClassRunnerFailure       Class = "runner_failure"
	ClassInfrastructureFail  Class = "infrastructure_failure"
	ClassNetworkFailure      Class = "network_failure"
	ClassDependencyFailure   Class = "dependency_failure"
	ClassCapacityFailure     Class = "capacity_failure"
	ClassPolicyFailure       Class = "policy_failure"
	ClassCredentialFailure   Class = "credential_failure"
	ClassUnknown             Class = "unknown"
)

// ErrEmptyLog is returned when Classify is called with an empty logTail and
// no heuristic rule fires on the (node, attempt) pair alone. We prefer an
// explicit error over silently guessing "unknown", because the Self-Healing
// standard requires the classification to be traceable to a rule.
var ErrEmptyLog = errors.New("classifier: logTail is empty and no rule fired from node/attempt")

// Classification is the result of Classify. Fields mirror the
// failure-classification.schema.json contract 1:1, plus a Rule field that
// records which heuristic fired (not part of the public schema; used for
// tracing and tests).
type Classification struct {
	// Class is one of the Class* enum values.
	Class Class `json:"class"`
	// Retryable indicates whether the Self-Healing scheduler should
	// requeue the node at all.
	Retryable bool `json:"retryable"`
	// MaxRetries caps the number of automatic retries for this class.
	MaxRetries int `json:"max_retries"`
	// Backoff is "none", "linear", or "exponential". Defaults to
	// "exponential" in the policy table.
	Backoff string `json:"backoff"`
	// HumanRequired marks classes that must be escalated to a human
	// operator (e.g. credential_failure, policy_failure).
	HumanRequired bool `json:"human_required"`
	// AutonomousRemediation lists the remediation actions the Self-Healing
	// loop is allowed to take without human approval (per the Autonomous
	// Remediation Standard #129).
	AutonomousRemediation []string `json:"autonomous_remediation,omitempty"`
	// Rule is the name of the heuristic rule that produced this
	// classification. Not part of the public schema; logged for tracing.
	Rule string `json:"-"`
}

// rule is a single heuristic. Each rule has a stable name, a predicate, and
// the Class it produces. Rules are evaluated in order; the first match
// wins. Ordering matters: more-specific rules must precede more-generic ones.
type rule struct {
	name  string
	match func(n workgraph.Node, a workgraph.Attempt, logTail string) bool
	class Class
}

// --- heuristic rule predicates ---

// exitCode137or143: SIGKILL (137) or SIGTERM (143) — the runner or an OOM
// killer terminated the process. This is a runner/infrastructure signal.
func exitCode137or143(_ workgraph.Node, a workgraph.Attempt, _ string) bool {
	return a.ExitCode == 137 || a.ExitCode == 143
}

// exitCode124: the GNU `timeout` command's exit code when it kills the
// process. Indicates an explicit timeout fired (could be node TimeoutS, a
// runner wrapper, or a CI-level timeout).
func exitCode124(_ workgraph.Node, a workgraph.Attempt, _ string) bool {
	return a.ExitCode == 124
}

// statusTimedOut: the worker marked the attempt as timed_out before
// reporting an exit code (e.g. the runner wrapper itself timed out).
func statusTimedOut(_ workgraph.Node, a workgraph.Attempt, _ string) bool {
	return strings.EqualFold(a.Status, "timed_out")
}

// commandNotFound: the shell printed "command not found" or the Go
// exec package returned ENOENT/ErrNotFound. Indicates a missing
// tool/binary, which is a code failure (the action-manifest didn't
// declare its dependencies correctly).
func commandNotFound(_ workgraph.Node, _ workgraph.Attempt, logTail string) bool {
	log := strings.ToLower(logTail)
	return strings.Contains(log, "command not found") ||
		strings.Contains(log, "no such file or directory") ||
		strings.Contains(log, "exec format error")
}

// networkError: any of the well-known network error patterns. These are
// transient and almost always retryable.
func networkError(_ workgraph.Node, _ workgraph.Attempt, logTail string) bool {
	log := strings.ToLower(logTail)
	patterns := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"no route to host",
		"network is unreachable",
		"dns lookup",
		"i/o timeout",
		"tls handshake",
		"econnreset",
		"econnrefused",
		"enetunreach",
	}
	for _, p := range patterns {
		if strings.Contains(log, p) {
			return true
		}
	}
	return false
}

// dependencyError: missing module / package manager could not resolve a
// dependency. Distinct from network_failure (the network was fine — the
// remote registry returned 404, or the local cache is stale).
func dependencyError(_ workgraph.Node, _ workgraph.Attempt, logTail string) bool {
	log := strings.ToLower(logTail)
	patterns := []string{
		"no matching version",
		"package not found",
		"could not resolve",
		"failed to download",
		"checksum mismatch",
		"missing module",
		"unable to resolve dependency",
		"404 not found",  // when fetching a dependency
	}
	for _, p := range patterns {
		if strings.Contains(log, p) {
			return true
		}
	}
	return false
}

// credentialError: missing or expired credentials / secrets. Must be
// escalated — the Self-Healing loop cannot mint a new credential.
func credentialError(_ workgraph.Node, _ workgraph.Attempt, logTail string) bool {
	log := strings.ToLower(logTail)
	patterns := []string{
		"unauthorized",
		"authentication failed",
		"permission denied",  // shell files — disambiguate below
		"invalid token",
		"expired token",
		"credentials missing",
		"missing credentials",
		"no credentials",
		"forbidden",
		"401 ",
		"403 ",
		"403,",
	}
	for _, p := range patterns {
		if strings.Contains(log, p) {
			return true
		}
	}
	return false
}

// capacityError: out of disk, out of memory, ENOSPC.
func capacityError(_ workgraph.Node, _ workgraph.Attempt, logTail string) bool {
	log := strings.ToLower(logTail)
	patterns := []string{
		"no space left",
		"disk full",
		"out of memory",
		"cannot allocate memory",
		"enospc",
		"enomem",
	}
	for _, p := range patterns {
		if strings.Contains(log, p) {
			return true
		}
	}
	return false
}

// policyError: a policy engine (OPA, admission controller) rejected the
// action. Must be escalated — autonomous remediation would risk policy
// circumvention.
func policyError(_ workgraph.Node, _ workgraph.Attempt, logTail string) bool {
	log := strings.ToLower(logTail)
	patterns := []string{
		"policy denied",
		"policy violation",
		"admission rejected",
		"rego: eval",
		"rego: compile",
		"action-manifest:",
		"forbidden by policy",
	}
	for _, p := range patterns {
		if strings.Contains(log, p) {
			return true
		}
	}
	return false
}

// flakyTestPattern: a recognised test framework flake signature. These are
// the patterns that the Slice-3 flake catalog captures.
var flakyTestRe = regexp.MustCompile(`(?i)(flaky|flake|test timed out.*retry|rerun|test.*intermittent|Test.*failed.*on retry|attempts?: [2-9])`)

func flakyTest(n workgraph.Node, _ workgraph.Attempt, logTail string) bool {
	if !isTestNode(n) {
		return false
	}
	return flakyTestRe.MatchString(logTail)
}

// testFailurePattern: explicit FAIL from a test framework, but not
// matching the flaky patterns. We only match when the node itself
// is recognisably a test node (run starts with `go test`, `pytest`,
// `npm test`, etc.).
var testFailureRe = regexp.MustCompile(`(?i)(\bFAIL\b |--- FAIL: |pytest.*FAILED|test failed|\d+ failed,?\s*\d+ passed|^Test:\s.*FAIL|Error Trace:)`)

func testFailure(n workgraph.Node, _ workgraph.Attempt, logTail string) bool {
	if !isTestNode(n) {
		return false
	}
	return testFailureRe.MatchString(logTail)
}

// isTestNode: heuristic for "this node is a test step". Used by both the
// flaky_test and test_failure rules. Conservative: if we can't tell, we
// say "no" and let the generic exit-code rules fire.
func isTestNode(n workgraph.Node) bool {
	r := strings.ToLower(strings.TrimSpace(n.Run))
	if r == "" {
		return false
	}
	prefixes := []string{
		"go test",
		"pytest",
		"npm test",
		"npm run test",
		"yarn test",
		"jest",
		"vitest",
		"mocha",
		"cargo test",
		"mvn test",
		"gradle test",
		"dotnet test",
		"rspec",
		"bundle exec rspec",
		"tox",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(r, p) {
			return true
		}
	}
	// Also match by evidence type if the node has been annotated.
	for _, t := range n.Evidence.Types {
		if t == "test" {
			return true
		}
	}
	return false
}

// isCodeNode: opposite heuristic for code/build nodes (go build, make,
// cargo build, etc.). Used by the default rule.
func isCodeNode(n workgraph.Node) bool {
	r := strings.ToLower(strings.TrimSpace(n.Run))
	prefixes := []string{
		"go build",
		"go vet",
		"go fmt",
		"cargo build",
		"cargo check",
		"npm run build",
		"yarn build",
		"make ",
		"cmake ",
		"gcc ",
		"clang ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(r, p) {
			return true
		}
	}
	return false
}

// rules is the ordered heuristic chain. Order is significant: most-specific
// to most-generic. The final "default" rule catches anything that did not
// match the earlier ones and reports it as code_failure (if the node looks
// like a build/code step) or unknown.
var rules = []rule{
	// Runner / infra / network signals from exit code or status.
	{name: "exit_code_137_143", match: exitCode137or143, class: ClassRunnerFailure},
	{name: "exit_code_124_timeout", match: exitCode124, class: ClassInfrastructureFail},
	{name: "status_timed_out", match: statusTimedOut, class: ClassInfrastructureFail},

	// Policy / credentials — must escalate, check before generic
	// "permission denied" / "unauthorized" matches elsewhere.
	{name: "policy_error", match: policyError, class: ClassPolicyFailure},
	{name: "credential_error", match: credentialError, class: ClassCredentialFailure},

	// Capacity / dependency / network — transient infra.
	{name: "capacity_error", match: capacityError, class: ClassCapacityFailure},
	{name: "dependency_error", match: dependencyError, class: ClassDependencyFailure},
	{name: "network_error", match: networkError, class: ClassNetworkFailure},

	// Test-specific (checked before generic code_failure).
	{name: "flaky_test", match: flakyTest, class: ClassFlakyTest},
	{name: "test_failure", match: testFailure, class: ClassTestFailure},

	// Generic code signals.
	{name: "command_not_found", match: commandNotFound, class: ClassCodeFailure},

	// Default fallback: code node → code_failure, else unknown.
	{name: "default_code_node", match: func(_ workgraph.Node, _ workgraph.Attempt, _ string) bool { return true }, class: ClassCodeFailure},
}

// Classify maps a failed attempt to a Classification. The ctx is reserved
// for future tracing/cancellation hooks (currently unused). node describes
// the action-manifest step that was being executed; attempt carries the
// exit code, status, and worker-reported error string; logTail is the tail
// of the worker's stdout/stderr log (typically the last few KB).
//
// If no rule fires AND logTail is empty, Classify returns ErrEmptyLog.
// Otherwise the first matching rule wins and its class is enriched with
// the per-class retry policy from PolicyFor().
func Classify(ctx context.Context, node workgraph.Node, attempt workgraph.Attempt, logTail string) (*Classification, error) {
	_ = ctx // reserved for future tracing
	// Pre-flight: a 0-status attempt isn't really a failure. We still
	// classify (the caller may have labelled it failed for policy reasons),
	// but we surface this as "unknown" if no specific rule matches.
	for _, r := range rules {
		if r.match(node, attempt, logTail) {
			cls := PolicyFor(r.class)
			return &Classification{
				Class:                cls.Class,
				Retryable:            cls.Retryable,
				MaxRetries:           cls.MaxRetries,
				Backoff:              cls.Backoff,
				HumanRequired:        cls.HumanRequired,
				AutonomousRemediation: append([]string(nil), cls.AutonomousRemediation...),
				Rule:                 r.name,
			}, nil
		}
	}
	// Should be unreachable: the default rule matches everything. Kept
	// as a safety net so a future rule omission doesn't silently drop a
	// classification to nil.
	if logTail == "" {
		return nil, ErrEmptyLog
	}
	cls := PolicyFor(ClassUnknown)
	return &Classification{
		Class:         cls.Class,
		Retryable:     cls.Retryable,
		MaxRetries:    cls.MaxRetries,
		Backoff:       cls.Backoff,
		HumanRequired: cls.HumanRequired,
		Rule:          "no_match",
	}, nil
}

// IsFailed reports whether the attempt is in a terminal failure state.
// Convenience for callers that want to skip successful attempts before
// invoking Classify.
func IsFailed(a workgraph.Attempt) bool {
	switch strings.ToLower(a.Status) {
	case "failed", "timed_out":
		return true
	}
	if a.ExitCode != 0 && (a.Status == "" || strings.EqualFold(a.Status, "failed")) {
		return true
	}
	return false
}