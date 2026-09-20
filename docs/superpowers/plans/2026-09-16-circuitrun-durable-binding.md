# CircuitRun Durable Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist an exact CircuitSpec→Mission Work binding in WORKS without creating a second execution state machine.

**Architecture:** Add a small `packages/circuitrun` value package and SQLite persistence in `services/work/store`. CircuitSpec is opaque to WORKS except for canonical JSON/digest validation; Work remains execution truth.

**Tech Stack:** Go, SQLite, existing WORKS store interface/tests.

**Spec:** `docs/superpowers/specs/2026-09-16-circuitrun-durable-binding-design.md`

## Global Constraints
- No API endpoint, worker behavior, Runtime call, deployment, or authority change in this slice.
- Existing Mission Work must exist before CircuitRun creation.
- Exact retries are idempotent; conflicting rebinds fail closed.
- Full `go test ./...` must remain green.

### Task 1: CircuitRun value contract
- Create `packages/circuitrun/circuitrun.go` and `packages/circuitrun/circuitrun_test.go`.
- RED: require canonical JSON equivalence, stable SHA-256, required fields, object-shaped CircuitSpec.
- GREEN: implement canonicalization/digest and validation only.

### Task 2: Durable WORKS persistence
- Create `services/work/store/circuit_run.go` and `services/work/store/circuit_run_test.go`.
- Modify `services/work/store/store.go`: schema v14, `circuit_runs` table, Store methods.
- Modify migration expectation tests from v13→v14.
- RED: existing Mission Work binds; unknown/non-mission Work fails; exact retry is idempotent; conflicting second binding fails closed; Get by ID/WorkID round-trips exact canonical subject.
- GREEN: transactional SQLite implementation using existing Store patterns.

### Task 3: Release gate
- Run focused tests, `go test ./...`, `go vet ./...`, `git diff --check`, secret hygiene.
- Verify remote `main` is still exact baseline before push.
- Commit with sign-off, push new branch normally, open a draft PR against `main`, do not merge or deploy.
