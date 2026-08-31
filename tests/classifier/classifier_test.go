// Package classifier_test covers the Self-Healing Failure Classifier
// (k-impl-007, platform-self-healing standard #117).
//
// Each test feeds Classify a representative (node, attempt, logTail)
// triple for one of the 10 failure classes from
// docs/standards/schemas/failure-classification.schema.json and asserts:
//
//   1. The Class field matches the expected class.
//   2. The per-class RetryPolicy fields (Retryable, MaxRetries,
//      HumanRequired) match PolicyFor() for that class — i.e. the
//      classifier applies the policy table, not just labels.
//   3. The Rule field is set to a non-empty heuristic name (proves
//      the classifier ran a real rule, not the catch-all).
//
// The tests are external (package classifier_test) so they exercise the
// public API only and never reach into unexported helpers.
package classifier_test

import (
	"context"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/classifier"
)

// classifyOK calls Classify with the supplied inputs and fails the test
// if Classify returns an error. It returns the classification for
// subsequent assertions.
func classifyOK(t *testing.T, node workgraph.Node, attempt workgraph.Attempt, logTail string) *classifier.Classification {
	t.Helper()
	cls, err := classifier.Classify(context.Background(), node, attempt, logTail)
	if err != nil {
		t.Fatalf("Classify returned err: %v (class=%s, log=%q)", err, attempt.Status, logTail)
	}
	if cls == nil {
		t.Fatalf("Classify returned nil classification")
	}
	if cls.Class == "" {
		t.Fatalf("Classify returned empty Class")
	}
	if cls.Rule == "" {
		t.Errorf("Classify returned empty Rule (traceability broken)")
	}
	return cls
}

// assertClass asserts the classification is exactly the expected class
// AND that the per-class policy fields match PolicyFor(). This is the
// primary contract of the classifier.
func assertClass(t *testing.T, got *classifier.Classification, want classifier.Class) {
	t.Helper()
	if got.Class != want {
		t.Errorf("Class: got %q, want %q (rule=%q)", got.Class, want, got.Rule)
	}
	pol := classifier.PolicyFor(want)
	if got.Retryable != pol.Retryable {
		t.Errorf("Retryable for %q: got %v, want %v", want, got.Retryable, pol.Retryable)
	}
	if got.MaxRetries != pol.MaxRetries {
		t.Errorf("MaxRetries for %q: got %d, want %d", want, got.MaxRetries, pol.MaxRetries)
	}
	if got.Backoff != pol.Backoff {
		t.Errorf("Backoff for %q: got %q, want %q", want, got.Backoff, pol.Backoff)
	}
	if got.HumanRequired != pol.HumanRequired {
		t.Errorf("HumanRequired for %q: got %v, want %v", want, got.HumanRequired, pol.HumanRequired)
	}
}

// TestClassify_RunnerFailure: exit code 137 (SIGKILL) is the canonical
// runner-killed signal (OOM, host shutdown, watchdog).
func TestClassify_RunnerFailure(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "build", Run: "go build ./..."},
		workgraph.Attempt{Status: "failed", ExitCode: 137, Error: "killed"},
		"signal: killed")
	assertClass(t, got, classifier.ClassRunnerFailure)
}

// TestClassify_InfrastructureFailure_TimedOut: exit code 124 from the
// GNU `timeout` wrapper indicates an explicit node/run timeout fired.
func TestClassify_InfrastructureFailure_TimedOut(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "test", Run: "go test ./..."},
		workgraph.Attempt{Status: "failed", ExitCode: 124, Error: "timeout"},
		"timeout: sending signal TERM")
	assertClass(t, got, classifier.ClassInfrastructureFail)
}

// TestClassify_InfrastructureFailure_Status: status=timed_out also routes
// to infrastructure_failure even when the exit code is 0 (some wrappers
// report a clean exit on timeout).
func TestClassify_InfrastructureFailure_Status(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "test", Run: "go test ./..."},
		workgraph.Attempt{Status: "timed_out", ExitCode: 0, Error: "wrapper timeout"},
		"elapsed: 1800s, deadline exceeded")
	assertClass(t, got, classifier.ClassInfrastructureFail)
}

// TestClassify_NetworkFailure: classic transient network failure markers.
// Highly retryable, exponential backoff, no human required.
func TestClassify_NetworkFailure(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "fetch", Run: "curl -sSf https://example.com/x"},
		workgraph.Attempt{Status: "failed", ExitCode: 7, Error: "connection refused"},
		"curl: (7) Failed to connect to example.com port 443: Connection refused")
	assertClass(t, got, classifier.ClassNetworkFailure)
	if got.MaxRetries < 3 {
		t.Errorf("network_failure should allow several retries, got MaxRetries=%d", got.MaxRetries)
	}
}

