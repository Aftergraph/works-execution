# RFC-0008: Runtime → WORKS Dispatch Acceptance Seal

Status: PROPOSED · Owner: works-kernel · Tracks: Golden Mission Phase 3,
Aftergraph/runtime#104, Aftergraph/after-graph-governance#68.

## Problem

"Runtime sent" is not "WORKS owns". Without a durable acceptance seam,
a Runtime crash, duplicate dispatch, stale authority epoch, or mid-flight
revocation can produce double execution, widened authority, or an execution
verdict that was never independently verified.

## Contract: `dispatch.acceptance/1.0`

Runtime dispatches an envelope; WORKS accepts durably and returns a
`works_execution_id`. One integration contract, versioned in
`contracts/schemas/dispatch.acceptance.schema.json`:

- `mission_id`, `authority_ref`, `authority_epoch`
- `runtime_dispatch_id`, `works_execution_id`
- `attempt_id`, `effect_id`, `idempotency_key`
- `budget_ref`, `budget_ceiling`
- `checkpoint_id`, `evidence_root`
- `verification_subject`
- outcome ∈ `ACCEPTED | SUCCEEDED | FAILED | INDETERMINATE`
- `verified` (independent verdict only), `verifier_id`

## Semantics (implemented in `internal/dispatch`)

1. **Accept is idempotent on `(idempotency_key, causal_id)`.**
   Same key + same causal identity returns the existing record
   (covers: Runtime dies after accept, duplicate dispatch, replay after
   completion). Same key + different causal identity fails closed.
2. **Authority freshness at accept.** Dispatch epoch < current epoch is
   rejected; restart never resets epoch, spend, revocation, or verdicts.
3. **Effects apply exactly once.** Unknown effect outcome resolves
   `INDETERMINATE`, never silent success.
4. **Budget ceiling is hard.** Over-ceiling spend fails closed; only a new
   dispatched budget reference (new idempotency key) can continue —
   no autonomous retry around the ceiling.
5. **Revocation wins mid-flight.** Revoked executions cannot apply effects,
   complete, spend, or verify afterwards.
6. **SUCCEEDED ≠ VERIFIED.** Completion records execution outcome only.
   Verification requires an independent `verifier_id` (never the dispatching
   Runtime), over the exact accepted subject, with the verifier available.
   Stale subject and unavailable verifier fail closed to UNVERIFIED.

## Adversarial coverage

`internal/dispatch/acceptance_test.go` proves all 12 required cases:
1. Runtime dies before accept → fresh accept, no phantom record.
2. Runtime dies after accept → redispatch returns the same record.
3. Worker dies before effect → effect not applied, safe to continue.
4. Worker dies after effect, before ack → duplicate apply rejected.
5. Duplicate dispatch → same record, single execution.
6. Stale authority → rejected at accept.
7. Mid-flight revocation → downstream protected work blocked.
8. Budget exhaustion → hard ceiling, no autonomous retry.
9. Verifier unavailable → stays UNVERIFIED.
10. Stale verification subject → fails closed.
11. Replay after completion → same record, no re-execution.
12. Mismatched causal identity → fails closed.

## Non-goals

- No authority semantics (AIE owns), no admission (TG owns), no outcome
  correctness verdicts (independent verifiers own). WORKS owns durability
  and exactly-once effect identity.
- The `Store` seam remains pluggable. The WORKS SQLite adapter persists acceptance,
  effect, budget and verification state across restart; memory-backed stores remain
  test fixtures only.
