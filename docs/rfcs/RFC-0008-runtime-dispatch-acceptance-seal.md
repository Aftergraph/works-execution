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
- `execution_context_id`, `trace_id` (optional; WORKS-minted at accept, execution-context/1.0 correlation — clients cannot supply them)
- outcome ∈ `ACCEPTED | SUCCEEDED | FAILED | INDETERMINATE`
- `verified` (independent verdict only), `verifier_id`
- `verdict.result`, `verdict.subject`, `verdict.evidence_ref`, `verdict.recorded_at`

## Semantics (implemented in `internal/dispatch`)

1. **Accept is an atomic idempotent insert on `(idempotency_key, causal_id)`.**
   The persistence adapter performs the insert under the database uniqueness
   constraint and returns the committed winner. A load-then-save sequence is
   forbidden because two Runtime retries can otherwise both observe absence.
   Same key + same causal identity returns the existing record; same key +
   different causal identity fails closed.
2. **Authority freshness at accept.** Dispatch epoch < current epoch is
   rejected; restart never resets epoch, spend, revocation, or verdicts.
3. **Effects apply exactly once.** Unknown effect outcome resolves
   `INDETERMINATE`, never silent success.
4. **Budget ceiling is hard.** Charges must be strictly positive, persisted
   budget state must be valid, and overflow-safe over-ceiling spend fails
   closed. Only a new dispatched budget reference (new idempotency key) can
   continue — no autonomous retry around the ceiling.
5. **Revocation wins mid-flight.** Revoked executions cannot apply effects,
   complete, spend, or verify afterwards.
6. **SUCCEEDED ≠ VERIFIED.** Completion records execution outcome only.
   Verification requires a terminal `SUCCEEDED` or `FAILED` outcome, an
   independent `verifier_id` (never the dispatching Runtime), the exact
   accepted subject, a normalized `ACCEPT`/`REJECT` result, and a non-empty
   evidence reference recorded with timestamp. Pre-terminal, stale-subject,
   unavailable-verifier, or missing-evidence calls fail closed.
7. **Correlation minting at accept.** WORKS mints `execution_context_id` and
   `trace_id` (execution-context/1.0) when a fresh dispatch wins the
   `AcceptIfAbsent` insert. Duplicate or retried dispatches of the same
   idempotency key return the winner's IDs unchanged; a replay never remints.
   These fields are optional on the wire for backward compatibility.

## Adversarial coverage

`internal/dispatch/acceptance_test.go` proves all 15 required cases:
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
13. Concurrent duplicate accepts → one durable execution identity.
14. Non-positive and overflowing spend → rejected without budget mutation.
15. Pre-terminal or evidence-less verdict → rejected without verification.

## Non-goals

- No authority semantics (AIE owns), no admission (TG owns), no outcome
  correctness verdicts (independent verifiers own). WORKS owns durability
  and exactly-once effect identity.
- The `Store` seam remains pluggable, but every production adapter MUST provide
  an atomic `AcceptIfAbsent` implementation. The WORKS SQLite adapter persists
  acceptance, effect, budget and verification state across restart; memory-backed
  stores remain test fixtures only.
