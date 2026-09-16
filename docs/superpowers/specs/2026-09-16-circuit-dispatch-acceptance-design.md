# CDA-0.1 — Circuit Dispatch Acceptance Algorithm

**Status:** Draft implementation stacked on WORKS PR #97. No merge or deployment.

## Purpose

CDA-0.1 is the deterministic WORKS transition from a persisted CircuitRun plus a Runtime Dispatch to one durable WorksExecution identity and one exact CircuitEffectBinding.

It preserves the frozen `dispatch.acceptance/1.0` contract and the legacy non-Circuit acceptance path.

## Algorithm

1. Begin one SQLite transaction.
2. Run the existing `dispatch.Acceptor` against a transaction-backed dispatch Store.
3. Fail closed on stale authority, idempotency/causal mismatch, or malformed dispatch identity.
4. Load the exact CircuitRun inside the same transaction.
5. Require `Dispatch.MissionID == CircuitRun.MissionID`.
6. Derive the frozen Dispatch SHA-256 and CircuitEffectBinding from durable identities.
7. Enforce one CircuitRun per WorksExecution and one identical EffectID per CircuitRun.
8. Commit acceptance and binding together; any error rolls both back.
## Commit law

`DispatchAcceptance AND CircuitEffectBinding` commit together, or neither exists.

Exact replay returns the same WorksExecution and binding. A conflicting replay, Mission mismatch, stale authority, missing CircuitRun, or binding uniqueness violation rolls back the transaction.

## Non-goals

- CDA does not grant authority or admission.
- CDA does not execute the effect or claim `EffectApplied`.
- CDA does not mark Work or Circuit as verified.
- CDA does not replace Witness/CircuitVerdict.
- CDA does not change `dispatch.acceptance/1.0`.
- No Runtime/API integration or production deployment is part of T040c.

## Epistemic boundary

This slice proves atomic durable causality **inside WORKS once CDA is invoked**. End-to-end causal proof from Runtime requires the later canonical Runtime→WORKS integration to call CDA rather than the legacy acceptance path for Circuit executions.