# ADR-0033: Lease fencing tokens and a transactional outbox

**Status:** Accepted
**Date:** 2026-10-02
**Deciders:** Owner (D2 and D3 locked decisions), implemented by Aftergraph Builder Max
**Schema impact:** `SchemaVersion` 17 -> **18** (`services/work/store/store.go`)

## Context

Two related holes in the durable-execution slice, both of which only
appear once the plane is running for real rather than in tests.

### D2 — a stale lease holder can write to a lease it no longer owns

`work_leases` had a TTL and a status, and `RenewLease` / `CompleteLease`
enforced `WHERE id = ? AND status = 'ACTIVE'`. Nothing distinguished *which
grant* of a node a caller was acting under.

The failure: worker A's lease expires while A is partitioned or hung. The
reaper revokes it, the node is re-granted to worker B, and A wakes up and
POSTs `/complete`. A presents a correct `(leaseID, status=ACTIVE)` pair —
for a lease generation that no longer owns the node — and writes its stale
result over B's live attempt.

`store.CompleteLease` also took **no worker identity at all**. The
owner-bind in `services/api/lease_owner_authz.go` (k-065) was the only
check, and it is an HTTP-layer check: anything reaching the store directly
bypassed it entirely.

### D3 — the publish side effect is not atomic with the state change

`services/api/publisher_hook.go` fired a GitHub status update from a
goroutine after `Store.CompleteLease` had already committed. A crash
between commit and the HTTP call loses the status update permanently, and
nothing durable recorded that it was ever owed. There was no retry.

An outbox *outside* the store does not fix this. `enqueue` before commit
can publish a state that never happened; `enqueue` after commit has exactly
the crash window above. Only same-transaction makes "the Work is SUCCEEDED"
and "a status update is owed" a single atomic fact.

## Decision

### D2 — monotonic fencing token, enforced as a compare-and-swap in the store

**Schema (v18).** `work_leases.epoch INTEGER NOT NULL DEFAULT 0`, added by
`migrateLeaseFencingAndOutbox` via the `pragma_table_info` introspection the
repo's other column adds use. Pre-v18 rows land on epoch 0, which no holder
can present — the fail-closed direction; re-granting the node issues a real
epoch.

**Minting.** `GrantLease` computes
`SELECT COALESCE(MAX(epoch), 0) + 1 FROM work_leases WHERE work_id = ? AND node_id = ?`
inside the grant transaction. The epoch is monotonic **per node**: a lease
on node `a` never fences a lease on node `b`, so unrelated nodes cannot
perturb each other's tokens. The first grant of a node is epoch **1**; zero
is reserved to mean "caller presented no token", which is what makes an
omitted epoch fail closed instead of matching epoch-zero rows.

**The fencing triple.** The four state-mutating verbs now take a
`store.LeaseRef` — `(executorId, leaseId, leaseEpoch)` — instead of a bare
lease id:

```go
type LeaseRef struct {
    LeaseID  string
    WorkerID string // executor identity; must equal the lease's worker_id
    Epoch    int64  // fencing token; must equal the lease's current epoch
}
```

All three parts are load-bearing and none is derivable from the others.
`LeaseID` selects the row. `WorkerID` is the *executor identity* — without
it, anyone holding the id could act as the lease holder, which is exactly
the gap `CompleteLease` had. `Epoch` proves *recency* — only it separates a
worker that lost its lease and woke up late from the current holder, since
both present a correct `(leaseID, workerID)` pair.

`GrantLease` keeps its plain signature: it is the verb that establishes the
epoch.

**Enforcement.** Every fenced verb runs `loadLeaseForFence` (classify) then
a single conditional `UPDATE`:

```sql
UPDATE work_leases SET ... WHERE id = ? AND status = 'ACTIVE' AND epoch = ? AND worker_id = ?
```

and requires `RowsAffected() == 1`. **The CAS predicate — not the
pre-read — is the law.** This is the whole reason the design is safe under
concurrency, and it is what makes the "two callers hold the same lease and
both complete it" case correct:

> Both callers' pre-reads legitimately observe `ACTIVE` with a matching
> epoch. Neither pre-read can detect the other; they are not racing to
> discover anything. What separates them is the `UPDATE`: the first writer's
> predicate matches and moves the row to `RELEASED`; the second writer's
> predicate no longer matches, affects zero rows, and `requireCASRows`
> re-reads inside the transaction to report `ErrLeaseNotActive`. Exactly one
> completion wins. The loser never finalizes the attempt twice.

The pre-read exists only to produce a precise error. On a single-connection
writer pool it also serializes callers, but the CAS does not depend on that
and would hold across two processes on one database file.

**Stale-epoch refusal — `ErrLeaseFenced`.** A distinct exported sentinel
alongside `ErrLeaseConflict` and `ErrLeaseNotActive`:

