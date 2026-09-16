# CircuitRun Durable Binding Design

**Status:** Proposed implementation slice for WORKS; no production deployment.

## Purpose

CircuitRun binds one composition-valid CircuitSpec to the durable Work that executes its consequential effect. WORKS owns this persistence because Work is already the source of execution truth.

CircuitRun does **not** introduce a second workflow/state machine. Work state, attempts, leases, artifacts and execution evidence remain authoritative in existing WORKS structures.

## Chosen shape

Use an immutable sidecar aggregate rather than a new Work type or Circuit fields embedded into Work.

`CircuitRunInput` contains `circuit_id`, raw `circuit_spec`, `work_id`, and `mission_id`. The store canonicalizes the JSON, computes SHA-256, assigns `crun_<32 hex>` and server time, then persists the immutable Run.

## Invariants

- `Work` remains the only execution state machine.
- CircuitRun creation requires an existing Mission Work; legacy CI Works fail closed.
- WORKS stores CircuitSpec as opaque semantic content. It validates JSON shape/canonical digest, not Circuit composition semantics.
- One root CircuitRun may bind to a Work in v0.1. Byte-identical retries are idempotent; a different binding for the same Work conflicts.
- CircuitRun grants no AIE authority, Trust admission, worker lease, execution permission, or verifier verdict.
- `circuit_spec_sha256` binds later verdicts to the exact compiled subject.
- CircuitSpec bytes are canonicalized before hashing/persistence so insignificant JSON formatting cannot change identity.

## Persistence

Schema v14 adds `circuit_runs(id, circuit_id, circuit_spec_sha256, circuit_spec_json, work_id, mission_id, created_at)` with a unique `work_id` foreign key to `works`.

Store methods: `CreateCircuitRun`, `GetCircuitRun`, and `GetCircuitRunByWorkID`. No API endpoint is introduced in this slice.

## Later slices

ExecutionContext remains independently immutable and discoverable by WorkID. CircuitVerdict will later bind `CircuitRunID + exact subject digest + independent verifier evidence`; it must not be inferred from Work success.
