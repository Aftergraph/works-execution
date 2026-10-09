# Durable Mission Runner Implementation Plan

> **For agentic workers:** use an isolated worktree and execute task-by-task with TDD and verification gates.

**Goal:** Make long Aftergraph missions survive ChatGPT/tool-session termination by handing execution to WORKS durable state and disposable workers.

**Architecture:** Compile a purpose-bound mission YAML into the existing `workgraph.Work` mission contract. Generate per-stage fail-closed reconciliation wrappers, then submit through the existing WORKS API; the caller may detach immediately while leases, reaper, artifacts and evidence continue server-side.

**Tech Stack:** Go 1.23+, YAML v3, existing WORKS HTTP API, SQLite leases/evidence.

**Spec:** `docs/superpowers/specs/2026-09-10-durable-mission-runner-design.md`

## Global Constraints

- WORKS is canonical durable execution state.
- Do not widen authority or bypass existing admission/policy checks.
- Revocation and budget exhaustion are containment, never automatic recovery.
- Reconciliation is read-only and must fail closed on indeterminate state.
- Mission stage completion is not independent verification.
- No credentials or secret values may be persisted in mission config output.

## Task 1: Mission compiler

Create `packages/missionhandoff` with strict parsing, stable mission/work identity, mission-contract validation and fail-closed reconciliation wrappers. Test missing fields, deterministic fingerprints, DAG dependencies, secret-reference enforcement, duplicate-side-effect prevention and indeterminate reconciliation.

## Task 2: Detached CLI handoff

Expose `works mission run --config PATH --api URL [--follow]`. Default returns immediately after durable creation. Before creating, reconcile deterministic Work identity against the control plane: identical spec means reuse; same mission ID with a changed spec fails closed.

## Task 3: Verification and live recovery proof

Run `gofmt`, `go test ./...`, `go vet ./...`, `go build ./...`, secret scan and diff checks. Build binaries from the exact branch SHA. Submit a detached VDS mission and verify it continues independently. Kill an executing worker, let its lease expire, start a replacement worker and prove reconciliation prevents duplicate consequential effects. Then perform independent review, PR/CI and merge only after green gates.
