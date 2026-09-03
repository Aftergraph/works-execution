# Auth — works-execution API

## Endpoints requiring Bearer auth (`requireBearer`, HS256 JWT from `/v1/workers/enroll`, `AuthEnabled=true` in production)

| Endpoint | Methods |
|---|---|
| `/v1/works` | POST (create), GET (list) |
| `/v1/works/*` | work item dispatch |
| `/v1/workers/*` (except `/enroll`) | GET `/v1/workers/ready` |
| `/v1/leases`, `/v1/leases/*` | lease acquire/complete/heartbeat |
| `/v1/runners/register` | POST (runner identity) — bearer since k-061 |
| `/v1/runners/{id}/abi` | POST (advertise/overwrite RAB; bearer since k-059, ownership-bound since k-061), GET (bearer read, k-061) |
| `/v1/runners/{id}/abi/negotiate` | POST (bearer read, k-061) |
| `/v1/audit-events` | GET (CloudEvents audit stream) — hardened in this PR |
| `/v1/dora` | GET (DORA metrics) — hardened in this PR |

## Public endpoints (no Bearer)

| Endpoint | Why |
|---|---|
| `/healthz`, `/readyz`, `/metrics` | Liveness/readiness/scrape; firewall the listener in production |
| `/v1/workers/enroll` | Issues short-lived HS256 JWTs (zero-secret enrollment) |
| `/v1/webhook/github` | HMAC-verified — see below |
| `/v1/runners`, `/v1/runners/{id}` | Identity lookup/listing stays public for operator discovery (k-002); only the capability-advertisement surface (`/abi`) is bearer — capability info is operationally sensitive |

## Runner surface ownership (k-061)

Bearer proves token validity, not ownership. On the mutating runner
paths the API therefore enforces `claims.worker_id == runner_id`
(`runner_authz.go`, error code `not_runner_owner`, answered before any
mint or store so denials provably leave the registry unchanged):

- `POST /v1/runners/register` with a caller-supplied `runner_id` — only
  the owning token may register or heartbeat-refresh that identity
  (the exact-match path used by `internal/worker` at startup).
  Omitting `runner_id` is legacy mode: the server mints an id and does
  **not** auto-bind it to the minting token; mutating the minted
  runner afterwards requires a token for its id.
- `POST /v1/runners/{id}/abi` — you may advertise only for the runner
  you are. Reads (`GET /abi`, `negotiate`) require a bearer but are
  deliberately not ownership-bound: the scheduler negotiates against
  other runners' RABs.

Dev mode (`AuthEnabled=false`) passes the ownership interlock by design
(the middleware never populates claims); this is what the e2e suite and
local development run on.

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