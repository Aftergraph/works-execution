# WORKS source-root activation

This runbook activates the production worker checkout root introduced by the source-root isolation contract.

## Canonical boundary

- production API: `http://127.0.0.1:18191`
- worker service: `works-worker.service`
- environment file: `/etc/works/works.env`
- source root: `/var/lib/works`
- checkout parent: `/var/lib/works/works-sources`
- activation helper: `scripts/ops/works-source-root.sh`

The helper does not install a new worker binary. Before activation, the canonical worker binary must already contain the `-source-root` contract from main.

## Fleet boundary

Production may run more than one worker unit (`works-worker.service`, `works-worker-2.service`, ...) sharing one binary and one env file. The activator discovers every **active** `works-worker*.service` unit, enforces the service contract on each, restarts each, and requires the runtime `WORKS_SOURCE_ROOT` readback on every unit before reporting success. Inactive/standby units are not restarted; they inherit the env file on their next start. A unit that fails any check fails the whole activation (fail closed) and triggers rollback of the env file plus restart of every fleet unit.

## Status

Read-only status:

```bash
cd /opt/works
bash scripts/ops/works-source-root.sh status
```

The status path proves that the worker service is active, it uses the canonical env file, the running executable is a `works-worker` binary exposing `-source-root`, and the local WORKS API health endpoint is healthy. Without root, the helper intentionally does not expose process environment or the root-owned env file.

## Activation

Run on the production WORKS VDS through the authorized root deployment path:

```bash
cd /opt/works
bash scripts/ops/works-source-root.sh enable
```

The helper requires root, validates the root-owned canonical env file, validates `/var/lib/works` capacity/inodes/mount policy, atomically replaces only `WORKS_SOURCE_ROOT`, creates the private checkout parent, restarts every active worker unit, requires a new worker PID per unit, reads the non-secret source root back from each running process, and rolls back the env file plus fleet restart if any postcondition fails.

The operation is idempotent: if both the canonical env file and running worker already use `/var/lib/works`, it returns `changed:false`.

## Required deployment sequence for issue #137

1. Ensure `/opt/works` is at exact merged main commit `8c55999010ebdd8ab8c3da1ed24dae0a69606ea7` or a descendant containing the same source-root contract.
2. Build/install the exact `works-worker` candidate using the governed root deployment process.
3. Run `works-source-root.sh status`.
4. Run `works-source-root.sh enable`.
5. Run `works-source-root.sh status` again.
6. Submit a Rendetalje exact-head verification work.
7. Require source checkout evidence under `/var/lib/works/works-sources`.
8. Require the `works-execution` GitHub status on the Rendetalje head to become SUCCESS.

## Truth boundary

A successful activator result proves the active worker is configured with the new source root. It does not by itself prove an exact-SHA repository workload succeeded. That requires a subsequent WORKS execution receipt.

Never print `/etc/works/works.env`, enrollment credentials, GitHub credentials, platform bridge secrets, verifier tokens, or process environments beyond the single non-secret `WORKS_SOURCE_ROOT` read-back.
