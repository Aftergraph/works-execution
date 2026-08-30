# Public API Contract Draft

## Work
`POST /v1/works`
- idempotency key required for external callers
- returns authoritative Work ID

`GET /v1/works/{work_id}`

`POST /v1/works/{work_id}/cancel`

## Workers
`POST /v1/workers/register`
`GET /v1/worker-pools`
`POST /v1/workers/{worker_id}/revoke`

## Artifacts
Use short-lived signed upload/download grants.

## Evidence
`GET /v1/works/{work_id}/evidence`

## Audit
`GET /v1/audit-events`

All mutations must expose actor identity and request correlation ID.
