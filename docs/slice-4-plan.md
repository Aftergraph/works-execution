# Slice 4 Plan — 12 Standards Implementation Cards

**Date:** 2026-08-31
**Status:** dispatched, subagents working
**Track:** Normal
**Depends on:** slice 1 (d3db1d1), slice 2 (dab84f2), slice 3 (03e192a)

## Source

Each card below was visible on `docs/kanban/board.json` column `ready` after slice 3. This document is the slice-4 contract — what we promised the registry, what the subagents were dispatched to deliver.

## The 12 cards

| ID | Standard | File(s) to create/modify | Tests | Acceptance |
|---|---|---|---|---|
| k-impl-001 | platform-action-manifest (#110) | `internal/manifest/admission.go`; wire into `services/api/api.go createWork` | `tests/manifest/admission_test.go` (4–6) | `ValidateAndEnrich` runs before `Store.CreateWork`; rejects undeclared side effects and permissions; missing fields get safe defaults. |
| k-impl-002 | platform-runner-identity (#121) | `services/runner/registry.go`, `services/api/runner_register.go`, `cmd/works-runner-id/main.go` | `tests/runner/registry_test.go` (5–8) | Mint SPIFFE ID `spiffe://works-execution/ns/<tenant>/sa/<id>`; validate format; lifecycle transitions. |
| k-impl-003 | platform-zero-secret (#114) | `services/api/auth.go`, `services/api/enroll.go`; update `internal/worker/client.go` | `tests/auth/zero_secret_test.go` (6–8) | Bearer token middleware on `/v1/leases/*` and `/v1/workers/*`; HMAC-signed enrollment tokens; dev-mode issuer swappable for real OIDC. |
| k-impl-004 | platform-hermetic-execution (#111) | `internal/sandbox/hermetic.go`; wire into `internal/worker/worker.go runCommand` | `tests/hermetic/hermetic_test.go` (6–10) | Env scrubbed (HOME/PATH dropped); tmpfs workspace; network egress blocked unless manifest allows. |
| k-impl-005 | platform-workflow-provenance (#122) | `services/provenance/producer.go`, `services/provenance/store.go`, `services/api/provenance_handler.go` | `tests/provenance/producer_test.go` (5–7) | Produces SLSA-style attestation per Work; HMAC-signed; persists in `work_provenance` table; v4→v5 migration. |
| k-impl-006 | platform-evidence-first (#113) | `services/evidence/bundle.go`, `services/api/evidence_handler.go` | `tests/evidence/bundle_test.go` (5–7) | `Produce(ctx, store, workID)` returns signed bundle matching `evidence-bundle.schema.json`; bundle_id = sha256(canonical JSON); `GET /v1/works/{id}/evidence`. |
| k-impl-007 | platform-self-healing (#117) | `services/classifier/classifier.go`, `services/classifier/policy.go` | `tests/classifier/classifier_test.go` (10–12) | All 10 failure classes classified by heuristic; per-class retry policy; wired into `CompleteLease`. |
| k-impl-008 | opentelemetry / OpenMetrics / Prometheus (#54, #65, #66) | `services/observability/metrics.go`, `services/api/metrics_handler.go` | `tests/observability/metrics_test.go` (5–7) | Counter/Histogram/Registry; `GET /metrics` Prometheus text; 13 metrics from pack's OBSERVABILITY.md. |
| k-impl-009 | platform-intelligent-scheduling (#118) + platform-content-addressed (#116) | `internal/scheduler/scheduler.go`; wire into `services/api/api.go readyNodesHandler` | `tests/scheduler/scheduler_test.go` (5–8) | `Select(ctx, work, node, runners)` filters by hard constraints, scores by soft optimization; assignment carries explainability record. |
| k-impl-010 | spdx-3.0 + cyclonedx (#59, #61) | `services/sbom/` (SPDX + CycloneDX generators) | `tests/supply_chain/sbom_test.go` (3–5) | `make sbom` emits both formats to `artifacts/sbom/`; both parse and contain expected modules. |
| k-impl-011 | opa + rego (#96, #97) | `policies/lease_grant.rego`, `services/api/policy.go` | `tests/policy/` (4–6) | Rego bundle: production_access requires approved evidence + standard trust_class; wired into `POST /v1/leases/grant`. |
| k-impl-012 | cloudevents + dora-metrics (#77, #65) | `services/audit/cloud_events.go`, `services/deploy/dora.go`, `services/api/audit_handler.go` + `dora_handler.go` | `tests/audit/`, `tests/deploy/` (5–8) | Every Work transition emits CloudEvents v1.0; `GET /v1/audit-events`; DORA metrics at `GET /v1/dora`; v5→v6 migration. |

## Estimated size

| Metric | Per card | Total (12 cards) |
|---|---|---|
| New Go LOC (impl) | ~250–500 | ~4,000 |
| Test LOC | ~150–400 | ~3,000 |
| Files created/modified | ~3–5 | ~50 |

## Sequencing & dependencies

Independent — all 12 can land in any order. Subagent dispatch covered all in 2 parallel batches of 6. Verification step (after dispatch returns) will:

1. `go vet ./...`
2. `go test ./...`
3. `go test -tags=e2e ./e2e/`
4. `make standards-validate` (slice-3 governance gate still green)
5. `make kanban-validate` (cards updated)
6. `make sbom` (smoke)
7. Live integration run: API + worker + a real work → verify provenance + evidence + DORA endpoints return real data

## Definition of done for slice 4

- All 12 cards moved from `ready` to `done` on the kanban.
- All 12 standards updated from `PLANNED` to `IMPLEMENTED` in `docs/standards/registry.json` with concrete evidence pointers.
- No regression on slice 1/2/3 gates.
- Commit + memory update.

## Risks

- **Schema migration collisions.** Three cards (k-impl-005, k-impl-006, k-impl-012) all add tables to the SQLite store. If subagents pick the same migration version (v4/v5/v6) they'll conflict at compile time. Mitigated by giving each a different version in the dispatch context (k-impl-005=v4→v5, k-impl-006 absorbed into v5, k-impl-012=v5→v6).
- **Worker refactor contention.** k-impl-003 (zero-secret) and k-impl-004 (hermetic) both modify `internal/worker/worker.go`. Mitigated by giving each a different function surface (token attach vs. sandbox wrapper).
- **API surface contention.** k-impl-001 (admission), k-impl-005/006/012 (new endpoints) all touch `services/api/api.go`. Mitigated by giving each a distinct handler name.

If integration conflicts arise in the verification step, I'll resolve them before commit.