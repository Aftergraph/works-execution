# G18 combined canonical acceptance and effect-time fence

This **review-only** cross-branch integration combines WORKS #240 (server-side live AIE V2 accept-time validation) with WORKS #239/#241 (atomic budget and live AIE effect-time preflight). The source commit has two parents, preserving both histories. It is not a protected-branch merge.

The V2 dispatch now records the immutable accept-time `action_id` and `effect_digest`. New budget holds fail closed if the accepted digest differs or the accepted action ID is absent/invalid. An effect-time operation must use a **distinct** admitted action ID. The difference is checked before contacting AIE, and rechecked inside the final SQLite seal transaction, along with the accepted digest, authority, mission, worker lease, Work and execution context.

An accepted dispatch, a budget reservation, and a successful effect-time AIE response **still do not authorize provider execution**. WORKS `PrepareFencedEffect` returns `CanDispatchEffect:false`, seals reconciliation-only intent, and calls no external provider. Cross-service authorization and sealing cannot be globally atomic, so privileged external tools remain disabled until provider-side idempotency, a verifiable post-effect receipt, live revocation-safe execution host and independent approval are proven.

Run `go test ./...`, `go vet ./...`, `go build ./...` and exact-HEAD CI on this combination. Both upstream branches remain owned separately and must receive independent native reviews. Freeze #2081/#2090 remains.
