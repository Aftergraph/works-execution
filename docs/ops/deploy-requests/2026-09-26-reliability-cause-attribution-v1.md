# Reliability cause attribution v1 production promotion

Date: 2026-09-26

## Candidate lineage

- feature merge: `487f716efbaa7de8fe31e7d03123ddfa96a13a0b`
- target: current exact `main` after this promotion PR lands through merge queue

## Promotion intent

Promote the already-merged recovery-cause attribution layer so production can
separate generic replay success from explicit recovery classes.

The deployed surface must expose:

- `works_reliability_ambiguous_ack_requests_total`
- `works_reliability_ambiguous_ack_recovered_total`
- `works_reliability_controller_reconnect_requests_total`
- `works_reliability_controller_reconnect_recovered_total`
- durable `recovery_cause` and `cause_source` in replay audit events
- attributed survival rates in `GET /v1/reliability`

## Governance

This promotion changes no authority semantics. Live mutation remains gated by
`scripts/ops/deploy-works-api-once.sh` and is eligible only on the exact
current-main commit carrying `[deploy-works-api]`.

Rollback, health, VCS revision/hash checks and Integrity Fabric smoke remain
mandatory.