// TestClassify_DependencyFailure: package manager could not resolve a
// dependency. Distinct from network_failure (network worked, the registry
// returned 404 / checksum mismatch).
func TestClassify_DependencyFailure(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "build", Run: "npm install"},
		workgraph.Attempt{Status: "failed", ExitCode: 1, Error: "npm error"},
		"npm ERR! 404 Not Found - GET https://registry.npmjs.org/missing-1.2.3")
	assertClass(t, got, classifier.ClassDependencyFailure)
}

// TestClassify_CapacityFailure: out of disk / out of memory.
func TestClassify_CapacityFailure(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "build", Run: "go build ./..."},
		workgraph.Attempt{Status: "failed", ExitCode: 1, Error: "ENOSPC"},
		"tempdir: write failed: no space left on device")
	assertClass(t, got, classifier.ClassCapacityFailure)
}

// TestClassify_PolicyFailure: an OPA/admission policy denied the action.
// MUST require a human — autonomous policy changes are not permitted.
func TestClassify_PolicyFailure(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "deploy", Run: "kubectl apply -f deploy.yaml"},
		workgraph.Attempt{Status: "failed", ExitCode: 1, Error: "denied"},
		"action-manifest: side_effect 'state_mutation' denied by policy: production_access requires approved evidence")
	assertClass(t, got, classifier.ClassPolicyFailure)
	if !got.HumanRequired {
		t.Error("policy_failure must require human review")
	}
	if got.Retryable {
		t.Error("policy_failure must not be auto-retryable")
	}
}

// TestClassify_CredentialFailure: expired or missing credentials.
// MUST require a human — credentials cannot be rotated autonomously.
func TestClassify_CredentialFailure(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "push", Run: "docker push registry.example.com/img:1"},
		workgraph.Attempt{Status: "failed", ExitCode: 1, Error: "unauthorized"},
		"docker: Error response from daemon: 401 Unauthorized: missing credentials")
	assertClass(t, got, classifier.ClassCredentialFailure)
	if !got.HumanRequired {
		t.Error("credential_failure must require human review")
	}
	if got.Retryable {
		t.Error("credential_failure must not be auto-retryable")
	}
}

// TestClassify_CodeFailure_CommandNotFound: a missing tool is a real
// code/build problem (the action-manifest didn't declare its deps).
func TestClassify_CodeFailure_CommandNotFound(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "build", Run: "go build ./..."},
		workgraph.Attempt{Status: "failed", ExitCode: 127, Error: "command not found"},
		"sh: 1: go: command not found")
	assertClass(t, got, classifier.ClassCodeFailure)
}

// TestClassify_TestFailure: a unit/integration test assertion failed.
// The node is recognisably a test step (go test ./...).
func TestClassify_TestFailure(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "test", Run: "go test ./..."},
		workgraph.Attempt{Status: "failed", ExitCode: 1, Error: "FAIL: TestFoo"},
		"--- FAIL: TestFoo (0.01s)\n    foo_test.go:42: expected 1, got 2\nFAIL\nexit status 1\nFAIL\tgithub.com/acme/x\t0.012s")
	assertClass(t, got, classifier.ClassTestFailure)
}

// TestClassify_FlakyTest: a recognised flake pattern (retry pass count
// in the log). Distinct from a real test_failure: retries are the entire
// point, and the policy allows more attempts with linear backoff.
func TestClassify_FlakyTest(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "test", Run: "go test ./..."},
		workgraph.Attempt{Status: "failed", ExitCode: 1, Error: "flaky"},
		"--- FAIL: TestBar (0.01s)\n    attempts: 3, flaky: rerun")
	assertClass(t, got, classifier.ClassFlakyTest)
	if got.MaxRetries < 2 {
		t.Errorf("flaky_test should allow several retries, got MaxRetries=%d", got.MaxRetries)
	}
	if got.Backoff != "linear" {
		t.Errorf("flaky_test should use linear backoff (deterministic timing for flake libs), got %q", got.Backoff)
	}
}

// TestClassify_IsFailed: smoke test for the convenience helper that
// callers use to filter attempts before invoking Classify.
func TestClassify_IsFailed(t *testing.T) {
	cases := []struct {
		name string
		a    workgraph.Attempt
		want bool
	}{
		{"failed_status", workgraph.Attempt{Status: "failed", ExitCode: 1}, true},
		{"timed_out_status", workgraph.Attempt{Status: "timed_out"}, true},
		{"succeeded_status", workgraph.Attempt{Status: "succeeded", ExitCode: 0}, false},
		{"cancelled_status", workgraph.Attempt{Status: "cancelled"}, false},
		{"empty", workgraph.Attempt{}, false},
		{"exit_code_only", workgraph.Attempt{Status: "", ExitCode: 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifier.IsFailed(tc.a); got != tc.want {
				t.Errorf("IsFailed(%+v): got %v, want %v", tc.a, got, tc.want)
			}
		})
	}
}

