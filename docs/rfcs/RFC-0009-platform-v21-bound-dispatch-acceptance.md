# RFC-0009: Platform V2.1 Runtime → WORKS Bound Dispatch Acceptance

Status: **PROPOSED — STEWARD P2 falsification candidate**  
Owner: works-kernel  
Governance review: Aftergraph/after-graph-governance#185  
Supersedes for new Platform V2.1 writes: RFC-0008 / dispatch.acceptance/1.0  
Compatibility: 1.0 remains readable and its route remains unchanged.

## 1. Why a new major contract is required

The Platform V2.1 identity design made two facts explicit after
`dispatch.acceptance/1.0` was frozen:

1. legitimate authority is represented by AIE `AuthorityLease` plus Trust
   Gateway policy/admission and **live action-time AIE revalidation**; there is
   no canonical platform-owned scalar `authority_epoch`;
2. `execution-context/1.0` is a real immutable WORKS object containing the
   canonical organization/tenant/principal/mission/AuthorityLease/Work/
   WorkerLease/admission-PDR binding. Trust Gateway loads that object before
   every V2.1 consequential action.

RFC-0008 cannot express those semantics safely:

- its `authority_epoch` freshness gate has no canonical V2.1 resolver;
- its accept-time `ctx_*` identifier is correlation stored inside the
  acceptance record, not a row in `work_execution_contexts`, so Trust Gateway
  cannot resolve it through `GET /v1/execution-contexts/{id}`.

Silently mapping AIE revocation-watermark sequence to `authority_epoch`, using
a constant, or accepting the client's own epoch would manufacture authority
truth. This RFC does none of those.

## 2. Decision

Platform V2.1 creates the canonical execution context **before** Runtime dispatch
acceptance.

```text
AIE AuthorityLease
  ↓
TG initial admission + PDR
  ↓
WORKS Work + WorkerLease
  ↓
WORKS materializes execution-context/1.0
  ↓
Runtime builds dispatch
  ↓
POST /v2/works/{work}/accept
  execution_context_id + dispatch/effect/idempotency/subject bindings
  ↓
WORKS proves that ctx exists, belongs to the route Work,
and still names the same active WorkerLease/Attempt
  ↓
durable dispatch acceptance bound to that exact ctx + trace
  ↓
Runtime/Habitat prepares proposed effect
  ↓
TG V2.1 reloads the SAME ctx
  ↓
AIE live revalidate(action_id)
  ↓
TG execution-phase PDR + WORKS evidence correlation
  ↓
effect
```

Acceptance is **durable execution correlation**, not permission to perform the
effect.

## 3. Request

Route: `POST /v2/works/{work_id}/accept`

The request contains no organization, tenant, principal, mission,
AuthorityLease, WorkerLease or trace values that Runtime could restate
incorrectly. Those come from the immutable referenced context.

Required request fields:

- `execution_context_id` — an already-existing WORKS ctx, never client minted;
- `runtime_dispatch_id`;
- `attempt_id`;
- `effect_id`;
- `idempotency_key`;
- `budget_ref`;
- `budget_ceiling >= 0`;
- `checkpoint_id`;
- `evidence_root`;
- `verification_subject`;
- `causal_id`.

No `authority_epoch` exists in 2.0.

Proposed machine schema:
`contracts/proposed/dispatch.acceptance.2.0.schema.json`.

The schema is intentionally outside the frozen manifest until governance #185
approves the major-version contract.

## 4. Server validation

Before a fresh acceptance, WORKS MUST prove:

1. the execution context exists;
2. `context.work_id == {work_id}`;
3. the referenced WorkerLease exists;
4. WorkerLease belongs to the same Work and worker recorded in the context;
5. `WorkerLease.attempt_id == request.attempt_id`;
6. WorkerLease is ACTIVE and not expired;
7. the context contains a valid trace ID;
8. the dispatch contract minimums are satisfied.

WORKS does **not** revalidate the AuthorityLease here. Context admission is
historical correlation. The effect boundary remains Trust Gateway → AIE.

## 5. Durable semantics

The existing `dispatch_acceptances` durability table remains the single
acceptance truth.

2.0 uses the existing atomic idempotency insert but binds the winner to the
pre-existing context and trace rather than minting a second fake context ID.

Same idempotency key + byte-equivalent immutable bindings returns the same
winner.

Same key + changed context, trace, Runtime dispatch, Attempt, Effect, budget,
checkpoint, evidence root, verification subject or causal ID fails closed.

A lost HTTP response may be replayed and read back after external authority
state changes, because recovery of an already-committed winner is not a new
authorization decision.

## 6. Authentication

The V2 route is platform-to-platform, not a worker mutation.

It requires the same dual service boundary as execution-context creation and
execution-PDR correlation:

- WORKS platform Bearer token; and
- `X-Works-Platform-Bridge`.

Worker enrollment tokens are not a substitute.

## 7. Response

The response returns the durable acceptance plus the canonical stored context
bindings:

- `works_execution_id`;
- `work_id`;
- `execution_context_id`;
- `trace_id`;
- `organization_id`, `tenant_id`, `principal_id`;
- `mission_id`;
- `authority_lease_id`;
- `worker_id`, `worker_lease_id`;
- `admission_decision_id`;
- dispatch/effect/idempotency/budget/checkpoint/evidence/verification bindings;
- outcome / independent verification fields.

Returning these fields is readback/projection; it does not upgrade them to
authority.

## 8. Compatibility

`POST /v1/works/{id}/accept` and `contract:dispatch.acceptance/1.0` remain
unchanged for N-1/history.

New Platform V2.1 consumers MUST use 2.0 once governance approves it.

No code may silently translate 2.0 to 1.0 by inventing `authority_epoch`.

## 9. Acceptance tests

The implementation candidate must prove:

- unknown ctx fails closed;
- ctx scoped to another Work fails without cross-Work enumeration;
- client cannot supply identity/authority/trace fields outside the ctx ref;
- inactive/expired/wrong WorkerLease fails;
- wrong Attempt fails;
- exact ctx + active WorkerLease accepts;
- replay returns same Work execution/context/trace;
- changed verification subject or effect under same idempotency key fails;
- response validates against the proposed 2.0 schema;
- acceptance never marks `verified=true` by itself;
- TG can later resolve the returned ctx through the canonical read endpoint.

## 10. Non-goals

- no AuthorityLease semantics in WORKS;
- no effect authorization;
- no TG replacement;
- no AIE replacement;
- no verification authority;
- no deletion or semantic rewrite of 1.0.
