# Reliability v14 production promotion

Date: 2026-09-26

This is the auditable one-shot promotion request for the reliability changes merged by PR #158, after deploy hardening PR #160.

## Candidate lineage

- reliability merge: `d99a5c305562eb2e61d255bf8cc81f45e95d5a70`
- deploy-hardening merge: `ccdeb55b9656e32f567ebab1d3174cc8fc5de3de`
- target: current `main` after this deployment-trigger PR is merged through the repository merge queue

## Promotion contract

The merge commit title carries `[deploy-works-api]`. The native WORKS pipeline is allowed to mutate production only when:

- the executing SHA is exactly current `origin/main`;
- the marker is present in the exact main commit message;
- non-interactive deployment authority is available;
- the candidate embeds the exact source revision and is not VCS-dirty;
- installed bytes and the running process hash match the candidate;
- `/healthz` recovers;
- the Integrity Fabric evidence smoke passes;
- rollback remains available on any post-install failure.

PR executions therefore verify but skip live mutation. The merged exact-main run is the only eligible promotion.

## Intended runtime effect

Promote the schema-v14 lost-ack/idempotency reliability surface and associated API changes. This request adds no new execution authority and does not weaken merge-queue, Sentinel, CodeQL, or WORKS-native gates.
