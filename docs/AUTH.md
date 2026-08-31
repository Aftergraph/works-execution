# Auth — works-execution API

## Endpoints requiring Bearer auth (`requireBearer`, HS256 JWT from `/v1/workers/enroll`, `AuthEnabled=true` in production)

| Endpoint | Methods |
|---|---|
| `/v1/works` | POST (create), GET (list) |
| `/v1/works/*` | work item dispatch |
| `/v1/workers/*` (except `/enroll`) | GET `/v1/workers/ready` |
| `/v1/leases`, `/v1/leases/*` | lease acquire/complete/heartbeat |
| `/v1/runners`, `/v1/runners/*` | runner registration + lookup |
| `/v1/audit-events` | GET (CloudEvents audit stream) — hardened in this PR |
| `/v1/dora` | GET (DORA metrics) — hardened in this PR |

## Public endpoints (no Bearer)

| Endpoint | Why |
|---|---|
| `/healthz`, `/readyz`, `/metrics` | Liveness/readiness/scrape; firewall the listener in production |
| `/v1/workers/enroll` | Issues short-lived HS256 JWTs (zero-secret enrollment) |
| `/v1/webhook/github` | HMAC-verified — see below |

## GitHub webhook HMAC

`POST /v1/webhook/github` verifies the `X-Hub-Signature-256` header as an
HMAC-SHA256 over the raw request body using `WORKS_WEBHOOK_SECRET`
(`webhook-secret` flag). The secret is shared with the GitHub webhook
config (webhook id 672708250 on works.rendetalje.dk). An invalid or
missing signature is rejected with 401 before the handler inspects the
payload.

## Pending owner action

Manual deletion of the `JonasAbde/__probe__` repository still waits on
GitHub 2FA — the owner must remove it via the GitHub web UI once 2FA is
unblocked. Automated deletion is intentionally not attempted (fail-closed:
nothing on the control plane publicly writes without a secret).