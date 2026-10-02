# Agent operating contract — works-execution

This file is the local execution contract for coding agents, bots, reviewers, and orchestrators working in this repo.

## Authority

- ADRs in `docs/adr/` are authoritative for architectural decisions.
- `docs/works-venture-starter-pack/` is the source-of-truth venture plan.
- Velocity track: Normal by default. Fast only for docs/ADRs with no runtime impact.

## Stack

- Language: Go 1.23+.
- State: SQLite (see ADR-0005).
- Worker: local subprocess for V1.
- CLI: Go (`cmd/works`).
- API: Go `net/http` + `chi` router.

## Gate requirements (Normal track)

- `go vet ./...` clean.
- `go build ./...` clean.
- `go test ./...` green, including `e2e/...`.
- All ADRs reviewed and accepted before merge.

## Non-negotiables

- Works cannot be marked SUCCEEDED without authoritative state machine transition AND evidence on disk.
- Workers are disposable; control plane owns state.
- V1 must work without AI.

## Hermes Execution Discipline

This section extends the contract for agents running under Hermes.
Repository-specific rules above always override generic defaults when stricter.

### Persistent Continuation Contract

At the start of every run, recover and revalidate all prior statements about:
unfinished work, risks, failed checks, blockers, deferred decisions, promised
next actions, and assumptions whose validity may have changed. Assign each a
disposition: `open | resolved | blocked | deferred | stale | superseded | invalid_claim`.
Promote every relevant authorized runnable item into the active task graph.

### Execution Requirement

Reading, acknowledging, planning, or summarizing is not execution progress
while an authorized runnable action exists. Each run must produce at least one
of: newly verified external state, an executed tool action, an artifact or code
change, a completed task-graph node, a repaired failure, new gate evidence,
or reversible preparation for a protected action.

### Mode Selection

Choose the primary mode before acting:
`DISCOVER | DESIGN | IMPLEMENT | DEBUG | REVIEW | REFACTOR | HARDEN | MIGRATE | RELEASE | OPERATE`

- **DEBUG**: reproduce root cause before changing code; test one hypothesis at a time.
- **REVIEW**: assess current implementation before editing.
- **REFACTOR**: characterize and preserve existing behavior.
- **MIGRATE**: protect compatibility, integrity, recovery, and partial-failure handling.
- **RELEASE**: verify exact source SHA and exact artifact.

After three materially different failed fixes, stop patch-stacking and reassess.

### Progressive Verification

Use cheapest useful feedback first, then broaden:
`syntax → format → targeted static checks → focused tests → module tests → integration tests → e2e → build → runtime`

Gate evidence must be bound to exact relevant state. Material changes invalidate affected evidence.

### Git and CI Discipline

- Work on a focused branch.
- Inspect complete diff and check for secrets before commit.
- Update existing PR rather than duplicating.
- Run `make vet && make test && make e2e` (Normal track) before merge.
- Merge only when gates pass, blocking reviews resolved, and no security risk remains.

### Completion Verification

An agent may propose `complete_candidate`; it may not declare final completion without verifying:
outcome exists, acceptance criteria pass, required gates pass, evidence is current,
exact resulting state is known, no critical continuation item remains open,
and post-delivery checks pass where applicable.
