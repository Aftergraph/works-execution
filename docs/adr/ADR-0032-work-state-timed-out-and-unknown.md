# ADR-0032: TIMED_OUT and UNKNOWN added to the frozen Work state vocabulary

**Status:** Accepted
**Date:** 2026-10-02
**Deciders:** Owner (explicit authorization), implemented by Aftergraph Builder Max
**Supersedes:** nothing. **Amends:** the `work.schema/1.0` state enum; strengthens `packages/workgraph/mission_state_vocab_test.go`

## Context

The `workgraph.State` vocabulary was frozen at twelve values and guarded by
`TestStateVocabularyFrozen`. Two gaps in that vocabulary were found while
building the Runtime-side execution port:

**Gap 1 — no timeout terminal.** `IsTerminal()` covers exactly
`{SUCCEEDED, FAILED, CANCELLED}`. The PRD lifecycle has `TIMED_OUT`. The
only available projection was `TIMED_OUT -> FAILED`, and that is lossy in
a way that does real damage: it makes a wall-clock kill indistinguishable
from a genuine test failure at exactly the layer the recovery supervisor
reads to decide whether to retry.

The concept was not absent from the codebase — only from this one enum.
The attempt layer has carried it since slice 2:

- `packages/workgraph/workgraph.go` documents `timed_out` on `Attempt.Status`.
- `internal/worker/worker.go` sets `res.Status = "timed_out"` when
  `exec.CommandContext` exceeds the per-node timeout.
- `services/classifier/classifier.go` routes `status_timed_out` to
  `ClassInfrastructureFail`, and folds it alongside `"failed"` in its
  terminal-set check.

So the failure classifier already knew the distinction, and the Work-level
state machine could not express it. Every consumer had to re-derive the
answer by joining back to attempt rows.

**Gap 2 — no indeterminate state.** The PRD lifecycle has `UNKNOWN` and
the vocabulary has no counterpart. The two nearby candidates are both
wrong:

- `SUSPENDED` is documented as "the hard-stop/checkpoint state" — a
  deliberate pause. Projecting UNKNOWN onto it asserts a stop that never
  happened, and `SuspendWork` additionally requires a checkpoint handoff
  (ADR-0010), so an UNKNOWN run with no handoff cannot even be recorded.
- `BLOCKED` reads as "cannot proceed right now", which is a scheduling
  condition. An UNKNOWN run may have already completed its work; BLOCKED
  invites a retry that would duplicate side effects.

## Decision

Add two states, owner-authorized and deliberate. **The vocabulary is 14.**

### `StateTimedOut` = `"TIMED_OUT"` — terminal

Reachable exactly where `FAILED` is reachable: from every non-terminal
state (`CREATED`, `PLANNING`, `QUEUED`, `RUNNING`, `VERIFYING`,
`WAITING_HUMAN`, `SUSPENDED`, `BUDGET_EXHAUSTED`). A wall-clock kill can
end a Work at any stage that was still running, so its reachability mirrors
`FAILED` rather than inventing a narrower rule.

Terminal, like `FAILED`: the run is over and will not progress on its own.
Like `FAILED`, it has no forward path.

Not a mission-only state — a CI Work can hit its wall clock without
carrying a mission contract.

### `StateUnknown` = `"UNKNOWN"` — NOT terminal

Reachable from `RUNNING` only. That is deliberate: UNKNOWN is how the
control plane records losing track of a run. The ambiguity arises while a
Work is executing, not while it is queued or verifying.

Resolvable outward to `FAILED`, `CANCELLED`, and `SUSPENDED` — decisions,
not progress. Resolution to `SUSPENDED` additionally passes the existing
ADR-0008 mission-only freeze law (a CI Work must supply a mission contract,
and `SuspendWork` requires a handoff).

**There is no `UNKNOWN -> RUNNING` edge.** This is the anti-self-resume law
for this state. UNKNOWN means the runtime does not know whether the node's
work already happened; an edge back to RUNNING would let it re-run blind.
Only a human or the recovery supervisor resolves it.

**UNKNOWN is not terminal, and that is the load-bearing half of the
decision.** `CanTransition` consults `IsTerminal()` first, so a terminal
UNKNOWN would make every one of those outward resolutions impossible and
strand the Work permanently. The entire point is that recovery must still
be able to resolve it. A permanent ambiguity is the opposite of the
intent.

### Enforcement in the store

