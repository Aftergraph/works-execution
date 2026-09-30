# WORKS API live promotion — 2026-09-30

## Current acceptance status

**Promotion to the current canonical main head is pending.** The live API is healthy, but its running source revision is older than main and there is no current-main deployment receipt.

- Canonical `Aftergraph/works-execution` main is `e809b0bfaca8831baa5e7ffd1d0628fa7b35ba35` after PR #179. Its GitHub Go tests, CodeQL, Sentinel checks, and native WORKS `vet`, `test`, and `build` jobs passed.
- The merge commit subject did not contain `[deploy-works-api]`. Native WORKS deploy nodes `wrk_e71377eecd6a603dbd69c98701612283` and `wrk_54f394d3c814d22c7accbb684a3e2d4c` therefore returned `one_shot_marker_absent` for `e809b0bfaca8831baa5e7ffd1d0628fa7b35ba35`. Their overall `SUCCEEDED` state does not mean a deployment occurred.
- `works-api.service` is active on the VDS. Its running process reports VCS revision `d54cddbe1a8a2d66490d2c1b89935de4842a45f0`, `vcs.modified=false`, and SHA-256 `05d5220dccbfe17ab0072e1c2deceb6be351f1dbec8233dfe5fae3c26aa13513`.
- The live VDS health endpoint and public `https://works.aftergraph.org/healthz` both return HTTP 200.
- Earlier native Work `wrk_72d6bbc97599c4b514a01aa5d02d8fe0` verified the installed `d54cddbe` binary, its exact hash, and authenticated Integrity Fabric smoke with `changed=false`. Its success applies to `d54cddbe`, not current main.
- `/var/lib/works/deployments/` has no `works-api-*.json` receipt for the current main head.

## What the next marked merge must prove

The promotion request is a one-shot, merge-queue operation. Its PR title and resulting main commit subject must both contain `[deploy-works-api]`; a missing marker is a safe skip, not a deployment pass. The merge-queue result must be the exact source SHA used by the native WORKS graph.

The guarded script must then:

1. Wait up to 180 seconds for the host-wide deployment lock so duplicate exact-head jobs can serialize and reconcile.
2. Confirm the candidate is the current `origin/main` SHA and has the one-shot marker.
3. Build an unmodified binary whose embedded VCS revision matches that SHA.
4. If the exact candidate is already installed, verify health and authenticated Integrity Fabric evidence without restarting it; otherwise, preserve a rollback binary, atomically replace the target, and restart the service.
5. Re-read the installed and running binary hashes after smoke, then atomically write and read back `/var/lib/works/deployments/works-api-<sha>.json`.
6. Report a verified receipt path, exact source SHA, binary SHA-256, process ID, health, and smoke result. If any changed deployment step fails, roll back and prove the old running hash; otherwise report rollback failure explicitly.

Deployment receipts use `aftergraph.works-api-deployment/2`. `verified_at` records the evidence time. `deployed_at` is `null` when an idempotent run verifies an already-installed candidate, so the receipt does not invent a deployment time.

## Production boundaries

- The production WORKS worker currently uses outbound HTTPS with JWT. There is no production `WORKS_MTLS_*` configuration, CA, client certificate enrollment, or proven cryptographic node attestation. This API promotion does not enable mTLS.
- `works-worker.service` enrollment, `wrkr_jonas_lenovo` heartbeat, Lenovo lease execution, reconnect recovery, and Runtime `/goal` integration remain separate physical acceptance gates.
- Outbound-only control is the target architecture; the physical VDS↔Lenovo pair remains unverified. Direct VDS-to-Lenovo SSH remains diagnostic/recovery transport, not the execution plane.
- Preserve `/var/lib/works/works.db` and the verified pre-promotion backup `works.db.pre-72688387f6df.sqlite`; do not copy or print database contents or credentials.

## Prior deployment failure evidence

- A protected evidence read without a bearer token correctly returned HTTP 401. The corrected smoke enrolls a temporary 60-second bearer in a private temporary directory, reads the evidence endpoint, and checks canonicalization, SHA-256, BLAKE3, and HMAC-SHA256 without creating a worker row.
- The previous rollback copied over an executing binary and hit Linux `Text file busy`. The guarded script now restores through a sibling file and atomic rename, then checks the restarted process hash and health.
- A retry on the exact already-running `d54cddbe` build returned `deployment=verified`, `changed=false`, and `integrity_smoke=true`; the missing receipt exposed an idempotent-path evidence gap. The script now writes a versioned, atomic receipt for both changed and unchanged verified outcomes.
- Earlier work `wrk_42d2dd8bec8b924dd4561b50948289ce` skipped because its candidate `04dbcf7f33ba2b03424fcf4cff89f16d6fe5ae3c` was not current main. Work `wrk_d557d1d6b722805f385a8685feae709f` skipped because `eb1603a4e0791a3db56d463f0f3d8f8966273fd0` lacked the marker. Both are expected fail-closed results.
