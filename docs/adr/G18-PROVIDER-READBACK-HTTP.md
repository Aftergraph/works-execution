# G18 · Authenticated HTTP provider receipt readback

This child of WORKS #243 implements an actual **read-only** HTTPS provider readback adapter: `NewHTTPProviderReadback(baseURL, credential)` plus `LookupEffectReceipt(ctx, idempotencyKey)` only. The transport can neither execute nor replay provider effects, and gives no execution permission. Operator configuration requires a pinned HTTPS origin; plain HTTP is allowed solely for loopback integration tests. Credentials are server-only. URLs with userinfo, paths, queries or fragments are rejected, as are redirects.

The API contract is **GET /v1/effects/receipts/{original-idempotency-key}**, with an authenticated operator bearer and bounded (64 KiB) strict JSON response:
```json
{"schema":"aftergraph.provider-readback/1","payload":{"schema":"aftergraph.works-provider-receipt/1","provider_id":"...","operation_id":"...","works_execution_id":"...","effect_id":"...","idempotency_key":"...","effect_digest":"sha256:...","action_id":"act_...","amount":1,"outcome":"succeeded","result_digest":"sha256:...","issued_at_unix_ms":0},"signature":"BASE64"}
```
The pinned Ed25519 public key and canonical WORKS SQLite sealing state are verified by the existing `FencedEffectReceiptReader` from #243, **not** by trusting the HTTP status or wire JSON. A valid response is only historical evidence. 404, offline, malformed JSON, wrong key, oversized body, unexpected fields and redirects fail closed and never retry an external mutation.

An official Go test connects this new client over localhost HTTP to the existing **separate SQLite sandbox provider**. It simulates a provider commit with lost reply and confirms only one HTTP lookup, one provider Apply in test-only code and exactly one sandbox counter increment. Additional negative transport tests cover redirects and malformed replies. Real production provider write/mutation API and signing key provisioning are deliberately absent; AIE and provider mutation are not atomically co-committed.

This feature branch must not be merged until #239/#240/#241/#242/#243 parents are reviewed and landed legitimately. Run exact-head Go Test/Vet/Build, CodeQL, and native independent review. Lume #2081/#2090 production freeze remains.