`GrantLease` refuses `UNKNOWN` with `ErrLeaseConflict`, extending the
k-mission-02 anti-self-resume law that already covers `WAITING_HUMAN`,
`SUSPENDED`, and `BUDGET_EXHAUSTED`. UNKNOWN is not a pause, but it is the
same hazard in a sharper form: leasing it hands the node straight back to
the runtime for a blind retry. This strengthens existing authorization
rather than relaxing it, and it is covered by
`TestGrantLease_RefusesUnknownWork`.

### Wire schema

`TIMED_OUT` and `UNKNOWN` are added to the `state` enum in
`contracts/schemas/work.schema.schema.json`. A State addition **is** a
wire-shape change; the freeze test's own header comment says so. Leaving
the schema at twelve would have made the new states unserializable on a
surface that validates against it.

### Deliberately NOT in this slice

`CompleteLease` does **not** auto-project `TIMED_OUT`. The worker reports
`timed_out` at the attempt layer and `CompleteLease` currently folds any
non-zero exit code to `succeeded`/`failed`. Auto-projecting would change
finalization semantics and the state machine for every Work, which is
beyond what a vocabulary addition authorizes. `TIMED_OUT` is reachable
through the ordinary transition API; the `CompleteLease` projection is a
deliberate follow-up, recorded here so it is not mistaken for an oversight.

## The freeze guard was structurally incapable of catching this

`TestStateVocabularyFrozen` claimed to forbid silent drift and could not
detect it. It built a local `want` map of twelve names, asserted
`len(want) != 12` — a self-check on its own literal — and then validated
`IsTerminal()` only for entries in that local map. It never consulted the
real enum. Adding a thirteenth state would have left it green.

Replacing that list with a fourteen-entry one would be the same bug one
level down, because **Go cannot enumerate typed string constants at
runtime** — there is no reflection story for a `type State string` const
block. A membership check against a hand-written list therefore fails in
both directions: declare a const *and* append it and every membership check
passes; declare a const *alone* and the test never sees it.

The guard is now two tests, and adding a state requires editing **both**
the constant block and `AllStates()`:

1. **`AllStates()`** — an authoritative, ordered, 14-entry set living next
   to the constants. It returns a fresh slice per call so no consumer can
   mutate the canonical set (`TestAllStatesReturnsDefensiveCopy`).
   `TestStateVocabularyFrozen` asserts exact set membership, distinctness,
   `len == 14`, the exact terminal set `{SUCCEEDED, FAILED, CANCELLED,
   TIMED_OUT}`, and that UNKNOWN is explicitly non-terminal.

2. **`TestStateConstantsAreRegistered`**
   (`packages/workgraph/workgraph_state_ast_test.go`, in-package) — parses
   every non-test `.go` file in the package with stdlib `go/parser` +
   `go/ast`, collects every constant explicitly typed as `State`, and
   requires each one to be a member of `AllStates()`; it also checks the
   mirror image, that no `AllStates()` entry lacks a constant.

   **This is the part that cannot be bypassed.** Declaring a new `State`
   constant without registering it fails this test, because the AST sees
   the declaration. Stdlib only; no new dependency.

A guard that cannot fail is not a guard. Both directions are now closed.

## Consequences

**Positive**

- The recovery supervisor can tell an infrastructure timeout from a
  genuine failure without joining back to attempt rows.
- An indeterminate run is representable instead of being forced into a
  state that lies about it.
- The vocabulary guard actually guards.

**Negative**

- Fourteen states is more surface to map across repos. The cross-repo
  mapping to `mission-state/1.0` remains an owner decision (Wave 4); this
  ADR does not change it.
- `TIMED_OUT` is terminal, so a Work that times out cannot be resumed even
  if infrastructure recovers. That is intended — recovery is a new attempt,
  not a resumption of a dead one.
- The in-package AST test couples the test suite to source layout. If the
  package is split across directories, extend the file scan; do not delete
  the guard.

**Neutral**

- Existing transition behaviour is unchanged. `UNKNOWN` has no outgoing
  edge that existed before, `TIMED_OUT` adds edges only from states that
  already permitted `FAILED`, and no existing value was renamed.

## Cross-repo coordination

The Runtime-side execution port mirrors this 14-state set, with
`TIMED_OUT` terminal and `UNKNOWN` non-terminal. The Go enum, the wire
schema, and the freeze guards are owned by this repo.

## Security impact

None. `UNKNOWN` is added to the set of states from which the runtime cannot
self-resume, which is strictly more restrictive. No authorization,
authentication, or lease-conflict behaviour is relaxed.