```go
var ErrLeaseFenced = errors.New("lease fenced: stale executor or epoch")
```

It is distinct because the two mean different things and a caller must act
differently. `ErrLeaseNotActive`: the lease is finished; re-granting the
node is the legitimate next step. `ErrLeaseFenced`: your token is stale;
your lease generation no longer owns this node, and retrying is pointless.

Wrapped errors carry presented-vs-current values so an operator can tell a
zombie worker from an identity bug. `errors.Is` holds in both cases.

**Error precedence is a security property, not a convenience.** Checked in
this order:

1. missing row -> `ErrNotFound`
2. not `ACTIVE` -> `ErrLeaseNotActive`
3. worker/epoch mismatch -> `ErrLeaseFenced`

Status is checked *before* the token so a finished lease never leaks
anything about the token through the shape of its error, and an already
released lease has no meaningful epoch to compare against. A caller cannot
use the error to distinguish "stale but real" from "fabricated id":
`ErrNotFound` for the latter, `ErrLeaseNotActive` for the former.

This ordering has a consequence worth stating, because it is counter-intuitive
on first reading: **the zombie-worker scenario surfaces as
`ErrLeaseNotActive`, not `ErrLeaseFenced`.** A re-grant mints a *new* lease
row, so the zombie's own row is left `RELEASED`, and step 2 fires before
step 3 is ever reached. The write is refused either way — the security
property holds — but the sentinel describes what is actually true of that
row: the lease is finished.

`ErrLeaseFenced` fires on the cases where the row is still live and the
token is what does not match:

- a wrong executor presenting a live lease's id and current epoch (the
  impostor case, and the one `CompleteLease` previously had no defence
  against at all); and
- an epoch mismatch on a live `ACTIVE` row.

Both are covered by `TestFencedVerbs_RefuseWrongExecutor`,
`TestFencedVerbs_RefuseZeroEpoch`, and
`TestFencedVerbs_EpochMismatchOnActiveLeaseIsFenced`.

There is deliberately no HTTP-level test for the wrong-executor case: in
dev mode (`AuthEnabled=false`) the executor identity is resolved *from the
lease row itself*, so an HTTP caller cannot present an executor that differs
from the stored one. The scenario is unreachable through that surface
without full claim-bearing auth setup, and is pinned at the store layer
where it belongs.

**HTTP surface.** `heartbeat`, `complete`, `release`, and `revoke` accept an
`epoch` field. The executor identity comes from the authenticated token in
production, falling back to the lease's own `worker_id` in dev mode
(`AuthEnabled=false`), matching the existing k-065 nil-claims precedent.

**This is a deliberate, breaking wire change.** A pre-ADR-0033 worker sends
no epoch, gets `epoch: 0`, and is refused with **409 `lease_fenced`**. That
is correct: an unfenced client must not be able to act. `internal/worker`'s
client refuses locally if a server returns a lease without an epoch, so a
version mismatch fails at claim time with a clear message rather than
mid-execution. Coordinated deploys are required.

**The reaper is fenced too.** `ListExpiredLeases` returns the epoch, and
`reapOnce` presents the token it actually observed. Without this, a reaper
that listed a lease and was then overtaken by a re-grant would cancel the
*new* holder's live lease. `reapOnce` skips refusals, exactly as it
already skipped idempotent refusals.

**`lease_owner_authz.go` is retained as defence in depth.** The HTTP
owner-bind still refuses before any handler runs; the store's check now
makes that bind non-vacuous for direct store callers instead of being the
only one.

### D3 — transactional outbox

**Schema (v18).** `outbox`, with `idempotency_key TEXT NOT NULL UNIQUE`,
`status` (`PENDING` / `CLAIMED` / `DELIVERED` / `DEAD`), `attempts`,
`max_attempts`, `claimed_by`, `claimed_at`, `claim_expires_at`,
`delivered_at`, `last_error`.

**Written in the same transaction as the state mutation.** `UpdateState`
enqueues a `work.terminal` obligation inside its own transaction whenever
the target state is terminal, before it commits. The topic is a *Work-level
fact*, not a GitHub-specific one: WORKS owns the fact that the Work
finished, and whatever renders that fact subscribes. Coupling the store to a
publisher would invert that.

**How duplicate delivery is prevented — three independent mechanisms:**

1. **Deterministic idempotency key.** `work.terminal:<work_id>:<STATE>` on
   a `UNIQUE` column, inserted `INSERT OR IGNORE`. A retried mutation, a
   replayed webhook, or a second enqueue of the same terminal fact collapse
   to one row instead of producing two deliveries.
2. **Claim CAS.** A dispatcher selects candidates, then claims each with a
   conditional `UPDATE` re-asserting the claimable predicate.
   `RowsAffected() == 1` is the only proof of ownership. A dispatcher that
   merely selected rows and then delivered them would double-deliver under
   concurrency. Reclaimable claims (`CLAIMED` with `claim_expires_at` in the
   past) mean a crashed dispatcher cannot strand rows forever.
