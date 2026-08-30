# Agent Glossary (works-execution)

> Terminology used across agent capability declarations, audit logs, and
> capability manifests. Aligned with ISO/IEC 22989:2022 (AI Concepts and
> Terminology) and the AI standards cluster in `docs/standards/registry.json`.

## Action

A single executable step within a Work graph. Composed of `command`,
`timeout`, `needs`, optional `cache`, `evidence` requirements, and (in
slice 3+) a **capability manifest** declaring CPU, memory, GPU, FS access,
network access, secrets, permissions, side effects, rollback.

## Artifact

A content-addressed output of an Action. Identified by sha256 of its bytes
in V1 (`internal/worker/worker.go::writeArtifact`). Stored on disk under
`<ArtifactsDir>/<workID>/<nodeID>.log` in slice 1+2; planned to move to
object storage (S3-compatible) in slice 4+.

## Attempt

One execution attempt of one Action. Identified by a unique `att_<hex>` id.
Mutable during execution (`status: running`); terminal statuses are
`succeeded`, `failed`, `cancelled`, `timed_out`. Each attempt may carry a
`lease_id` linking it to the lease under which it ran.

## Capability manifest

Structured declaration of an Action's required resources and side effects
(see `platform-action-manifest` #110). Slice 3 ships the JSON Schema.

## Evidence

A structured verification record bound to an attempt. Slice 1+2 emits
`build` evidence per node completion; slice 3 emits per-evidence-type
records (test, typecheck, lint, security_scan, artifact, policy) per
the pack's Evidence Model.

## Evidence bundle

A signed, content-addressed JSON document (or CBOR) collecting all evidence
records, attempt records, artifact manifests, and lease records for a single
Work. Slice 3.

## Lease

A time-bounded authorization for a worker to execute an Action. States:
`ACTIVE → RELEASED | REVOKED | EXPIRED`. See `docs/standards/mappings/platform.md`
§"Runner Identity Standard" for the slice-3 SPIFFE mapping.

## Lease-reaper

Background goroutine in the control plane that detects `EXPIRED` leases
and returns their Actions to the ready queue. Default interval 5s; lost-worker
SLO ≤30s.

## Node

Same as Action in slice 1+2. Reserved as the graph-level noun; "Action" is the
runtime noun. Slice 3 may split these into distinct concepts when the
capability manifest applies to runtime only.

## Readiness

A boolean state for each Action: "all dependencies SUCCEEDED, no active lease,
no in-flight attempt, Work state is QUEUED or RUNNING." Computed by
`packages/workgraph.Work.ReadyNodes`.

## Work

The durable execution object — the source of execution truth (ADR-0001).
States: `CREATED → PLANNING → QUEUED → RUNNING → VERIFYING → SUCCEEDED`, with
side states `BLOCKED`, `FAILED`, `CANCELLED`.

## Worker

The runtime that executes Actions. `cmd/works-worker`. Capability declared in
`docs/agents/worker.md`. NOT a machine-learning agent in V1.