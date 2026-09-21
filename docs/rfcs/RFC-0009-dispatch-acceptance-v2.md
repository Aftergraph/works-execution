# RFC-0009: Runtime → WORKS Contextual Dispatch Acceptance V2

Status: PROPOSED · Owner: works-kernel · Tracks: Aftergraph/after-graph-governance#185 and STEWARD P2.

## Problem

The frozen `dispatch.acceptance/1.0` is not a sufficient carrier for Platform V2.1:

1. it requires `authority_epoch`, but Platform V2.1 has no canonical AuthorityLease epoch; and
2. it mints correlation IDs without materializing the full WORKS-owned
   `execution-context/1.0` object that Trust Gateway later resolves.

The 1.0 surface remains compatibility history and stays fail-closed in
production while its epoch resolver is intentionally absent.

## V2 decision

`dispatch.acceptance/2.0` is additive and removes `authority_epoch` from the
new request. It carries the canonical identity/correlation references needed to
materialize the real execution context:

- organization / tenant / principal;
- Mission;
- AuthorityLease reference;
- Work route scope;
- WorkerLease reference;
- initial admission PDR;
- Runtime dispatch/effect/idempotency/checkpoint/evidence references.

The final verification subject is intentionally absent from initial acceptance because it does not exist until the governed effect produces an immutable candidate.

WORKS validates that the WorkerLease is active, unexpired and belongs to the
route-scoped Work, mints `ctx_*` + `trc_*`, and commits the acceptance plus
`execution-context/1.0` in one SQLite transaction.

After the effect, Runtime binds the observed immutable subject through
`dispatch.verification-subject/1.0`. The first binding wins; replaying the same
subject is idempotent; a different subject for the same accepted execution is a
conflict. For coding work the canonical subject is `git:<owner>/<repo>@<40hex>`.

## Authority boundary

V2 acceptance is **not** effect authorization.

```text
Runtime dispatch
  ↓
WORKS acceptance + execution-context
  ↓
proposed consequential action
  ↓
Trust Gateway
  ↓
AIE live revalidation
  ↓
execution-phase PDR
  ↓
effect
  ↓
observed immutable candidate
  ↓
Runtime → WORKS exact-subject binding
  ↓
independent verification
```

WORKS does not infer authority from Project, Work, WorkerLease, execution
context, repository membership or possession of an AuthorityLease reference.

## Replay law

A committed V2 acceptance is recoverable by idempotency key even if the
WorkerLease later expires. That is readback of an already committed durable
decision, not a new authorization.

A fresh V2 acceptance requires a current active WorkerLease.

## Failure laws

- unknown/foreign/expired WorkerLease → fail closed;
- client-supplied ctx/trc/authority_epoch/final verification subject at initial acceptance → rejected by strict HTTP decoding;
- same idempotency key + different causal identity → fail closed;
- acceptance insertion without context insertion → impossible by transaction;
- context insertion without acceptance insertion → impossible by transaction;
- verdict before post-effect subject binding → fail closed;
- different-subject rebind → fail closed;
- SUCCEEDED still does not imply VERIFIED.

## Freeze boundary

The schema lives under `contracts/proposals/` while governance issue #185 is
open. It MUST NOT be added to the frozen manifest or described as a released
contract until cross-repo review approves the major-version transition.
