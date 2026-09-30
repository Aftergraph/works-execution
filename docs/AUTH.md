# Auth — works-execution API

## Endpoints requiring Bearer auth (`requireBearer`, HS256 JWT from `/v1/workers/enroll`, `AuthEnabled=true` in production)

| Endpoint | Methods |
|---|---|
| `/v1/works` | POST (create), GET (list) |
| `/v1/works/*` | work item reads, queue/cancel, logs, evidence and provenance; execution-context writes also require the platform token |
| `/v1/workers/*` (except `/enroll`) | GET `/v1/workers/ready` |
| `/v1/leases`, `/v1/leases/*` | lease acquire/complete/heartbeat |
| `/v1/runners/register` | POST (runner identity) — bearer since k-061 |
| `/v1/runners/{id}/abi` | POST (advertise/overwrite RAB; bearer since k-059, ownership-bound since k-061), GET (bearer read, k-061) |
| `/v1/runners/{id}/abi/negotiate` | POST (bearer read, k-061) |
| `/v1/audit-events` | GET (CloudEvents audit stream) — hardened in this PR |
| `/v1/dora` | GET (DORA metrics) — hardened in this PR |

Bearer authentication on `/v1/works/*` proves possession of an enrolled
worker JWT. It does not provide per-Work owner or tenant authorization:
work reads and queue/cancel are not yet bound to an owning principal. Do not
treat this route as a multi-tenant isolation boundary. Platform bridge
operations retain their additional platform-token checks.

## Public and identity-bound endpoints

| Endpoint | Why |
|---|---|
| `/healthz`, `/readyz`, `/metrics` | Liveness/readiness/scrape; firewall the listener in production |
| `/v1/workers/enroll` | Issues a short-lived HS256 JWT after the legacy shared challenge, or after worker mTLS certificate identity validation when `WORKS_MTLS_ADDR` is enabled |
| `/v1/webhook/github` | HMAC-verified — see below |
| `/v1/runners`, `/v1/runners/{id}` | Identity lookup/listing stays public for operator discovery (k-002); only the capability-advertisement surface (`/abi`) is bearer — capability info is operationally sensitive |

## Worker startup policy

`works-worker` requires `WORKS_ENROLL_SECRET` by default and exits if it is
missing or blank. An operator-provisioned mTLS client certificate can replace
the shared challenge: configure `WORKS_MTLS_CA`, `WORKS_MTLS_CERT`,
`WORKS_MTLS_KEY`, and `WORKS_MTLS_SERVER_NAME`, then use an HTTPS
`WORKS_API` URL. The worker validates the server against the configured CA,
and the server requires a client certificate with one canonical
`spiffe://aftergraph.org/ns/workers/sa/<worker-id>` URI SAN. Enrollment and
every worker ready, lease, runner-registration, ABI, and cache request bind the
certificate identity to the bearer `worker_id`. A certificate is a provisioned
workload identity; it is not TPM-backed machine attestation.

The API enables this mode by setting all of `WORKS_MTLS_ADDR`,
`WORKS_MTLS_CERT`, `WORKS_MTLS_KEY`, and `WORKS_MTLS_CLIENT_CA`. This starts a
separate TLS listener requiring a CA-verified client certificate and also
makes the worker endpoints reject requests arriving through the ordinary
HTTP listener. Keep the mTLS origin behind a Cloudflare Tunnel TCP route and
use `cloudflared access tcp` on the worker so inner TLS reaches the API.
Do not use an HTTP tunnel route for the worker path because it terminates TLS
before the API can validate the client certificate.

The TLS listener validates client certificates against
`WORKS_MTLS_CLIENT_CA`; the worker validates the server against its own
`WORKS_MTLS_CA` bundle. They normally contain different issuing roots. Never
copy the worker CA private key to a worker. Provision worker certificates
through an operator-controlled issuer and rotate/revoke them using that
issuer's documented process. This repository does not yet implement CA
issuance, revocation checking, or hardware-backed attestation.

Unauthenticated operation requires the explicit
`--allow-unauthenticated-dev` flag, and that flag is accepted only when
`WORKS_API` is a loopback URL. A server-side 503 enrollment response also
fails closed unless that same local-development opt-in is present. A 401 or
403 enrollment response always fails; the dev flag does not bypass rejected
credentials. Remote `WORKS_API` URLs must use HTTPS; plaintext HTTP is
accepted only for loopback, and URL-embedded credentials are rejected. Do not
enable unauthenticated mode for a remote or production control plane.

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
config (webhook id 672708250 at
`https://works.aftergraph.org/v1/webhook/github`; signed ping verified 200 on
2026-09-30). An invalid or missing signature is rejected with 401 before the
handler inspects the payload.

## Pending owner action

Manual deletion of the `JonasAbde/__probe__` repository still waits on
GitHub 2FA — the owner must remove it via the GitHub web UI once 2FA is
unblocked. Automated deletion is intentionally not attempted (fail-closed:
nothing on the control plane publicly writes without a secret).
