# v0.2.0 — Governed kernel: contracts, missions, evidence, link

**Date:** 2026-09-02 · **Base:** main @ `05cdacb` · **Range:** `0eca06b` (freeze-slice-0, PR #9) → `4f9bc69` (k-link-01, PR #20)

v0.1 built the execution machinery: durable `Work` objects, lease-based
scheduling, disposable workers, webhook ingestion, result publishing. v0.2
makes that machinery *governed*: every surface the kernel exposes is pinned by
a frozen, hash-attested contract, and regression tests over those contracts
are law — drift changes a hash, never silently changes semantics.

## What each merged slice added

### Contract Freeze Slice 0 — `contracts(freeze)` (PR #9, `0eca06b`)

Twenty frozen contracts materialized as 21 draft-07 JSON schemas under
`contracts/schemas/`, wrapped in `contracts/manifest.json` (21 entries;
`work.schema`/`handoff` inlined) with a freeze-attestation SHA-256 in
`contracts/manifest.sha256`
(`2d2f1d27474a908a19aafb9c152be5e27c80987400f21cdfca94080b8bf14a86`).
`tests/contracts/` adds 21 contract tests (conformance, baseline,
adversarial, compatibility) that are now regression law: a schema change
without a re-generated manifest hash fails CI by design. Compatibility rule
frozen per `proto.charter/1.0`: N-1 read tolerance; breaking changes require a
major version bump.

### k-mission-01 — `kernel(mission)` (PR #10, `ab87f8d`)

Mission contract + budget ledger (ADR-0008/0009). Mission `Work`s carry a
`MissionContract` (budget ceiling, verification criteria, purpose bindings,
kill switch) and a `BudgetLedger` (reserved at lease time, consumed
continuously, clock paused while `WAITING_HUMAN`, stopped on hard stop; late
provider bills recorded but never push user-visible consumption past the
ceiling). The state machine gains the forward mission states
`WAITING_HUMAN`, `SUSPENDED`, `BUDGET_EXHAUSTED` — reachable only by mission
works, never by CI works.

### k-mission-02 — `kernel(mission)` persistence (PR #11, `5041452`)

Handoff/checkpoint persistence (ADR-0010, `handoff.schema/1.0`). A handoff is
the frozen 5-layer checkpoint payload (state snapshot, narrative, decision
log, priority queue, warnings) written only on kernel-recognized
suspend/wait/fail transitions. Suspend/resume is fail-closed: an invalid
handoff is never persisted, resume without a checkpoint is refused, and a
checkpoint whose re-derived hash mismatches its persisted hash
(`ErrCorruptHandoff`) is never silently resumed from. Schema v8: `works.mission_json`
column + `work_handoffs` table.

### k-evid-01 — `evidence(quittance)` (PR #12, `a83a8a9`)

FailureAttribution + Quittance (ADR-0011/0024) in `services/evidence`: the
settlement-grade mission receipt layer. Kernel-negation law: a work that
failed verification can never carry a price hint
(`ErrQuittanceFailedPriced`) and a quittance cannot exist without an evidence
bundle (`ErrQuittanceNoEvidence`) — *failed ⇒ no price*.

### k-hal-01 — `hal(cpi)` (PR #13, `4f05fc0`)

The CPN provider boundary at `cpi/1.0` (ADR-0012/0018): a single frozen
handshake/exec/snapshot contract every compute provider speaks. Plus the
conformance harness (`packages/providers`): interface compliance alone proves
nothing — every future provider must pass the executable `ConformanceSuite`
before it can be registered.

### k-pulse-01 — `hal(pulse)` (PR #14, `4994501`)

PULSE-Node CPN adapter (`packages/providers/pulse_provider.go`, ADR-0013/0026),
consent-gated and request-only: it announces only capabilities backed by an
active ConsentGrant, every wire call carries the grant reference and the
daemon re-validates (double enforcement), and with no grants it emits zero
outbound bytes — every call fails locally before any network dial. v1
transport is loopback HTTP on `127.0.0.1:7777` with a localhost pairing token;
mTLS is the v2 upgrade path.

### k-now-01 — `shell(now)` (PR #15, `d9c77de`)

NOW-shell contract surface (ADR-0025, `shell.contracts/1.0`,
`packages/shell`): typed validation law for every shell surface plus the NOW
projection — the live mission view of what needs human attention first and
what the budget clock is doing. NOW is a read surface (T1); privileged
actions (approve/deny/kill/take/hand_back) belong to the COMMAND surface at
T3 and can never be exposed by `pulse` or `local_only` surfaces.

### k-cap-01 — `capability(cap)` (PR #16, `b1491fd`)

Capability-evaluation law (`packages/capability`, `policy.token/1.0`): a
shell contract declaration executes only if the frozen contract allows it AND
the caller's policy token carries a matching scope. Grants carry `token_id`,
`work_id`, org, and can only ever narrow, never widen, delegation.

### k-billing-01 — `billing(settle)` (PR #18, `d7bc4e4`)

Settlement law over quittance + budget (`packages/billing`, ADR-0011/0024 +
0009): settlement consumes the budget ledger against a quittance under four
laws — clock law (paused clocks never meter), clamp law (late bills never
push past the ceiling), late-bill evidence law, fail-closed (no quittance, no
settlement). Settlement never mutates the ledger.

### WORKS Conversation V1 Task 1 — journaling (PR #19, `e89d2e9`)

Every canonical state transition is journaled to the durable per-work event
journal (schema v9 `work_events`, globally monotonic sequences), with a REST
journal listing for the conversation mirror; SSE consumers resume with
sequence > cursor.

### k-link-01 — `feat(link)` (PR #20, `4f9bc69`)

The WORKS-Link server surface (ADR-0026/0027, `services/api/link_handler.go`,
`packages/link`): the only routes a PULSE device may present to the kernel —
`POST /link/v1/pair`, `POST /link/v1/mounts`, `GET /link/v1/missions`,
`POST /link/v1/revoke`. `/link/v1/commands` exists in the frozen
`link.wire/1.0` enum but is deliberately **not mounted**: the request-only law
(PULSE is never a controller) makes every privileged command structurally
unmountable. Auth: SAS pairing (6-char human-displayed code) mints a
short-lived HMAC device token; the service re-reads durable device state on
every call. Schema v10: `link_devices` + `link_mounts`. Enabled opt-in via
`WORKS_LINK_PAIRING_SECRET` (min 32 bytes); without it every link route stays
mounted and answers **503 fail-closed** — never a silent default. See
[runbooks/link-surface.md](runbooks/link-surface.md).

## Upgrade notes (v0.1 → v0.2)

- **No breaking wire changes.** All new surfaces are additive and the frozen
  compatibility rule is N-1 read tolerance per `proto.charter/1.0`; existing
  API clients keep working unchanged.
- **Schema migration v7 → v10, automatic and idempotent on restart.** The
  production store (`/var/lib/works/works.db`) is currently at schema v7.
  Restarting `works-api`/`works-worker` with the new binary applies, in order:
  v8 (`works.mission_json` + `work_handoffs`, PRAGMA-checked column add),
  v9 (`work_events` journal), v10 (`link_devices` + `link_mounts`). All
  migrations are `CREATE TABLE IF NOT EXISTS` / introspection-guarded `ALTER
  TABLE` — safe to run repeatedly, no data backfill. Verify after restart:
  `sqlite3 /var/lib/works/works.db 'select max(version) from schema_version'`
  → `10`.
- **`WORKS_LINK_PAIRING_SECRET` is opt-in.** Generate one (`openssl rand
  -hex 32`, ≥ 32 bytes), add it to `/etc/works/works.env`, and restart. If it
  is absent or short, the link surface is mounted but every route answers
  503 — that is the intended fail-closed posture, not an error to fix under
  pressure. Setup walk-through: [runbooks/link-surface.md](runbooks/link-surface.md).
- **Contract tests are regression law.** If you change anything under
  `contracts/schemas/`, re-run `python3 contracts/gen_freeze.py` — the
  manifest hash must move visibly. CI runs the 21 tests in `tests/contracts/`.

## Governance invariants (unchanged by v0.2, now contract-pinned)

- **State machine authority** — only the kernel moves a `Work` through its
  transitions; the transition table is frozen in `work.schema/1.0` and locked
  by baseline contract tests.
- **Evidence-on-disk for `SUCCEEDED`** — a succeeded work has evidence
  records; a quittance cannot exist without an evidence bundle.
- **Lease TTL** — leases expire and are re-grantable; expired leases are
  reclaimable via `ListExpiredLeases`.
- **Idempotent webhooks** — webhook ingestion is replay-safe.
- **Frozen contract tests** — the 21 tests in `tests/contracts/` fail on
  drift; the manifest hash is the freeze attestation.
- **Pool enforcement at grant** — BYOC pool membership is checked when a
  lease is granted, not later.
- **Cache exit≠0 refusal** — the content cache never stores results from a
  failed attempt.

## ADR boundary (honest note)

ADRs 0005–0007 live in this repo (`docs/adr/`). ADRs 0008–0027 referenced by
the v0.2 slices live in the AVC workspace docs, **not** in this repository —
they are cited from the frozen schemas and package docs here, but their text
is not vendored in-repo.

## Verification at release

- `go build ./...` — clean
- `go test ./tests/contracts/ -count=1` — 21/21 PASS
- Full suite `go test ./... -count=1` — all packages ok (per
  `contracts/FREEZE_EVIDENCE.md`: 30/30 packages, exit 0)