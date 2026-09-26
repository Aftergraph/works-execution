# Reliability telemetry v1 production promotion

Date: 2026-09-26

This is the auditable one-shot promotion request for the production reliability telemetry merged by PR #162.

## Candidate lineage

- telemetry merge: `72fc0f7bcfa49aa56fb7901e410173a8d2cab5f6`
- prior reliability deployment baseline: `e29f5ef08a46c04a55a9219fa4223c17ce264e95`
- target: current `main` after this deployment-trigger PR is merged through the repository merge queue

## Why a promotion commit is required

The self-hosted WORKS pipeline runs on every push, but `scripts/ops/deploy-works-api-once.sh` mutates the live service only when the exact current-main commit message carries `[deploy-works-api]`.

PR #162 correctly verified on native WORKS, GitHub tests, CodeQL and Sentinel, but its merge commit intentionally had no deployment marker. A live probe therefore still saw the previous API binary and `GET /metrics` returned 404. That is expected deployment gating, not evidence that the telemetry implementation failed.

## Promotion contract

The merge commit title carries `[deploy-works-api]`. The native WORKS pipeline may mutate production only when:

- the executing SHA is exactly current `origin/main`;
- the deployment marker is present on that exact commit;
- non-interactive deployment authority is available;
- candidate VCS revision equals the exact main SHA and `vcs.modified=false`;
- installed and running binary hashes match the candidate;
- `/healthz` recovers;
- Integrity Fabric evidence smoke passes;
- rollback remains available on post-install failure.

PR executions verify but skip live mutation. Only the merged exact-main run is eligible.

## Intended runtime effect

Promote the PR #162 telemetry surface:

- live `GET /metrics` including `works_reliability_*` counters/histogram;
- bearer-protected `GET /v1/reliability?hours=...`;
- durable replay-outcome CloudEvents in `work_audit_events`;
- production wiring of Prometheus registry, collector and reliability audit emitter.

No execution authority or verification rule is broadened.
