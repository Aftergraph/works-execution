# CircuitVerdict Exact-Subject Implementation Plan

**Goal:** Persist an immutable exact-subject CircuitVerdict without changing Work verification state.

**Architecture:** Extend `packages/circuitrun` with subject/verdict value types and `services/work/store` with schema v15 persistence. Caller never supplies the derived subject or spec digest.

**Spec:** `docs/superpowers/specs/2026-09-16-circuit-verdict-exact-subject-design.md`

## Constraints
- T040a only: CircuitRun/spec subject, not effect binding.
- No API, dispatch, Work-state, deployment, or authority changes.
- Exact retries idempotent; conflicting re-attestation fails closed.

### Task 1
- RED: derive stable subject and validate verdict input/result/timestamp.
- GREEN: add value types/helpers only.

### Task 2
- RED: existing CircuitRun required; store derives digest/subject; exact retry idempotent; conflicts fail closed; round-trip survives persistence.
- GREEN: schema v15 `circuit_verdicts` and Store methods.

### Task 3
- Run focused tests, full `go test ./...`, `go vet ./...`, diff/secret hygiene.
- Push stacked branch and open draft PR against `friday/circuitrun-20260916`; do not merge or deploy.
