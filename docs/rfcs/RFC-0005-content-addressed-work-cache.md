# RFC-0005: Content-Addressed Work Cache

**Status:** IMPLEMENTED (2026-08-31)
**Author:** Hermes Agent (atlas)
**Date:** 2026-08-31
**Track:** Normal
**Supersedes:** none
**Related:** RFC-0004 (BYOC pools), RFC-0006 (self-hosted CI), ADR-0005 (SQLite)

## Problem

Every push to a pilot repo re-ran the full pipeline: `go vet` + `go test`
+ `go build` from scratch, even when the inputs were byte-identical to a
previous run. For a self-hosted control plane whose selling point is
"faster than Actions", re-executing provably-identical work is wasted
compute and wasted wall-clock. But caching CI results is dangerous: a
false hit (replaying a result for inputs that were actually different)
is a product-integrity failure, not a performance miss.

## Decision

Cache computation **only when equivalence can be proven**. A node's
outcome is a pure function of a small, enumerable input set; bind all of
it into a content-addressed key and store only successful results.

## Design

### 1. Canonical fingerprint

`packages/cache.Key` — every field that can change a node's outcome
participates in the SHA-256 hash; nothing outside it does:

- `run` — the node's command, exact bytes
- `repository` + `ref` + `sha` — the work's source identity
- `env` — the node's declared environment variables (canonicalized by
  `json.Marshal`, which sorts map keys — map iteration order would
  otherwise poison the hash)
- `os` + `arch` — the work's declared requirements
- `scope` — `worker` | `organization` (worker-local keys include the
  worker id; organization keys do not)

The fingerprint is the hex SHA-256 of the canonical JSON document.

### 2. Store

`packages/cache.Store` backs onto the same SQLite database as the works
table (single-writer is fine: cache reads happen once per node
execution). Table `work_cache`:

- `fingerprint TEXT PRIMARY KEY`
- `scope`, `work_id` (first creator, provenance), `node_id`
- `exit_code`, `log_tail` (truncated to 4 KiB — full logs stay in the
  artifact store; the cache only needs enough to explain a hit in the UI)
- `created_at`

**Only successful executions are cached.** `Put` refuses `exit_code != 0`
— failures are re-run by design; flaky infrastructure should not be
memorialized. `ON CONFLICT(fingerprint) DO NOTHING` keeps the first
creator as provenance.

### 3. Scheduler + worker integration

- The scheduler attaches `cache_key` to ready items.
- The worker claims a hit **before** executing: duration 0, evidence
  `cache:hit`, log tail replayed from the entry.
- Successful misses are stored after execution.

### 4. API

`GET/PUT /v1/cache/{key}` behind the same bearer auth as the rest of the
worker surface.

## Correctness posture

A false hit is a product-integrity failure, so the fingerprint errs on
the side of missing inputs — anything not captured changes the key,
never gets merged in. Hit rate is secondary to correctness.

## Verification (production VDS, 2026-08-31)

- Cold pipeline (no cache): **21s**.
- Warm pipeline (all hits): **3s** — **7× faster**.
- 5 unit tests in `packages/cache`; full cycle verified on prod
  (k-impl-028).

## References

- `packages/cache/cache.go`, `packages/cache/cache_test.go`
- `services/api/cache_handler.go`, `services/api/api.go`
- `internal/worker/worker.go`
- Starter pack: `CACHE_AND_CAS.md`
