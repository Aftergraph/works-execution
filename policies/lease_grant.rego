# policies/lease_grant.rego
#
# First OPA Rego policy bundle for works-execution.
# Implements standards #96 (OPA) and #97 (Rego) plus the Policy-Enforced
# Action Standard (#125) requirement that every action runs through a
# policy check before any state mutation.
#
# Scope: governs POST /v1/leases/grant. The API layer (services/api/policy.go)
# evaluates this bundle and rejects the request with HTTP 403 + a structured
# error code if `allow` is false.
#
# Input shape (services/api/policy.go -> DecisionInput):
# {
#   "request": {
#     "action": "lease.grant",          # string — the verb being evaluated
#     "work_id": "...",                # string
#     "node_id": "...",                # string
#     "worker_id": "...",              # string
#   },
#   "work": {
#     "id": "...",                     # string
#     "policy": {
#       "production_access": bool,     # boolean — privileged execution flag
#       "trust_class":     string      # string — work's minimum trust floor
#     }
#   },
#   "evidence": [                      # array — evidence records for this work
#     {
#       "id":     "...",
#       "type":   "build|test|...",
#       "result": "pass|fail|warn|skip",
#       "node_id":"...",
#       ...
#     },
#     ...
#   ],
#   "runner": {                        # runner identity record (k-impl-002)
#     "runner_id":   "...",
#     "trust_class": "untrusted|standard|privileged",
#     "lifecycle_state": "pending|active|draining|retired"
#   }
# }
#
# Output:
#   data.lease_grant.allow           -> bool
#   data.lease_grant.deny_reasons    -> array<string>   (empty when allow=true)
#   data.lease_grant.required_trust  -> string            (echoes the floor we enforced)
#
# -----------------------------------------------------------------------------
# 1. Package + metadata
# -----------------------------------------------------------------------------
package lease_grant

import future.keywords.in

# Policy bundle version. Bumped when rules change. Used by audit + tests.
policy_version := "v1.0.0"

# Trust-class ordering — lower index = lower privilege. Reused by both
# `trust_meets_floor` and the runner-trust rule below.
trust_order := ["untrusted", "standard", "privileged"]

# -----------------------------------------------------------------------------
# 2. Default decisions
# -----------------------------------------------------------------------------

# Default: deny. Every rule below must explicitly grant.
defaultallow := false

# `deny_reasons` starts empty. Each rule that blocks appends a structured
# reason so the API can return machine-readable error codes to the caller
# (mapped to HTTP 403 + a stable `error` code).
defaultdeny_reasons := []

# Echoes the trust floor the work demanded. Always populated, even on allow,
# so audit log consumers can confirm which tier was checked.
required_trust := input.work.policy.trust_class

# -----------------------------------------------------------------------------
# 3. Helpers
# -----------------------------------------------------------------------------

# rank_of(class) returns the 0-based index in `trust_order`. Unknown classes
# get rank -1 so they fall below the floor.
rank_of(class) := i if {
    some i, c in trust_order
    c == class
}

# Negative-case rank for unknown values.
rank_of(_) := -1

# trust_meets_floor(runner_class, floor_class) is true iff the runner's trust
# is at or above the floor demanded by the work.
trust_meets_floor(runner_class, floor_class) if {
    rank_of(runner_class) >= rank_of(floor_class)
}

# any_approved_evidence returns true if at least one evidence record in
# `input.evidence` has result == "pass". An empty evidence array returns
# false. We iterate over a copy so Rego's immutability guarantees hold.
any_approved_evidence if {
    count([e | some e in input.evidence; e.result == "pass"]) > 0
}

# runner_is_active is true iff the runner's lifecycle_state is "active".
# Retired, draining, or pending runners cannot lease work — that gating
# belongs to the auth layer (k-impl-003), but we double-check here as a
# defense-in-depth measure.
runner_is_active if {
    input.runner.lifecycle_state == "active"
}

# -----------------------------------------------------------------------------
# 4. Allow rules (positive)
# -----------------------------------------------------------------------------

# Rule 4a — non-production work.
# If the work does NOT require production access, it is unprivileged and the
# evidence + trust floor rules do not apply. We allow without further checks.
allow if {
    input.work.policy.production_access == false
}

# Rule 4b — production_access work with approved evidence AND adequate
# runner trust. This is the core rule that the slice-4 spec calls for.
allow if {
    input.work.policy.production_access == true
    any_approved_evidence
    trust_meets_floor(input.runner.trust_class, input.work.policy.trust_class)
    runner_is_active
}

# -----------------------------------------------------------------------------
# 5. Deny-reason rules (negative). Each appends a stable error code so the
#    API layer can return a meaningful message + machine-readable code.
# -----------------------------------------------------------------------------

deny_reasons contains reason if {
    input.work.policy.production_access == true
    not any_approved_evidence
    reason := "missing_approved_evidence"
}

deny_reasons contains reason if {
    input.work.policy.production_access == true
    any_approved_evidence
    not trust_meets_floor(input.runner.trust_class, input.work.policy.trust_class)
    reason := "runner_trust_below_floor"
}

deny_reasons contains reason if {
    input.work.policy.production_access == true
    any_approved_evidence
    trust_meets_floor(input.runner.trust_class, input.work.policy.trust_class)
    not runner_is_active
    reason := "runner_not_active"
}

# Catch-all deny reason when production_access is required but no
# production-grant rule fired. Useful for unknown-class inputs.
deny_reasons contains reason if {
    input.work.policy.production_access == true
    count(deny_reasons) == 0
    reason := "production_access_denied"
}