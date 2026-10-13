# WORKS · Fenced effect budget holds (G18 / Genesis #2095)

This adds a **real transactional budget hold** in the canonical WORKS SQLite database, associated with the frozen V2 dispatch acceptance, WORKS-minted execution context and live worker lease. It is not an alternative ledger or tool execution engine.

## Atomic behavior

`HoldFencedEffect` begins a single SQLite transaction and reads the committed `dispatch_acceptances` record, exact execution-context identity, active/unexpired worker lease and already-held budget. It inserts a unique `(works_execution_id,effect_id)` reservation only when ceiling minus BudgetSpent minus existing holds can cover the amount. Request idempotency keys are immutable; equal replays recover the existing row but never receive fresh execution permission. Mismatched replay, changed worker, revoked acceptance, expired lease, invalid amount or budget exhaustion fail closed.

`SealFencedEffect` atomically transitions `held` to `reconcile` once. The transition occurs BEFORE an externally visible operation. Crash or ambiguous provider results remain `reconcile`, never automatically replayed. A provider with idempotency support and verified outcome/compensation is mandatory.

## Critical authority boundary

These functions reserve WORKS budget and effect identity; they DO NOT authorize side effects. Trust Gateway/AIE must revalidate live `action_id`, authority lease, tenant/mission/principal, allowed capability and exact effect digest at the action boundary. An authority-ref, user input, model claim or caller-supplied numeric epoch is never a substitute. See WORKS #106. There is **no HTTP route or dispatcher wiring** in this PR, so this cannot accidentally activate autonomous production operations.

Current WORKS state is local SQLite; shared budget across multi-host services requires routing every hold/spend through the same canonical WORKS database and authoritative Trust revalidation. The Trust observation store on Lume #2108 cannot be interpreted as budget escrow, one-shot execution permission or signed remote proof.

## Verification and release

Tests cover real SQLite V2 acceptance, exact identity/lease, budget overspend, competing requests, replay and reconciliation seals. Run `go test ./services/work/store/...`, `go vet ./...`, `go build ./...`, and broader e2e under the repository's AGENTS.md contract. The CI result is not known until exact-head workflows complete.

Do not wire this into production, merge protected branches, or lift Aftergraph/Lume #2081/#2090 freeze without the independent reviewer, resolved #106 Trust/AIE action-time proof, real Work/Run edge-case tests and verified provider idempotency.