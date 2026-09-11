# Durable Mission Runner Design

**Status:** Approved for implementation by operator request `Execute` on 2026-09-10.

## Problem

ChatGPT and connector tool turns are finite. Long Aftergraph work must therefore not depend on one coordinator turn remaining alive. WORKS already owns durable Work state, leases, worker-loss recovery, evidence, and mission checkpoints; this feature exposes those primitives as a safe handoff surface for agentic coordinators.

## Ownership

WORKS remains canonical for durable execution. Runtime/Hermes may submit and observe missions but cannot become the source of truth. Trust Gateway/AIE remain authoritative for admission and authority. An executor completion is not an independent verification verdict.

## Design

Add a `missionhandoff` compiler and `works mission run` CLI surface. A mission YAML compiles to one mission `workgraph.Work` whose stage DAG is executed by ordinary disposable WORKS workers. Submission is detached by default so the calling ChatGPT/Hermes session may disappear immediately after receiving the Work ID.

Each stage may declare a read-only `reconcile` command. Before a consequential `run`, the worker executes the generated reconciliation wrapper: exit 0 means the desired external state already exists and the mutation is skipped; exit 1 means it is proven absent and the mutation may execute; any other exit is indeterminate and fails closed without replaying the mutation. This implements `observe reality, then continue` after uncertain timeouts or worker loss.

## Mission configuration

Required: stable `mission_id`, non-empty objective, at least one stage, purpose bindings, budget ceiling, and at least one verification criterion. Stage names become WorkGraph node IDs. Dependencies use `needs`. Existing Node permissions, side effects and timeouts remain authoritative.

## Recovery and containment

Worker death, connector timeout, process loss and coordinator disconnect are continuity faults. Lease expiry makes the stage eligible for another worker; reconciliation runs before the mutation on the replacement attempt. Revocation and budget exhaustion remain containment states and must never auto-resume.

Mission identity is also the idempotency key. The compiler derives a stable Work ID from `mission_id` and binds a canonical spec fingerprint into the objective. Re-submitting the same mission reconciles to the existing Work; reusing a mission ID with a changed spec fails closed.

## Verification semantics

Stage success means execution completed, not that the mission is independently verified. The mission contract carries deterministic or human-review criteria. Existing WORKS evidence remains append-only. No new code may turn an executor's prose assertion into independent verification.

## Operator interface

`works mission run --config mission.yaml [--api URL] [--follow]` submits the durable mission. Without `--follow`, it prints the Work ID and exits. `--follow` is convenience only and must not affect execution lifetime. `works status <work_id>` remains the canonical observer.

## Acceptance

The implementation must prove deterministic config compilation, fail-closed reconcile semantics, stable idempotency identity, detached submission, worker-loss recovery compatibility, no plaintext secrets persisted by the compiler, and full repository tests/build gates. A live VDS mission should continue after the submitting client exits and survive worker replacement without duplicating a consequential side effect.
