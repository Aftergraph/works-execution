// Per-class retry policy table for the Self-Healing Failure Classifier
// (k-impl-007, platform-self-healing standard #117).
//
// Each Class maps to a RetryPolicy describing how the Self-Healing
// scheduler should react when a node attempt is classified into that
// class. The fields mirror failure-classification.schema.json exactly:
//   - Retryable:        may the scheduler requeue the node automatically?
//   - MaxRetries:       upper bound on automatic retries (0..5 per schema).
//   - Backoff:          "none" | "linear" | "exponential".
//   - HumanRequired:    must a human operator approve before requeue?
//   - AutonomousRemediation: allowed remediation actions per the
//     Autonomous Remediation Standard #129.
//
// Adding a new class: add an entry to policyTable below AND add the class
// to the Class enum in classifier.go. Both must stay in sync with
// failure-classification.schema.json.

package classifier

// RetryPolicy is the policy attached to a classification. See
// Classification in classifier.go — the two structs share the same
// wire shape; the only difference is that RetryPolicy is keyed by Class
// and has no Rule field.
type RetryPolicy struct {
	Class                Class
	Retryable            bool
	MaxRetries           int
	Backoff              string
	HumanRequired        bool
	AutonomousRemediation []string
}

// policyTable is the canonical per-class retry policy. Values are
// intentionally conservative: prefer escalation to spurious retry storms.
//
// Notes per class:
//
//   - code_failure: usually a real bug; retrying without code change is
//     pointless. We allow one retry with exponential backoff so transient
//     cache-state issues self-heal; anything more is human work.
//
//   - test_failure: real test assertion failure. One retry catches
//     order-dependent setup issues; two retries is the cap to bound
//     flakiness masquerading as failure.
//
//   - flaky_test: known flake patterns. Retries are the entire point —
//     allow up to 3, linear backoff (deterministic timing helps flake
//     libraries reproduce).
//
//   - runner_failure: the runner itself died (OOM, SIGKILL). Almost
//     always retryable on a different runner; 3 attempts with
//     exponential backoff.
//
//   - infrastructure_failure: explicit timeouts, runner wrapper
//     timeouts. Same shape as runner_failure.
//
//   - network_failure: highly transient. Up to 4 retries with
//     exponential backoff; rarely needs human.
//
//   - dependency_failure: usually a missing or wrong-version package.
//     One retry to allow cache refresh, then escalate.
//
//   - capacity_failure: out of disk/memory on the runner. Retryable
//     (the next runner may have more resources), but cap at 2.
//
//   - policy_failure: NEVER retry autonomously. Retrying without a
//     policy change risks policy circumvention; human_required is true.
//
//   - credential_failure: NEVER retry autonomously. Rotating credentials
//     is out of scope for Self-Healing; human_required is true.
//
//   - unknown: terminal. No autonomous retry; one linear-backoff attempt
//     is allowed to surface a more-specific failure on the second try.
var policyTable = map[Class]RetryPolicy{
	ClassCodeFailure: {
		Class:         ClassCodeFailure,
		Retryable:     true,
		MaxRetries:    1,
		Backoff:       "exponential",
		HumanRequired: false,
		AutonomousRemediation: []string{
			"rebuild_cache",
		},
	},
	ClassTestFailure: {
		Class:         ClassTestFailure,
		Retryable:     true,
		MaxRetries:    2,
		Backoff:       "exponential",
		HumanRequired: false,
		AutonomousRemediation: []string{
			"rebuild_cache",
		},
	},
	ClassFlakyTest: {
		Class:         ClassFlakyTest,
		Retryable:     true,
		MaxRetries:    3,
		Backoff:       "linear",
		HumanRequired: false,
		AutonomousRemediation: []string{
			"rerun_node",
			"record_flake",
		},
	},
	ClassRunnerFailure: {
		Class:         ClassRunnerFailure,
		Retryable:     true,
		MaxRetries:    3,
		Backoff:       "exponential",
		HumanRequired: false,
		AutonomousRemediation: []string{
			"reassign_runner",
		},
	},
	ClassInfrastructureFail: {
		Class:         ClassInfrastructureFail,
		Retryable:     true,
		MaxRetries:    3,
		Backoff:       "exponential",
		HumanRequired: false,
		AutonomousRemediation: []string{
			"reassign_runner",
		},
	},
	ClassNetworkFailure: {
		Class:         ClassNetworkFailure,
		Retryable:     true,
		MaxRetries:    4,
		Backoff:       "exponential",
		HumanRequired: false,
		AutonomousRemediation: []string{
			"rebuild_cache",
			"reassign_runner",
		},
	},
	ClassDependencyFailure: {
		Class:         ClassDependencyFailure,
		Retryable:     true,
		MaxRetries:    1,
		Backoff:       "exponential",
		HumanRequired: false,
		AutonomousRemediation: []string{
			"refresh_cache",
		},
	},
	ClassCapacityFailure: {
		Class:         ClassCapacityFailure,
		Retryable:     true,
		MaxRetries:    2,
		Backoff:       "exponential",
		HumanRequired: false,
		AutonomousRemediation: []string{
			"reassign_runner",
		},
	},
	ClassPolicyFailure: {
		Class:         ClassPolicyFailure,
		Retryable:     false,
		MaxRetries:    0,
		Backoff:       "none",
		HumanRequired: true,
		AutonomousRemediation: []string{
			// Intentionally empty: autonomous policy changes are not
			// permitted under any circumstance. The scheduler must
			// escalate to a human operator.
		},
	},
	ClassCredentialFailure: {
		Class:         ClassCredentialFailure,
		Retryable:     false,
		MaxRetries:    0,
		Backoff:       "none",
		HumanRequired: true,
		AutonomousRemediation: []string{
			// Intentionally empty: credentials cannot be rotated or
			// minted by the Self-Healing loop.
		},
	},
	ClassUnknown: {
		Class:         ClassUnknown,
		Retryable:     true,
		MaxRetries:    1,
		Backoff:       "linear",
		HumanRequired: false,
		AutonomousRemediation: []string{},
	},
}

// PolicyFor returns the RetryPolicy for the given class. Unknown classes
// (those not in the policyTable) fall back to the ClassUnknown policy.
// The returned value is a deep copy of the table entry so callers can
// safely mutate AutonomousRemediation without poisoning the global.
func PolicyFor(c Class) RetryPolicy {
	p, ok := policyTable[c]
	if !ok {
		p = policyTable[ClassUnknown]
	}
	// Defensive copy of the slice.
	p.AutonomousRemediation = append([]string(nil), p.AutonomousRemediation...)
	return p
}

// AllPolicies returns a snapshot of the policy table, keyed by class.
// Used by tests and by the standards-validate command to confirm that
// every class listed in failure-classification.schema.json has a
// matching policy entry. Order is not guaranteed.
func AllPolicies() map[Class]RetryPolicy {
	out := make(map[Class]RetryPolicy, len(policyTable))
	for k, v := range policyTable {
		v.AutonomousRemediation = append([]string(nil), v.AutonomousRemediation...)
		out[k] = v
	}
	return out
}