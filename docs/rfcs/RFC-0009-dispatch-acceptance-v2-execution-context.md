# RFC-0009: Dispatch Acceptance 2.0 + Materialized Execution Context

Status: **PROPOSED / NOT FROZEN**  
Owner: WORKS kernel, with cross-repo governance registration required  
Tracks: Aftergraph/after-graph-governance#185, Aftergraph/works-execution#125, Aftergraph/STEWARD-by-Aftergraph#2

## Problem

The existing `dispatch.acceptance/1.0` is useful compatibility infrastructure, but it cannot by itself carry the approved Platform Convergence V2.1 execution chain.

Two mismatches are load-bearing:

1. `authority_epoch` has no canonical AuthorityLease owner in V2.1. Production WORKS therefore correctly leaves `CurrentEpoch` unwired and fails the 1.0 HTTP accept surface closed.
2. 1.0 mints a correlation-shaped `execution_context_id`, but it does not materialize the canonical WORKS-owned `execution-context/1.0` record consumed by Trust Gateway V2.1.

A P2 implementation must not solve either mismatch by inventing authority truth or by treating a correlation-shaped identifier as a durable execution context.

## Decision proposed

Introduce a new major `dispatch.acceptance/2.0` path.

The request binds the canonical inputs already established before durable execution:

```text
organization
tenant
principal
mission
AuthorityLease
WorkerLease
initial admission PDR
Runtime dispatch
Attempt
Effect
idempotency / causal identity
budget
checkpoint
evidence root
exact verification subject
```

WORKS then validates the Work + WorkerLease relationship and atomically persists:

```text
durable acceptance
+
execution-context/1.0
+
WORKS-minted ctx_*
+
WORKS-minted trc_*
```

The acceptance response returns the same canonical context identity that
`GET /v1/execution-contexts/{id}` resolves.

## Authority boundary

2.0 deliberately removes the unowned scalar `authority_epoch` from the new path.

That does **not** make dispatch acceptance an authority decision.

Initial legitimacy is represented by the referenced AuthorityLease and admission PDR. Every consequential action still requires Trust Gateway to fetch the immutable execution context and perform live AIE revalidation immediately before effect, recording an action-time PDR.

```text
dispatch accepted != effect authorized
execution-context != authorization token
WorkerLease != AuthorityLease
```

WORKS never becomes the authority owner.

## Wire proposal

The candidate schema is:

`contracts/proposals/dispatch.acceptance.v2.schema.json`

It discriminates request and acceptance records with `kind`.

Request records cannot carry:
- `work_id` (route scope);
- `works_execution_id`;
- `execution_context_id`;
- `trace_id`;
- outcome or verification fields.

Because the request branch is strict (`additionalProperties:false`), a client attempt to preselect WORKS-owned correlation is rejected.

Acceptance records must carry the canonical Work ID plus materialized context/trace identity.

## Compatibility

- `dispatch.acceptance/1.0` remains readable and unchanged.
- 2.0 is a major path; no in-place semantic rewrite of 1.0.
- No migration/backfill is required for historical acceptances.
- Consumers must explicitly opt into 2.0.
- The proposal is not added to the frozen manifest until governance disposition approves the contract.

## Recovery

A committed idempotency winner remains recoverable after a later authority-freshness change. Recovery reads the already committed decision; it is not a new authorization.

Fresh, never-before-accepted dispatches still pass the current authority/admission prerequisites of the 2.0 composition before effect execution.

## Required implementation proof

Before the proposal can become normative:

1. WORKS validates WorkerLease belongs to the route-scoped Work.
2. acceptance + execution context are one durable transaction.
3. duplicate acceptance returns the same `works_execution_id`, `ctx_*`, and `trc_*`.
4. failure between context creation and acceptance cannot leave an orphan half-state.
5. request-supplied context/trace is rejected.
6. Trust Gateway resolves the returned context and revalidates the exact AuthorityLease at consequence time.
7. revoked authority after acceptance blocks the effect.
8. exact verification subject remains immutable through Sentinel verification.
9. `SUCCEEDED != VERIFIED`.
10. Golden Mission proves the composed six-repo path.

## Claim boundary

This RFC and schema are executable contract candidates only. They do not establish a live P2 execution path or platform PASS.
