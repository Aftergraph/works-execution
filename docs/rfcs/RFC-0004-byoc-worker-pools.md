# RFC-0004: BYOC Worker Pools

Status: IMPLEMENTED (2026-08-31)
Scope: Days 61-90 "Prove customer value" — "Add BYOC worker pools"

## Problem

Design partners run works-execution against their own repositories, but
before this RFC **any** worker could lease **any** work: the scheduler's
runner pool was empty in practice (workers never registered as runners),
the ready-handler fell back to "no scheduler" behavior, and the lease
endpoint had no runner-identity check. A partner's CI work could land on
shared compute; two partners' workers could race each other's jobs.

## Design

### 1. Worker self-registration + heartbeat

`internal/worker.Worker` gains `RunnerIdentity *RunnerSpec`
(TrustClass + Labels). When set, the worker:

1. POSTs `/v1/runners/register` at startup (idempotent upsert keyed on
   `runner_id`; server stamps `LastHeartbeatAt`).
2. Re-POSTs the same registration every `HeartbeatEvery` — the upsert
   doubles as the heartbeat.
3. Keeps polling regardless of registration failures (a broken
   registry degrades scheduling; it must not stop execution).

`cmd/works-worker` wires this from new flags:

    works-worker -pool avc-core -trust standard ...
    (env: WORKS_POOL, WORKS_TRUST_CLASS)

Pool membership is carried as the machine-assigned label
`pool:<name>` in runner capabilities.

### 2. Pool constraint in the work schema

`workgraph.Requirements.Pool string` — when set, only runners labeled
`pool:<name>` are eligible. Empty = no constraint (back-compat).

### 3. Scheduler hard filter (advisory)

`internal/scheduler.hardFilter` rejects with `pool_mismatch` when the
work names a pool the runner's labels don't carry. This shapes the
`/ready` response (pool-scoped nodes are only offered to in-pool
runners, with an explainable `unschedulable` record otherwise) — but it
is **not** the security boundary.

### 4. Lease-grant enforcement (the boundary)

`services/api.grantLease` loads the work and, when
`Requirements.Pool != ""`, verifies the leasing worker is registered
with the matching `pool:<name>` label. Non-members get **403
pool_mismatch** and zero state change. A worker that bypasses `/ready`
or races it cannot cross the pool boundary — the filter is enforced at
the mutation point, not the suggestion point.

### 5. Stale-runner exclusion

`readyNodesHandler` drops runners whose `LastHeartbeatAt` is older than
3× the 10s default heartbeat interval. Dead runners stop receiving
work; registrations without a heartbeat timestamp (pre-BYOC) are kept
for compatibility.

### 6. Visibility

- `GET /v1/runners` — list identities; `?pool=<name>` filters by pool,
  `?alive=true` drops stale heartbeats.
- `works runners [--pool NAME] [--alive]` — operator table with trust,
  lifecycle, pool, last-heartbeat age, os/arch.

## Security notes

- The pool label is machine-assigned at `/v1/runners/register`, which
  sits behind the same Bearer auth as the rest of the worker surface.
  A worker can only join the pool its operator provisions it into.
- Pool enforcement is checked on **every** lease grant, not cached.
- Unscoped works (no Pool) intentionally remain schedulable by any
  active runner — the shared pool is opt-out, matching the pack's
  "workers-as-disposable, control-plane-owns-state" model.

## Verification (production VDS, 2026-08-31)

- `wrkr_prod_1` registered into pool `avc-core`, heartbeat visible in
  `works runners`.
- Work with `requirements.pool=avc-core`: QUEUED → SUCCEEDED in ~1s.
- Work with `requirements.pool=foreign-pool`: stays QUEUED, **zero
  attempts** after 8s+; journal shows 9× `pool denied` on grant
  attempts; scheduler records `unschedulable [pool_mismatch:1]`.
- Own-pool + unscoped works unaffected; 26/26 test packages green;
  e2e, kanban-validate, standards-validate green; vet clean.
