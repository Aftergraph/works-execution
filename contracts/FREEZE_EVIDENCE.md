# CONTRACT FREEZE SLICE 0 — Evidence Record

**Repo:** JonasAbde/works-execution · **Branch:** contracts/freeze-slice-0
**Base:** main @ 3bca133 · **Slice HEAD:** (se git log efter commit)
**Authority:** final-freeze-review.md — READY_FOR_CONTRACT_FREEZE, godkendt af Jonas

## Manifest
- `contracts/manifest.json` — 21 entries (20 frozen contracts; work.schema/handoff inlined)
- `contracts/manifest.sha256` — freeze-attestation-hash: `2d2f1d27474a908a19aafb9c152be5e27c80987400f21cdfca94080b8bf14a86`
- NOTE: manifest hash regenereres ved `python3 contracts/gen_freeze.py`; ændring i schemas → ny hash = synlig drift (by design)

## Test resultater (go test ./tests/contracts/ -count=1)
- **21/21 PASS** (conformance 1, manifest 1, baseline 3, adversarial 12, compatibility 4)
- Fuldt suite: `go test ./... -count=1` → **30/30 pakker ok, 0 fail** (exit 0)
- `go vet ./tests/contracts/...` clean

## Kontrakter (frosne, repræsenteret)
| # | Kontrakt | Form | Tests |
|---|---|---|---|
| 1 | work.schema/1.0 | schema | baseline-real-work, unknown-field, legacy, drift |
| 2 | kernel.budget/1.0 | schema | adversarial-budget (pause/late-bill/hard-stop) |
| 3 | handoff.schema/1.0 | schema (inlined i work.schema) | (k-mission-02 udvider) |
| 4 | evidence.schema/1.1 | schema | baseline-evidence |
| 5 | quittance.rules/1.0 | schema w/ failed⇒no-price law | quittance-adversarial |
| 6 | cpi/1.0 | handshake-schema | compat old-provider, unknown-cap reject |
| 7 | rab/1.0 | handshake-schema | compile-conformance |
| 8 | identity/1.0 | schema (service_principal felt) | cross-tenant fail-closed |
| 9 | policy.token/1.0 | schema | delegation-narrowing adversarial |
| 10 | events/1.0 | schema (source,seq-orden; ts informativ) | events-skew adversarial |
| 11 | sync.rules/1.0 | schema (revoke_precedence) | revoke-beats-sync |
| 12 | proto.charter/1.0 | schema (unknown_field_tolerance pinned) | compat N↔N |
| 13 | secret.ref/1.0 | schema w/ additionalProperties=false | value-serialization reject |
| 14 | brain.ns/1.0 | schema w/ promotion-law | agent-authoritative reject |
| 15 | obs.evidence.rules/1.0 | if/then-law (evidence=signed+untrimmable) | boundary adversarial |
| 16 | shell.contracts/1.0 | schema w/ allOf-command-laws | request-only + T3 adversarial |
| 17 | link.wire/1.0 | endpoint-enum + auth + tiers | compile-conformance |
| 18 | pairing/1.0 | state-schema (7 tilstande) | pairing-progression |
| 19 | kernel.lifecycle/1.0 | machine-registry-schema | state-machine baseline |
| 20 | pulse.db/1.0 | entities + consent_rule + wal | compile-conformance |
| 21 | release.rings/1.0 | rings + soak + kill_switch | compile-conformance |

## Drift-log (dokumenteret, ikke skjult)
1. **Forward-states (WAITING_HUMAN, SUSPENDED, BUDGET_EXHAUSTED):** frozen schema accepterer dem (N-1-parse); kernel kan IKKE emitte dem endnu (kernel-emission-test på plads). Hverken dokumentarisk eller implementeringsdrift — forward-compatible by design. *(TestDocumentedDriftForwardStates)*
2. **Ingen anden drift fundet:** rigtige Work-validerer mod frozen schema; state machine-transitions matchet 1:1; evidence canonical-JSON-tilgang kompatibel.

## Klassificerede undtagelser
- SAFE TO DEFER (fra final-freeze-review): F10-simuleringstests (N↔N-1 *runtime*-scenarier kræver to implementeringer — schemas + fixtures er på plads); billing-mekanik; nøglerotation-tal.
- PULSE-siden: `pulse.db/1.0` + `release.rings/1.0` er frosne *kontrakter*; conformance-tests i pulse-repoet er en del af PULSE V1-lukningen (separat distributions-blocker: MSIX-cert).

## CI
- works.yml-pipeline (vet → test → build) kører på push via avc-core-poolen; publisher poster status på exact head.
- Lokal exact-head verifikation ved commit: `go vet ./... && go test ./...` = grøn (30/30).