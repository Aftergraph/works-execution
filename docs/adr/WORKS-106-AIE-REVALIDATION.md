# WORKS #106 · Trust/AIE live accept-time revalidation

This slice changes the existing POST /v2/works/{id}/accept HTTP route, not a new execution engine. It is FAIL-CLOSED by default: without a configured server-side Trust Gateway authority verifier new V2 acceptance returns 503 before any WORKS write.

The request adds `action_id` and `effect_digest` (SHA-256). The server builds a fixed, typed `aftergraph.works-dispatch-revalidation-request/1` body and sends it to Trust Gateway's operator-scoped `POST /v2/works/dispatch/revalidate` endpoint. Trust uses its real persisted AIE `revalidate(action_id, {bot:'works-runtime', tool:'accept_dispatch_v2', args:canonicalBindings})` with exact action fingerprints. No user-supplied freshness epoch, decision flag or independent policy evaluation in WORKS.

WORKS verifies HTTPS (or localhost for tests), configured bearer, response schema, allowed=true and exact action/authority-lease/effect digest correlation. Any missing, stale, revoked, malformed, unauthenticated or unreachable response fails closed. No automatic retry after failure. The accepted dispatch stores action ID and effect digest, and existing acceptance replay cannot rebind them.

Configuration: `WORKS_TRUST_GATEWAY_URL` and `WORKS_TRUST_GATEWAY_OPERATOR_TOKEN` (existing operator credential, NOT newly minted by WORKS). Both are required. Empty/invalid config means no new V2 acceptance. The new Trust Gateway route is proposed in PR https://github.com/Aftergraph/trust-gateway/pull/173 and MUST be deployed/verified independently before configuring WORKS.

SECURITY: This is ACCEPT-time authority only. Revocation immediately after validation cannot be ruled out across a network boundary. External provider calls remain forbidden until the separate effect-time AIE/Trust revalidation, canonical WORKS budget/fence hold PR #239, idempotent effect dispatch and trusted receipt verification are implemented together. A successful admission or acceptance is NEVER an execution permit.

Tests: `go test ./services/api/...`, `go test ./services/work/store/...`, `go test ./...`, `go vet ./...`, CodeQL and independent native review. No protected merge, deploy, secret write, freeze lift or force bypass has been performed. Lume #2081/#2090 freeze continues.