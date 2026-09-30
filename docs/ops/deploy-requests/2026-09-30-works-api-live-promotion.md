# WORKS API live promotion — 2026-09-30

## Intent

Promote the current-main WORKS API source after PR #175 (`b6f4bef2ab4cfe36d205d791ece27daeebd90be3`) through `scripts/ops/deploy-works-api-once.sh` and its rollback, health, and Integrity Fabric checks.

This promotion installs the tested WORKS API code, including the optional worker mTLS listener. It does not enable mTLS: production currently has no `WORKS_MTLS_*` configuration or listener on port 3041. Certificate provisioning and Lenovo worker enrollment remain a separate acceptance step.

## Production readback before promotion

- `works-api.service` and `works-worker.service` are active on the VDS.
- The API process reports VCS revision `72688387f6df06394140c11e4e256c66948d7785`; its running binary SHA-256 is `abd003f1094e4fbe294dc05c2e3e58f3f9f0eddc33c460188bc4fb68e06451e0`.
- API health at `http://127.0.0.1:18191/healthz` is HTTP 200.
- The existing worker is `wrkr_prod_1` and uses `http://127.0.0.1:18191`.
- Port 18191 is listening; ports 3041 and 8080 are not.
- The deployed source is an ancestor of current `main`. The SQLite data and existing worker configuration are to be preserved.
- An online SQLite backup was created before the promotion attempt and passed `PRAGMA integrity_check` with WORKS schema version 14.

## Guarded deployment contract

- The exact deployment commit must be the current `origin/main` HEAD and its commit subject must contain `[deploy-works-api]`.
- Run `scripts/ops/deploy-works-api-once.sh` from a clean checkout of that exact commit.
- The deploy script takes a host-wide `flock` before mutation so the native WORKS pipeline and an operator cannot overwrite each other's rollback files.
- The script must build an unmodified binary with matching VCS revision, require the installed and running hashes to match the exact candidate even on idempotent retries, preserve a rollback binary, restart the API, verify the running binary hash and health, and pass the live Integrity Fabric smoke before writing its deployment receipt.
- The smoke uses a temporary 60-second enrollment bearer minted with the existing `WORKS_ENROLL_SECRET`; the token and response files stay in the private temporary directory and no worker row is created.
- Rollback must copy the saved binary to a sibling path, atomically rename it over the executing binary, restart the service, and verify the running process hash and health. Any failed rollback must report `rollback=FAILED` and must not be described as restored.
- Do not replace the binary or edit the production unit outside this script.

## Acceptance after promotion

- The live API source revision and running binary hash match the exact current-main build.
- `/healthz` returns HTTP 200.
- The Integrity Fabric smoke for `wrk_3995b52a8e30d244dc83f6413bba0df2` passes SHA-256, BLAKE3, and HMAC-SHA256 checks and publishes a receipt under `/var/lib/works/deployments/`.
- Existing WORKS API routes and the Aftergraph Cloudflare hostname remain healthy.
- `WORKS_MTLS_LISTENER=NOT_ENABLED` until a production CA and Lenovo client identity are provisioned and tested. This release alone does not prove physical Lenovo enrollment, heartbeat, lease execution, or Runtime `/goal` integration.

## Findings from the 2026-09-30 live attempt

- `/healthz` returned HTTP 200, while the evidence endpoint correctly returned HTTP 401 without a bearer token. The old smoke had omitted authentication; a live enrollment-token plus evidence read now passes the canonicalization, SHA-256, BLAKE3, and HMAC-SHA256 checks.
- The old rollback copied directly over an executing binary and hit Linux `Text file busy`. A concurrent native WORKS deployment also reused the same per-SHA backup name. The script now serializes deployments, gives each backup a unique name, restores by atomic rename, and verifies the running hash.
- The previous marked deploy work was cancelled after it repeatedly retried the failing smoke. The corrected script must be merged and run by the native WORKS pipeline before a deployment receipt or production-promotion pass is claimed.
- The API and worker services remain active and `/healthz` is HTTP 200. The live promotion is still unverified; mTLS, Lenovo enrollment/heartbeat/lease execution, and Runtime `/goal` remain unverified.
