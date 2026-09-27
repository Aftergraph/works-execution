# ADR-0029: Verification-aware resume reconciliation boundary

**Status:** Proposed  
**Date:** 2026-09-27  
**Deciders:** Aftergraph maintainers  
**Owner:** WORKS resume state transition; Runtime/external adapters own live-world observation

## Context

ADR-0028 introduced the pure verification lifecycle and typed dependency
invalidation law. The next failure mode is at resume:

```text
durable checkpoint
    ↓
time passes / worker dies / runtime restarts
    ↓
external world changes
    ↓
blind resume
```

A cryptographically intact handoff only proves what WORKS persisted. It does
not prove that repository heads, authority leases, resources, external events,
or other mutable dependencies still match the state that was verified before
suspension.

The existing governed resume path already binds:
- bridge authorization;
- approval receipt;
- principal and tenant;
- exact persisted checkpoint hash;
- idempotency;
- mission state.

Those controls must remain intact. Reconciliation is an additional gate, not a
replacement.

## Decision

A verification-aware handoff stores a versioned checkpoint under the reserved
`handoff.state_snapshot` key:

```text
aftergraph_verified_state
```

with schema:

```text
verified-state-checkpoint/0.1
```

This preserves the frozen `handoff.schema/1.0` top-level contract.

The embedded checkpoint contains:
- canonical VERIFIED nodes and proof references;
- typed dependency edges;
- the subset of canonical fingerprints that must be re-observed before resume.

### Anti-bypass law

The historical `ResumeFromCheckpoint` path remains available for legacy
handoffs. If the reserved verification checkpoint exists, that path returns
`ErrReconciliationRequired` and does not mutate the Work.

Only `ResumeFromCheckpointReconciled` may resume a verification-aware
checkpoint.

### Trusted-observer law

Live fingerprints are supplied by a control-plane `ResumeWorldObserver`, not
by the worker/model and not by an untrusted request field.

If no observer is wired, a verification-aware HTTP resume returns unavailable
and remains paused. Legacy handoffs remain backward compatible.

### Exact-checkpoint binding

Observation is performed against the checkpoint the caller just read. Before
the RUNNING transition, WORKS re-reads the latest handoff and requires its
payload hash to equal the expected hash. A concurrently replaced checkpoint
therefore fails closed.

### Reconciliation law

```text
persisted verified checkpoint
        ↓
trusted fresh observation
        ↓
typed reconcile
        ↓
no impact ───────────────→ RUNNING
        │
        └─ STALE/INVALIDATED → remain paused
                               selective revalidation required
```

A live observation never overwrites a canonical fingerprint.

## Ownership

- **WORKS** owns checkpoint integrity and the RUNNING transition.
- **Runtime / trusted adapters** own external observation.
- **Sentinel** remains the independent verifier.
- **AIE / Trust Gateway** remain authority/policy truth sources.
- **STEWARD** may consume reconciliation outcomes for replanning but cannot
  assert that reconciliation succeeded.

## Compatibility

Legacy handoffs without `aftergraph_verified_state` retain the existing
resume behavior.

No frozen contract changes. No database schema change. The versioned envelope
is contained within the already-generic `state_snapshot` field.

## Initial implementation slice

- versioned verified-state checkpoint envelope;
- canonical proof/fingerprint validation at checkpoint construction/read;
- generic handoff attachment/decoding;
- store-level blind-resume refusal for verification-aware handoffs;
- exact checkpoint-hash binding on reconciled resume;
- API `ResumeWorldObserver` hook;
- fail-closed behavior for missing/failed observation;
- typed reconciliation before RUNNING;
- legacy resume compatibility.

## Falsification criteria

Reject or revise this design if any test demonstrates:

1. A verification-aware handoff resumes through legacy blind resume.
2. Missing observer state is treated as success.
3. Changed monitored state transitions the Work to RUNNING.
4. A hard dependency change fails to invalidate its downstream outcome.
5. A changed checkpoint can reuse observations made for the old checkpoint.
6. Live observations overwrite canonical verified fingerprints.
7. A legacy handoff is broken merely because verification-aware resume exists.
8. Worker/model output can directly claim trusted current-world state.

## Acceptance criteria

- `go build ./...` clean.
- `go vet ./...` clean.
- `go test ./...` green.
- integrity before/after benchmark green.
- CodeQL green.
- direct store bypass test fails closed.
- unchanged trusted observation resumes successfully.
- changed DATA dependency blocks resume.
- changed AUTHORITY dependency hard-invalidates downstream.
- wrong checkpoint hash blocks resume.
- observation failure blocks resume.
- existing resume tests remain green.

## Follow-up

This slice deliberately stops before automatic selective revalidation.

The next integration should:
1. persist reconciliation outcomes/evidence;
2. let Runtime route impacted subjects to appropriate verifiers;
3. re-check authority through AIE/Trust Gateway;
4. create a fresh verified checkpoint only after revalidation;
5. measure avoided reruns, false-resume rate, recovery latency, and
   cost-per-verified-outcome.

## Rollback

Remove the verification-aware resume files/hooks and ADR-0029. Legacy
`handoff.schema/1.0`, storage, and resume semantics remain intact.

## Security impact

Positive. A previously verified result can no longer be resumed merely because
its handoff bytes are intact. Verification-aware work requires fresh
control-plane observation and exact checkpoint binding before the execution
state can return to RUNNING.
