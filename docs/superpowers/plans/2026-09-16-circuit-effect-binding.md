# Circuit Effect Binding Implementation Plan

> Execute test-first in the isolated `friday/circuiteffect-20260916` worktree.

**Goal:** Persist an explicit CircuitRun→dispatch effect identity seam without modifying the frozen dispatch acceptance contract.

## Constraints

- No API, Runtime integration, production deploy, merge, or frozen contract change.
- Do not infer Work identity from dispatch `attempt_id`.
- Caller provides only CircuitRunID + WorksExecutionID; all effect identity is derived from durable records.
- Support multiple effects per CircuitRun.

### Task 1: value contract
- Add `EffectBindingInput` and `EffectBinding` in `packages/circuitrun`.
- Validate only supplied IDs; caller cannot supply effect/mission/work provenance.
### Task 2: durable binding
- Bump SQLite v15→v16 and add `circuit_effect_bindings`.
- Add create/get/list Store methods.
- Transactionally load CircuitRun and dispatch acceptance.
- Require MissionID match and complete dispatch identity.
- Hash the frozen Dispatch struct and persist the derived binding.

### Task 3: falsification
- Unknown records fail closed.
- Mission mismatch fails closed.
- Exact retry is idempotent; conflicting rebinding fails.
- Direct dispatch identity tamper is detected on read.
- Multiple distinct effects on one CircuitRun remain legal.
- Restart round-trip preserves exact identity.

### Task 4: release gate
- Run focused tests, full `go test ./...`, `go vet ./...`, diff/secret hygiene.
- Push as a stacked draft PR on PR #96 only; do not merge or deploy.
