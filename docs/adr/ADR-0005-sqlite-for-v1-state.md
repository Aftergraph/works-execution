# ADR-0005: V1 uses SQLite for durable state (deviation from pack)

**Status:** Accepted
**Date:** 2026-08-31
**Deciders:** Founding engineer (Hermes agent, on behalf of Jonas)

## Context

`docs/works-venture-starter-pack/03_ENGINEERING/IMPLEMENTATION_PLAN.md` prescribes PostgreSQL for V1 durable metadata. `02_ARCHITECTURE/SYSTEM_ARCHITECTURE.md` repeats "PostgreSQL metadata, queue/event system, artifact/CAS object storage" as the data layer. No ADR in the source pack justifies PostgreSQL specifically; it is recommended as the V1 starting point.

For the first verifiable slice (Go monorepo + Work schema + local worker + minimal API + end-to-end test passing) we need a state store that:

1. Works **without infrastructure setup** on any developer machine, CI runner, or container image.
2. Is a single file that can be inspected, copied, or deleted to reset state.
3. Has a Go-native driver with no CGO required (so `go build ./...` works on a stock Go install).
4. Supports the durability and atomicity guarantees the state machine requires: each `Work` state transition is committed atomically and survives process restart.

PostgreSQL satisfies durability but requires Docker, system packages, port allocation, user setup, and connection pooling. SQLite satisfies all four criteria and matches the V1 non-goal of "general cloud replacement."

## Decision

V1 uses **SQLite** (`modernc.org/sqlite`, pure Go driver — no CGO) for the `Work`/`Node`/`Attempt`/`Evidence` store. Schema migrations are checked into `services/work/store/migrations/`. The store is wrapped behind a `Store` interface so the underlying driver can be swapped without touching the API or worker.

## Consequences

**Positive**

- Zero-infra V1: `go build && ./bin/works-worker` just works.
- Single-file database simplifies the e2e test (use a per-test tempdir).
- WAL mode gives us concurrent reads with a single writer, sufficient for V1.
- The `Store` interface keeps the API and worker oblivious to the driver.

**Negative**

- Single-writer bottleneck becomes visible at moderate concurrency. The state machine only writes on transition events, which are rare relative to reads.
- Migration to PostgreSQL is a real piece of work later — column types, JSON handling, advisory locks for leases all differ.
- Cloud Postgres deployment (the production story) is not this codebase.

**Operational**

- Backups = `cp *.db backups/`. Documented in `docs/operations/BACKUP.md` (to be written in slice 2).
- WAL files must be checkpointed before copying.
- The `Store` interface must carry `BEGIN IMMEDIATE` semantics or equivalent; SQLite's WAL with `PRAGMA busy_timeout=5000` is sufficient for V1.

## Migration path to PostgreSQL (when V1 graduates)

1. Replace `services/work/store/sqlite.go` with `services/work/store/postgres.go` behind the same `Store` interface.
2. Translate migration files (the SQL is intentionally portable: VARCHAR, INTEGER, BLOB, ISO-8601 TEXT).
3. Move `Lease.ExpiresAt` enforcement from in-process to `SELECT ... FOR UPDATE SKIP LOCKED` in Postgres.
4. Move `Evidence` JSON columns from `TEXT` to `JSONB`.
5. Run both backends side-by-side behind a feature flag for one design-partner pilot.

## Rollback

Revert this ADR. The `Store` interface means the swap is mechanical; the state machine, API, and worker code do not change.

## Security impact

No change. Tenant isolation in V1 is by database file path; in PostgreSQL it will be by `tenant_id` column + row-level security. Same threat boundary, different enforcement.