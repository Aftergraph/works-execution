# ADR-0030: Cross-process resume observation protocol

**Status:** Proposed  
**Date:** 2026-09-27  
**Deciders:** Aftergraph maintainers  
**Owner:** WORKS reconciliation/transition; Runtime observation execution

## Context

ADR-0029 added a trusted in-process `ResumeWorldObserver` seam. That is useful
for tests and co-located adapters but it is not the canonical production shape:
Aftergraph Runtime is TypeScript and WORKS is Go, and their ownership boundary
is process/repository independent.

A production protocol therefore needs to preserve all of these laws:

- Runtime owns orchestration and external observation.
- WORKS owns durable checkpoint truth and the RUNNING transition.
- workers/models cannot self-assert fresh world state.
- the observation must bind to the exact handoff it was made for.
- stale observations must not be reusable indefinitely.
- Runtime should learn *what to observe* without receiving prior canonical
  fingerprints that encourage echo/self-confirmation.

## Decision

Introduce a backward-compatible observed checkpoint envelope:

```text
verified-state-checkpoint/0.2
```

The existing `verified-state-checkpoint/0.1` remains readable.

### Observation bindings

Every monitored snapshot root in v0.2 must have exactly one binding:

```json
{
  "node_id": "repo-head",
  "kind": "GIT_REF",
  "locator": "github:Aftergraph/runtime#refs/heads/main"
}
```

Initial observation kinds:

- `GIT_REF`
- `AUTHORITY`
- `RESOURCE`
- `EVENT`
- `TEMPORAL`

A binding describes observation routing only. It grants no authority.

### Handoff discovery

The bridge-authenticated:

```text
GET /v1/works/{id}/handoff
```

continues returning the exact checkpoint hash and additionally reports:

- whether the checkpoint is verification-aware;
- for v0.2, a `resume.observation-plan/1.0` plan containing only observation
  bindings.

The observation plan does **not** expose prior canonical fingerprints or proof
objects. Runtime is instructed what to observe, not what answer to reproduce.

### Resume observation

Runtime may include:

```json
{
  "observation": {
    "schema": "resume.observation/1.0",
    "observer_id": "runtime:runtime-host",
    "checkpoint_hash": "<exact handoff hash>",
    "observed_at": "<RFC3339>",
    "snapshot": {
      "repo-head": "<fresh fingerprint>"
    }
  }
}
```

inside the already bridge-authenticated resume request.

The observation:

- is authenticated by the existing platform bridge + bearer boundary;
- must identify a Runtime observer;
- must bind the exact requested checkpoint hash;
- must be fresh (maximum age: 2 minutes);
- may not be more than 30 seconds in the future;
- must contain non-empty subject IDs and fingerprints;
- becomes reconciliation input only, never canonical state.

The full observation is included in the idempotency payload binding.

### Selection law

For verification-aware checkpoints, WORKS selects current-world input in this
order:

1. bridge-authenticated `resume.observation/1.0`, when supplied and the
   checkpoint is v0.2;
2. configured in-process `ResumeWorldObserver`, for compatibility/co-location;
3. otherwise fail closed.

Legacy non-verification handoffs reject unexpected observation payloads.

### Reconciliation law

The existing ADR-0029 store gate remains authoritative:

```text
authenticated observation
        ↓
checkpoint-hash binding
        ↓
typed reconciliation
        ↓
no impacted subjects → RUNNING
impact detected       → remain paused
```

The HTTP bridge never directly sets VERIFIED/STALE/INVALIDATED state.

## Security properties

- The worker/model does not possess the platform bridge credential.
- A stale observation is time-bounded.
- An observation for checkpoint A cannot authorize checkpoint B.
- Prior expected fingerprints are redacted from the observation plan.
- A changed observation only invalidates/blocks; it cannot directly establish
  new canonical truth.
- The existing approval receipt, principal, tenant, bearer, bridge secret,
  idempotency, mission-state and checkpoint-hash checks remain mandatory.

## Compatibility

- `verified-state-checkpoint/0.1` remains valid.
- legacy `handoff.schema/1.0` is unchanged.
- legacy handoff GET clients may ignore the additive response fields.
- legacy resume bodies remain valid for legacy checkpoints.
- no database migration.

## Falsification criteria

Reject or revise this protocol if:

1. v0.2 allows a monitored subject with no observation binding.
2. an observation binding can silently target an unmonitored subject.
3. GET handoff leaks prior canonical fingerprints through the observation plan.
4. a bridge observation can be used against another checkpoint hash.
5. an observation older than the freshness bound resumes work.
6. a changed observed fingerprint resumes work.
7. a legacy checkpoint accepts an unexpected cross-process observation.
8. the observation itself can directly create VERIFIED canonical state.
9. existing v0.1/legacy resume tests regress.

## Acceptance criteria

- `go build ./...` clean.
- `go vet ./...` clean.
- `go test ./...` green.
- integrity before/after benchmark green.
- CodeQL green.
- v0.1 checkpoint compatibility retained.
- v0.2 exact binding coverage.
- handoff discovery exposes observation plan.
- fresh unchanged bridge observation can resume with no local observer.
- changed, stale, or wrong-checkpoint observation blocks resume.
- legacy resume rejects unexpected observation.

## Next integration

Runtime should implement `resume.observation-plan/1.0` as a model-free
control-plane service:

```text
GET handoff
  → resolve typed observation adapters
  → observe all required roots
  → POST resume with observation
```

Provider adapters (Git ref, AIE authority, resource/event sources) remain
separate from the generic protocol.

## Rollback

Remove v0.2 construction, additive handoff-view fields, and bridge observation
handling. v0.1 + ADR-0029 in-process observer behavior remains intact.
