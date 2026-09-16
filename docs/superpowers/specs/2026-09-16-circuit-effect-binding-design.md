# Circuit Effect Binding Design

**Status:** Implemented as draft T040b stacked on CircuitVerdict PR #96; no deployment.

## Purpose

Create an explicit WORKS-owned seam from a persisted CircuitRun to an already-durable dispatch acceptance/effect identity without changing `dispatch.acceptance/1.0`.

The relation is not inferred from `attempt_id`, causal IDs, or naming. Those existing fields do not contractually identify a Work.

This slice creates a durable explicit binding seam; it does not independently discover causality. Full causal execution proof requires the binding operation to be invoked by the canonical dispatch-accept flow in a later integration slice. Same MissionID alone is necessary but not sufficient evidence of causality.

## Input

Caller supplies only `circuit_run_id` and `works_execution_id`.

WORKS loads both durable records and derives WorkID, MissionID, RuntimeDispatchID, dispatch AttemptID, EffectID, VerificationSubject, CausalID, and a SHA-256 digest of the frozen Dispatch identity.
## Invariants

- CircuitRun and dispatch acceptance must already exist.
- Dispatch MissionID must equal CircuitRun MissionID.
- EffectID, VerificationSubject, RuntimeDispatchID, dispatch AttemptID, and CausalID must be present.
- One WorksExecutionID can belong to only one CircuitRun.
- One EffectID may appear at most once within one CircuitRun.
- The same effect identity cannot be rebound within one CircuitRun.
- Exact retries are idempotent; conflicting bindings fail closed.
- Read paths re-derive the dispatch digest and reject identity drift.
- Multiple distinct effect bindings per CircuitRun are allowed.
- Binding an effect identity does **not** claim `EffectApplied`, success, verification, authority, or settlement.

## Persistence

Schema v16 adds `circuit_effect_bindings`. `dispatch_attempt_id` is deliberately named as dispatch provenance and MUST NOT be interpreted as `work_attempts.id` without a future explicit contract.