3. **Ownership-scoped settlement.** `MarkOutboxDelivered` and
   `FailOutboxAttempt` re-assert `claimed_by`, so a dispatcher whose claim
   expired cannot settle a row behind the dispatcher's back
   (`ErrOutboxNotClaimed`).

**Delivery is at-least-once, not exactly-once — stated plainly.** The
claim protocol guarantees two *live* dispatchers never hold the same row.
It cannot close the one window no database transaction can: the external
side effect succeeded, then the process died before the settle committed.
This is inherent, not a defect, and it is why handlers must be idempotent —
`services/publisher` already requires exactly that ("MUST be idempotent on
(Repository, SHA)").

**Failure handling.** A handler error returns the entry to `PENDING` for
retry, or `DEAD` once `attempts` reaches `max_attempts` (default 3). `DEAD`
rows are retained, never deleted — an undelivered side effect must stay
auditable. A failed entry never aborts the batch: the rest of the queue
still gets its turn.

**Migration of `maybePublishOnTerminal` — deliberate, not silent.** The call
at the end of `completeLease` is **removed**. Publishing now happens only
via the dispatcher. The publish *decision* was factored out into
`terminalPublishResult`, so there is exactly one implementation of "which
states publish and what conclusion they map to" shared by both drivers;
two copies would inevitably diverge. `maybePublishOnTerminal` itself is
retained (same behaviour, same tests, same k-068 drain gate) as an operator
and test entry point exposed via `PublishTerminal`, and its documentation
states plainly that it is no longer on the request path.

Two dispatcher-specific behaviours worth naming:

- An **unpublishable** Work (CLI provenance, no repository or non-40-char
  SHA) is retired **without** a side effect and **without** burning retry
  budget. Otherwise every local run would exhaust its attempts on an
  impossible publish and end `DEAD`, which makes `DEAD` mean nothing.
- A **publisher error** is retried. That is the entire reason the outbox
  exists: a transient GitHub 5xx during a Work's completion used to be
  dropped on the floor.

`cmd/works-api` starts the dispatcher with a `hostname-pid` dispatcher id,
alongside the lease reaper. It is safe to run concurrently with request
handling, and safe to run as several instances — the claim CAS is what
prevents overlap.

### Coupling note

D2 and D3 land at the same schema version because the outbox is written by
`UpdateState`, and `CompleteLease` is one of the fenced verbs. Splitting
them would mean shipping the outbox against writes that were not yet
fenced.

## Consequences

**Positive**

- A stale holder cannot write to a lease it no longer owns.
- The store's own check is non-vacuous; direct store callers are covered,
  not just HTTP callers.
- Concurrent completion of one lease finalizes it exactly once.
- The reaper cannot cancel a lease that was re-granted underneath it.
- Terminal status updates are durable and retried instead of fire-and-forget.

**Negative**

- **Breaking wire change.** Pre-ADR-0033 workers are refused with 409
  `lease_fenced` until they carry the epoch. Coordinated deploy required.
- Releasing or revoking an already-terminal lease now returns
  `ErrLeaseNotActive` where it previously returned a wrapped
  `workgraph.ErrInvalidTransition`. Both refuse the mutation and both map
  to HTTP 409; only the sentinel changed. No caller distinguished them.
- `DEAD` outbox rows accumulate and need operator attention.
- An outbox row is written for every terminal transition, including CLI
  works that will never be published. The dispatcher retires them cheaply.

**Operational**

- Backups need no change; `outbox` is in the same database file.
- A wedged dispatcher (crashing on every drain) would leave entries to age
  out via claim expiry and eventually go `DEAD`. Worth an alert on
  `DEAD > 0`.

## Rollback

Revert this ADR. The `epoch` column is additive and may be left in place
unused; the `outbox` table is additive and inert if the dispatcher is not
started. Rolling back the store alone is therefore safe — but the wire
change is not, since the two halves must move together.

## Security impact

Net tightening. Stale-token and wrong-executor writes are refused inside
the store rather than only at the HTTP layer, so the previous hole for
direct store callers is closed. `UNKNOWN` (ADR-0032) is additionally
non-leasable, which is strictly more restrictive. No authentication,
authorization, lease-conflict, idempotency, or checkpoint-integrity
behaviour is relaxed. The outbox does not weaken evidence integrity: it
moves a status-update side effect into a retried queue, and terminal state
transitions themselves remain governed by the same store transaction laws as
before.

## Verification status

Go was not available in the authoring environment, so **no test in this
change has been executed**. Every claim above is from reading the code.
Verification is CI-only: `.github/workflows/go-test.yml` runs
`go build ./...`, `go vet ./...`, `go test ./... -count=1`.