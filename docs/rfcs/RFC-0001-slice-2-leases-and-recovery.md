# RFC-0001: Slice 2 — Leases, Worker-Loss Recovery, Log Streaming

**Status:** Accepted
**Author:** Hermes Agent (atlas)
**Date:** 2026-08-31
**Implements:** `00_START_HERE/90_DAY_EXECUTION_PLAN.md` Days 31–60 (subset)

## Summary

Slice 1 used **implicit claiming**: workers "owned" a node by virtue of having
written a `running` attempt. There was no protocol for handoff, no detection of
lost workers, no way to safely cancel mid-flight execution. This slice
introduces a proper **Lease** entity with explicit grant/heartbeat/release
semantics, a periodic reaper that detects and recovers from worker loss, and
log streaming so a worker's progress is observable from the API.

Out of scope for this slice (deferred to slice 3): Docker sandbox, GitHub
webhook integration, artifact store beyond local filesystem.

## Motivation

1. The pack's SLO `05_OPERATIONS/SLOS_AND_SRE.md` requires lost-worker
   detection **<30s**. Slice 1 has no detection at all — a `kill -9` of a
   worker leaves the attempt row in `running` forever.
2. The chaos test "kill worker mid-node" (`03_ENGINEERING/TEST_STRATEGY.md`)
   is impossible to satisfy without leases.
3. The worker protocol draft `03_ENGINEERING/WORKER_PROTOCOL_DRAFT.md`
   specifies `LEASE_OFFER / LEASE_ACCEPT / LEASE_REJECT / HEARTBEAT`
   messages — slice 1 has none of these.
4. Without lease-based revocation, `POST /v1/works/{id}/cancel` is a lie: the
   worker keeps running the subprocess to completion regardless.

## Goals

- **Lease entity** with explicit grant/heartbeat/release/expire states.
- **Worker-loss detection ≤30s** at p99, even under single-worker load.
- **Subprocess cancellation** when a lease is revoked or the work is cancelled.
- **Log streaming** so `GET /v1/works/{id}/nodes/{n}/logs` returns what the
  worker has produced so far, without waiting for terminal state.
- **All slice-1 tests still pass.** No regression on existing behavior.
- **Chaos test proves the recovery path** end-to-end.

## Non-goals

- Docker / gVisor sandboxing (slice 3).
- Capability-aware scheduler scoring (slice 3; slice 2 uses simple "any worker"
  for V1, matching slice 1).
- Multi-tenant worker isolation (slice 3).
- Lease-based optimistic concurrency on the store (we keep the SQLite `BEGIN
  IMMEDIATE` model from slice 1).

## Design

### Lease entity

```go
type LeaseStatus string
const (
    LeaseActive   LeaseStatus = "ACTIVE"
    LeaseRenewed  LeaseStatus = "RENEWED"   // terminal — used by reaper after extension
    LeaseExpired  LeaseStatus = "EXPIRED"   // terminal — reaper detected timeout
    LeaseRevoked  LeaseStatus = "REVOKED"   // terminal — explicit cancellation
    LeaseReleased LeaseStatus = "RELEASED"  // terminal — worker voluntarily gave it back
)

type Lease struct {
    ID         string       `json:"id"`
    WorkID     string       `json:"work_id"`
    NodeID     string       `json:"node_id"`
    WorkerID   string       `json:"worker_id"`
    AttemptID  string       `json:"attempt_id"`
    GrantedAt  time.Time    `json:"granted_at"`
    ExpiresAt  time.Time    `json:"expires_at"`
    LastBeatAt time.Time    `json:"last_beat_at"`
    Status     LeaseStatus  `json:"status"`
}
```

State transitions:

```
            grant            heartbeat (extends ExpiresAt)
  (none) ─────────► ACTIVE ─────────────────────► ACTIVE
                     │
                     ├── reaper detects ExpiresAt < now()  ──► EXPIRED
                     ├── POST /v1/leases/{id}/release     ──► RELEASED
                     ├── POST /v1/works/{id}/cancel      ──► REVOKED
                     └── POST /v1/leases/{id}/complete    ──► (terminal, row kept)
```

### HTTP API additions

| Method | Path | Body | Effect |
|---|---|---|---|
| POST | `/v1/leases/grant` | `{work_id, node_id, worker_id, ttl_seconds?}` | Returns `Lease` with id+expiry. **Atomically** transitions node to RUNNING if no active lease exists. |
| POST | `/v1/leases/{id}/heartbeat` | `{}` | Extends `ExpiresAt` by `ttl_seconds` (default 30s). 409 if lease is not ACTIVE. |
| POST | `/v1/leases/{id}/complete` | `{exit_code, artifact?, evidence?}` | Marks lease RELEASED, finalizes attempt, transitions node result. |
| POST | `/v1/leases/{id}/release` | `{reason?}` | Marks lease RELEASED, marks attempt cancelled. |
| GET | `/v1/works/{id}/nodes/{n}/logs?follow=true` | — | Streams artifact log (slice 1: file on disk). |

### TTL math (SLO compliance)

- **Lease TTL:** 30s (matches SLO target).
- **Heartbeat interval:** 10s (worker side).
- **Reaper interval:** 10s (API side).
- **Worst-case lost-worker detection:** TTL (30s) + reaper-interval (10s) = **40s**.
- **To meet SLO ≤30s**, reduce TTL to 20s or reaper to 5s.

**Decision:** lease TTL = 25s, heartbeat every 10s, reaper every 5s. Worst-case
detection = 25 + 5 = **30s** (right at the SLO boundary). Worker default keeps
the heartbeat at 10s; if a heartbeat is missed once, the lease expires within
25s and the reaper catches it within the next 5s.

