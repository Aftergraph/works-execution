# G18 effect-time revalidation, fenced WORKS preparation

This slice is stacked on canonical WORKS [#239](https://github.com/Aftergraph/works-execution/pull/239), with a separate Trust Gateway source [#174](https://github.com/Aftergraph/trust-gateway/pull/174) stacked on #173.

`SQLiteStore.PrepareFencedEffect(ctx,input,authority)` is a **server-internal preparation and reconciliation boundary**, not a generic model-callable API. It reuses real atomic WORKS budget hold/acceptance/lease checks from #239, then loads actual persisted WORKS mission and authority identities, not caller or model data. A *different, newly admitted* AIE effect-action ID is verified through a server-owned Trust client at `POST /v2/works/effects/revalidate` with fixed fingerprint scope. Finally the intent is sealed to reconciliation inside a second canonical SQLite transaction with rechecked acceptance, authority/mission/attempt, immutable context and live worker lease.

On Trust outage/revocation or worker expiry, the operation fails closed and does not seal or call a provider. A held budget may remain requiring owner reconciliation; it must never be silently released on ambiguous results or auto-replayed. A previously sealed effect is always reconciliation-required, never a second authorization.

**Security and semantics:** Neither `allowed:true` from Trust nor `PrepareFencedEffect` issues an effect permit. The return value has `CanDispatchEffect:false`. No provider invocation, credential broker, real external change, or live deployment is built here. The AIE remote check and local SQLite seal are separate transactions; revocation can race after preflight. Production-safe consequential side effects still require their *own* fresh authority check at the actual mutation boundary, provider idempotency key enforcement, outcome verification and authorized revocation/compensation.

Tests target real WORKS SQLite V2 acceptance, worker lease, revocation during Trust revalidation, strict HTTP response correlation, no redirects and replay. Run `go test ./services/work/store/...`, `go vet ./...`, CodeQL and exact-HEAD CI.

No main merge, production activation, secrets, rulesets or release freeze changed. Real cross-repo sandbox work is still required before any external effects.
