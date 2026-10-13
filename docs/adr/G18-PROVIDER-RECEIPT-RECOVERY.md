# G18: Verified provider receipt recovery and sandbox crash tests

Canonical stack: WORKS #239 (budget/lease holds), #240 (accept-time AIE), #241 (effect-time AIE), #242 (combined action binding). This child adds **provider readback and immutable result evidence** without enabling real external effects.

## Provider contract

The trusted host pins a provider identity and a verified Ed25519 PUBLIC key. A provider returns a canonical `aftergraph.works-provider-receipt/1` envelope signed over exact `json.Marshal` bytes. It includes provider operation ID, WORKS execution/effect/idempotency, original effect SHA256 digest, separate effect-time action ID, reserved amount, outcome (`succeeded` or `failed`), provider result digest and issue time. WORKS rejects wrong signer, stale or malformed identities, action reuse, unauthorized provider, changed result, missing evidence or any claim to effect authorization.

`FencedEffectReceiptReader.Reconcile` performs ONLY `LookupEffectReceipt(ctx, idempotencyKey)`. It has no `Execute`/`Apply` method. The original WORKS `dispatch_effect_holds` row must already be sealed to `reconcile`; recovering an absent/unsealed/rebound effect is forbidden. The verified result and signature are recorded in an immutable `dispatch_effect_receipts` table in the SAME canonical WORKS SQLite database. Concurrent replay cannot change the stored evidence. `Read` verifies the stored signature on every access, even after worker lease expiry or revocation; historical evidence stays readable, but no new permission arises.

## True sandbox behavior in tests

The **test-only** sandbox provider uses a separate durable SQLite database, implements provider-side idempotency, signs an independently committed counter result, and supports lookup-only recovery when the simulated provider response was lost after commit. Regression tests assert: no repeat mutations, lost results recovered, offline providers remain UNKNOWN, forged signatures never persist and altered identity cannot reconcile. This is a real multi-database test, not a mock of the WORKS ledger.

## Limits and gating

This slice is not a production provider or execution API. The sandbox's `Apply` method exists only in a `_test.go` fixture; the shipped code has a LOOKUP-only provider interface. No server route, credentials, production provider, money spend, merge or deploy is enabled. AIE revalidation and SQLite fencing are NOT globally atomic with remote provider mutation. End-to-end external dispatch still requires the real provider's authenticated idempotent mutation API, pinned signing identity, policy-approved host, fresh authorization immediately at mutation, independent reviews and the Lume #2081/#2090 freeze lift. Unknown outcomes must never be auto retried.

Run `go test ./services/work/store/...`, `go test ./...`, `go vet ./...`, CodeQL and exact-HEAD CI on the stacked integration.