### Lease-reaper goroutine

Runs in the API process. Every 5s:

1. SELECT * FROM work_leases WHERE status='ACTIVE' AND expires_at < now
2. For each: UPDATE work_leases SET status='EXPIRED' WHERE id=? AND status='ACTIVE'
   (idempotent, safe under concurrent reapers later)
3. UPDATE work_attempts SET status='cancelled', finished_at=now, error='lease expired'
   WHERE id = lease.attempt_id AND status='running'
4. The node is now free to be re-leased — `ReadyNodes` will return it because no
   attempt is `running` and no `succeeded` attempt exists.

We do **not** set the Work state to FAILED on a single lease expiry — the node
may requeue and succeed on another attempt. Only transition to FAILED if the
policy says max-attempts-exceeded (slice 3).

### Worker refactor

Slice-1 worker used direct store writes. Slice-2 worker goes through HTTP:

1. **Poll** `GET /v1/workers/ready` (same as slice 1).
2. For each ready item, **request lease** via `POST /v1/leases/grant`. If 409
   (someone else got it), skip.
3. **Start heartbeat goroutine** that POSTs `/heartbeat` every 10s. Stops when
   the lease is complete/released/expired/revoked.
4. **Execute** the subprocess. On lease expiry detected by heartbeat error
   (409 on heartbeat = lease no longer ACTIVE), `cmd.Process.Kill()` the
   subprocess and treat the attempt as cancelled.
5. **Report result** via `POST /v1/leases/{id}/complete` with exit code,
   artifact reference, evidence.
6. **Continue polling.**

The subprocess kill is the critical correctness guarantee — without it, the
pack's "worker disposability" principle is violated.

### Log streaming

Trivial slice 2 implementation: the artifact file at
`<artifacts_dir>/<work_id>/<node_id>.log` is the log. `GET /v1/works/{id}/nodes/{n}/logs`
returns:

- `200` with the full file contents (chunked transfer if large).
- `206` with byte range if `Range:` header is present.
- `404` if no artifact exists for that work+node.

This is honest and correct; a streaming-from-worker-pipe implementation is
slice-3 work.

### Worker-to-API transport

Slice 1 used shared SQLite file. Slice 2 uses **HTTP for everything** — leases,
heartbeats, results. This matches the pack's "control plane owns state" rule
(ADR-0002) properly: workers never touch the store directly.

Trade-off: more network traffic per node. Acceptable for V1 because nodes
are coarse-grained.

## Security & trust

- **Lease IDs are unguessable** (16 random bytes hex-encoded, same as Work
  IDs). A worker cannot steal another worker's lease.
- **Lease completion requires the lease ID + an HMAC of (work_id, node_id,
  lease_id) signed with the worker's enrollment key.** Slice 2 defers
  enrollment-key issuance — V1 uses a static `WORKS_WORKER_TOKEN` env var
  shared between API and worker for slice 2 only. Slice 3 replaces this with
  proper mTLS or short-lived JWTs.
- **Fork policy** (per pack threat model item #7): unchanged from slice 1.

## Migration

Slice 2 is a non-breaking change to slice 1 data: the existing `work_attempts`
table gains a nullable `lease_id` column; a new `work_leases` table is added.
No backfill required. The migration uses the same `pragma_table_info` pattern
introduced in ADR-0005.

The CLI does not need changes — `works run` still POSTs to `/v1/works`, the
worker handles the lease internally.

## Test plan

| Layer | Test |
|---|---|
| Unit | `packages/workgraph`: Lease state machine, `CanTransitionLease`, lease-aware `ReadyNodes` |
| Unit | `services/work/store`: `GrantLease`, `RenewLease`, `RevokeLease`, `ExpireLease`, `ListExpiredLeases` |
| Unit | `services/api`: lease grant happy path, grant conflict (already leased), heartbeat extends expiry, complete transitions correctly |
| Integration | `e2e/lease_test.go`: full happy path through HTTP — grant → heartbeat → complete → SUCCEEDED |
| Chaos | `e2e/chaos_test.go`: start worker on `sleep 60` node, kill -9 worker, assert lease EXPIRED within 30s and node requeues |
| Regression | All slice 1 tests still green |

## Rollout

1. Land slice 2 on a branch.
2. Run all tests (slice 1 + slice 2) green.
3. Manual integration run with a real worker held under a lease.
4. Open PR with this RFC linked in the body.
5. Do not merge to main until Jonas signs off (AVC policy: Normal track requires
   explicit approval).

## Open questions / future

- Lease renewal behavior on worker clock skew — slice 3 should add `server_now`
  to heartbeat responses and have the worker compute the next expiry
  server-side rather than client-side.
- Lease transfer between workers — slice 3+; not needed for V1 since lease
  expiry returns the node to ready.
- Lease audit events — slice 3 (every grant/renew/revoke should be an
  `AuditEvent` per the pack's audit-baseline requirement).

## Rollback

Revert this slice. The store migration drops `work_leases` and removes the
`lease_id` column from `work_attempts`; this is mechanical because the slice
1 schema is a strict subset of slice 2's.

The HTTP endpoints can be removed without breaking slice 1 because no slice
1 client called them.

## Alternatives considered

- **Heartbeat-only (no leases):** simpler but doesn't solve the
  cancellation story. Rejected.
- **Optimistic concurrency on work_attempts:** would work but adds
  complexity to a code path the worker is already rewriting. Rejected for
  slice 2.
- **Push-based scheduler (long-poll):** the pack's protocol draft implies
  push but V1 doesn't need it. Pull-based polling keeps the surface small.
  Deferred to slice 3.