// TestPolicyFor_UnknownClass: the fallback policy must be sane. Any
// caller typo (e.g. "code_fail") must not crash and must return a
// non-retryable policy so misconfigured callers fail closed.
func TestPolicyFor_UnknownClass(t *testing.T) {
	pol := classifier.PolicyFor("not_a_real_class")
	known := classifier.PolicyFor(classifier.ClassUnknown)
	if pol.Class != known.Class {
		t.Errorf("PolicyFor(unknown) class: got %q, want %q", pol.Class, known.Class)
	}
	if pol.MaxRetries != known.MaxRetries {
		t.Errorf("PolicyFor(unknown) MaxRetries: got %d, want %d", pol.MaxRetries, known.MaxRetries)
	}
}

// TestAllPolicies_AllClassesCovered: every enum value in classifier.go
// must have an entry in the policy table. This is the gate that catches
// "added a new class but forgot to add its policy" bugs.
func TestAllPolicies_AllClassesCovered(t *testing.T) {
	want := []classifier.Class{
		classifier.ClassCodeFailure,
		classifier.ClassTestFailure,
		classifier.ClassFlakyTest,
		classifier.ClassRunnerFailure,
		classifier.ClassInfrastructureFail,
		classifier.ClassNetworkFailure,
		classifier.ClassDependencyFailure,
		classifier.ClassCapacityFailure,
		classifier.ClassPolicyFailure,
		classifier.ClassCredentialFailure,
		classifier.ClassUnknown,
	}
	policies := classifier.AllPolicies()
	for _, c := range want {
		if _, ok := policies[c]; !ok {
			t.Errorf("policy table missing entry for class %q", c)
		}
	}
	if len(policies) < len(want) {
		t.Errorf("policy table has %d entries, want >= %d", len(policies), len(want))
	}
}

// TestClassify_OrderMatters_ExitCodeBeatsLog: a network error in the log
// MUST NOT override exit-code-137 → runner_failure. Exit code wins
// because it is a direct signal from the OS and is far more specific.
func TestClassify_OrderMatters_ExitCodeBeatsLog(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "build", Run: "go build ./..."},
		workgraph.Attempt{Status: "failed", ExitCode: 137, Error: "killed"},
		"connection refused: unable to reach artifact server")
	assertClass(t, got, classifier.ClassRunnerFailure)
	if got.Rule != "exit_code_137_143" {
		t.Errorf("Rule: got %q, want exit_code_137_143 (exit code must win over log content)", got.Rule)
	}
}

// TestClassify_PolicyBeatsCredential: "policy denied" must win over a
// generic "unauthorized" string. This guards against the
// credential_error predicate accidentally swallowing policy failures,
// which would block human escalation on policy violations.
func TestClassify_PolicyBeatsCredential(t *testing.T) {
	got := classifyOK(t,
		workgraph.Node{ID: "deploy", Run: "kubectl apply -f x.yaml"},
		workgraph.Attempt{Status: "failed", ExitCode: 1, Error: "denied"},
		"policy denied: unauthorized namespace access")
	assertClass(t, got, classifier.ClassPolicyFailure)
	if got.Rule != "policy_error" {
		t.Errorf("Rule: got %q, want policy_error (policy must win over credential)", got.Rule)
	}
}

// TestClassify_RuleTraceability: every classification must carry a
// non-empty Rule. The Rule is the audit trail for "why was this node
// classified as X?" and is critical for post-incident review. We
// require a specific (non-default) rule for cases that match a
// specific heuristic; the "code" case legitimately falls through to
// the default rule, which is also acceptable as long as the Rule
// field is populated.
func TestClassify_RuleTraceability(t *testing.T) {
	cases := []struct {
		name        string
		node        workgraph.Node
		attempt     workgraph.Attempt
		log         string
		wantNonDefault bool
	}{
		{"runner", workgraph.Node{ID: "x", Run: "go build"}, workgraph.Attempt{Status: "failed", ExitCode: 137}, "killed", true},
		{"timeout", workgraph.Node{ID: "x", Run: "go test"}, workgraph.Attempt{Status: "failed", ExitCode: 124}, "timeout", true},
		{"network", workgraph.Node{ID: "x", Run: "curl"}, workgraph.Attempt{Status: "failed", ExitCode: 7}, "connection refused", true},
		{"code", workgraph.Node{ID: "x", Run: "go build"}, workgraph.Attempt{Status: "failed", ExitCode: 1}, "compile error: undefined: foo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := classifyOK(t, tc.node, tc.attempt, tc.log)
			if cls.Rule == "" {
				t.Errorf("Rule field is empty for case %q", tc.name)
			}
			if tc.wantNonDefault {
				if cls.Rule == "default_code_node" || cls.Rule == "no_match" || cls.Rule == "no_input" {
					t.Errorf("case %q fell through to fallback rule %q; classifier should match a specific signal", tc.name, cls.Rule)
				}
			}
		})
	}
}