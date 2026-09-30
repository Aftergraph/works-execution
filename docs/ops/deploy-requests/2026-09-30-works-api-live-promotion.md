# WORKS API live promotion — 2026-09-30

## Intent

Promote the current-main WORKS API source after PR #175 (`b6f4bef2ab4cfe36d205d791ece27daeebd90be3`) through `scripts/ops/deploy-works-api-once.sh` and its rollback, health, and Integrity Fabric checks.

This promotion installs the tested WORKS API code, including the optional worker mTLS listener. It does not enable mTLS: production currently has no `WORKS_MTLS_*` configuration or listener on port 3041. Certificate provisioning and Lenovo worker enrollment remain a separate acceptance step.

## Production readback before promotion

- `works-api.service` and `works-worker.service` are active on the VDS.
- The current API source is `7ea30cfe8550b280d96b25191774de15d9904e64`; its binary SHA-256 is `bb59aa0b036645a39a101af73f2c45e0ca26abefaa6e04d97e2f8dbb24ffb523`.
- API health at `http://127.0.0.1:18191/healthz` is HTTP 200.
- The existing worker is `wrkr_prod_1` and uses `http://127.0.0.1:18191`.
- Port 18191 is listening; ports 3041 and 8080 are not.
- The deployed source is an ancestor of current `main`. The SQLite data and existing worker configuration are to be preserved.

## Guarded deployment contract

- The exact deployment commit must be the current `origin/main` HEAD and its commit subject must contain `[deploy-works-api]`.
- Run `scripts/ops/deploy-works-api-once.sh` from a clean checkout of that exact commit.
- The script must build an unmodified binary with matching VCS revision, preserve a rollback binary, restart the API, verify the running binary hash and health, and pass the live Integrity Fabric smoke before writing its deployment receipt.
- Any failed check must leave the old API restored and healthy. Do not replace the binary or edit the production unit outside this script.

## Acceptance after promotion

- The live API source revision and running binary hash match the exact current-main build.
- `/healthz` returns HTTP 200.
- The Integrity Fabric smoke for `wrk_3995b52a8e30d244dc83f6413bba0df2` passes SHA-256, BLAKE3, and HMAC-SHA256 checks and publishes a receipt under `/var/lib/works/deployments/`.
- Existing WORKS API routes and the Aftergraph Cloudflare hostname remain healthy.
- `WORKS_MTLS_LISTENER=NOT_ENABLED` until a production CA and Lenovo client identity are provisioned and tested. This release alone does not prove physical Lenovo enrollment, heartbeat, lease execution, or Runtime `/goal` integration.
