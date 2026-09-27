# ADR-0028: Verification lifecycle, typed invalidation, and resume reconciliation

**Status:** Accepted  
**Date:** 2026-09-27  
**Deciders:** Aftergraph maintainers  
**Owner:** WORKS execution truth; cross-system contract promotion remains governance-owned

## Context

WORKS already provides durable execution, leases, checkpoints, evidence, exact-subject binding, independent verification ingest, and fail-closed dispatch acceptance. The existing system can answer whether a terminal Work received an independent verifier verdict.

Long-running autonomous execution introduces a different problem:

> A result that was verified at time T may stop being safe to treat as canonical after its dependencies or observed environment change.

A durable checkpoint answers "where were we?" but does not by itself answer "is what we believed still true?". Blindly resuming from a checkpoint can therefore preserve stale assumptions, duplicate work, or let a formerly valid downstream result remain accepted after its upstream basis changed.

The system needs three additional semantics:

1. A verification lifecycle richer than a permanent PASS/FAIL bit.
2. Typed dependency invalidation so only affected downstream subjects are revalidated.
3. Resume reconciliation that compares checkpointed observations with the live world before mutation resumes.

These semantics must not move execution truth into STEWARD, an agent, or a model. WORKS remains the durable execution owner. Sentinel remains the independent verifier. AIE/Trust Gateway remain authority/policy owners. Runtime remains lifecycle/orchestration owner.

## Decision

Introduce a pure WORKS verification-state primitive with the following states:

```text
UNKNOWN
  -> CANDIDATE
      -> VERIFIED
      -> REJECTED

VERIFIED
  -> STALE
  -> INVALIDATED

STALE
  -> CANDIDATE
  -> INVALIDATED

INVALIDATED
  -> CANDIDATE

REJECTED
  -> CANDIDATE
```

### Canonicalization invariant

A consequential subject may be treated as canonical only while:

- state is `VERIFIED`;
- an exact fingerprint is present; and
- a non-empty verification reference is present.

`VERIFIED` is reachable only through an explicit verification operation carrying the verifier identity, evidence reference, exact subject reference, and verification timestamp.

The primitive does not implement or weaken independent verification. It references verification truth already accepted by the existing owner boundary.

### Typed dependencies

The first version defines these edge types:

| Edge | Meaning | Change propagation |
|---|---|---|
| `DATA` | downstream consumed upstream data | STALE |
| `EFFECT` | downstream depends on a consequential effect | INVALIDATED |
| `RESOURCE` | downstream depends on a resource observation | STALE |
| `AUTHORITY` | downstream depends on authority validity | INVALIDATED |
| `TEMPORAL` | downstream depends on time/freshness | STALE |
| `VERIFICATION` | downstream depends on upstream proof | INVALIDATED |
| `JOIN` | downstream joins upstream outcomes | STALE |
| `EVENT` | downstream depends on an external event | STALE |

Once an invalidation reaches a path, `INVALIDATED` dominates all downstream propagation, including across otherwise-soft edges.

The graph may contain cycles. Propagation is monotone over the ordered severity set:

```text
unchanged < STALE < INVALIDATED
```

so reconciliation terminates at a fixed point.

### Resume reconciliation

Before a long-running mission resumes consequential mutation, the controller must:

```text
load durable checkpoint
        ↓
observe current world
        ↓
compare fingerprints
        ↓
changed / missing / added
        ↓
typed dependency propagation
        ↓
mark STALE / INVALIDATED
        ↓
selective revalidation
        ↓
continue only from canonical state
```

Changed or missing checkpoint subjects are roots of STALE impact. Added observations are recorded but do not invalidate existing proof unless a declared dependency gives them semantic relevance.

Critically, reconciliation does **not** overwrite the canonical fingerprint with an unverified live observation. Observation and canonical truth remain separate.

## Initial implementation slice

`packages/verifiedstate` contains the pure semantics only:

- lifecycle enforcement;
- proof-required `VERIFIED` transition;
- canonicalization gate;
- eight typed dependency edges;
- deterministic invalidation propagation;
- checkpoint/current snapshot diff;
- cycle-safe reconciliation;
- minimal selective revalidation impact set.

This slice intentionally has no SQLite migration, HTTP endpoint, model call, scheduler mutation, or cross-repo ownership change.

That keeps the first proof falsifiable and lets existing `go test ./...` gates validate the semantics before durable integration.

## Integration sequence

After this ADR is accepted and the primitive is green:

1. **WORKS persistence** — store verification state/fingerprint/proof references durably, transactionally bound to existing execution identity.
2. **Runtime resume seam** — require a reconciliation result before post-resume consequential dispatch.
3. **Sentinel binding** — map accepted independent verdicts to `Verify`; verifier failure must never create `VERIFIED`.
4. **AIE / Trust Gateway binding** — authority revocation or decision freshness changes emit hard `AUTHORITY` invalidation roots.
5. **STEWARD composition** — consume state/impact as orchestration input; STEWARD does not become execution or verification truth.
6. **Governance promotion** — only governance may freeze a cross-system contract/schema once owner integrations are proved.

## Consequences

**Positive**

- A checkpoint can no longer silently preserve stale truth.
- Reverification is selective rather than a full mission replay.
- Authority and verification changes propagate harder than ordinary data freshness changes.
- Models/workers remain disposable; state truth remains external.
- The same primitive can support long-running software development, monitoring, research, and operations.

**Negative**

- Every dependency that matters must eventually be represented explicitly.
- Incorrect edge typing can under- or over-invalidate.
- Persisted integration requires a schema migration and migration/backfill policy.
- Cross-repo event sources need stable fingerprints and freshness semantics.

## Falsification criteria

Reject or revise this design if tests show any of the following:

1. A STALE or INVALIDATED subject can pass the canonicalization gate.
2. A VERIFIED state can be created without an accepted proof reference.
3. A hard dependency change leaves its downstream subject merely VERIFIED or STALE.
4. An unrelated graph branch is invalidated by a change with no dependency path.
5. A cyclic graph causes non-termination.
6. Resume reconciliation replaces canonical fingerprints with unverified observations.
7. The owner integration requires STEWARD or an LLM to become canonical execution truth.

## Acceptance criteria for this slice

- `go vet ./...` clean.
- `go build ./...` clean.
- `go test ./...` green.
- Unit tests prove canonicalization gating, selective propagation, hard invalidation dominance, missing/added reconciliation, cycle termination, and silent-overwrite refusal.
- No existing frozen contract changes.
- No API behavior changes.
- No persistent schema changes.

## Acceptance evidence

Accepted on 2026-09-27 after the branch passed the repository's normal gates on commit `3f61b4fc348261e8046729137286072fa8fabc36`: `go build ./...`, `go vet ./...`, `go test ./...`, the integrity before/after benchmark, and CodeQL all completed successfully. The slice changes no existing API, frozen contract, or persistent schema.

## Rollback

Delete `packages/verifiedstate` and this ADR. No existing API, database, frozen contract, or execution path depends on this initial slice.

## Security impact

Positive but not complete. Authority invalidation becomes a first-class hard dependency in the semantic model, which prevents previously verified downstream state from remaining canonical after an authority basis changes. Enforcement is not security-relevant until the primitive is integrated with durable WORKS state and the live AIE/Trust Gateway boundary.
