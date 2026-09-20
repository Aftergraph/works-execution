# Sentinel → WORKS verifier credential activation

This runbook activates the dedicated credential used by Sentinel to publish terminal semantic-verification receipts into WORKS.

## Canonical boundary

- production API: `http://127.0.0.1:18191`
- service: `works-api.service`
- environment file: `/etc/works/works.env`
- credential: `WORKS_VERIFIER_TOKEN`
- ingest route: `POST /v1/works/{id}/verification`

The credential is distinct from worker enrollment, RAB control, GitHub and platform-bridge credentials.

## Activation

Run on the **actual WORKS production host as root**:

```bash
cd /opt/works
bash scripts/ops/enable-sentinel-verifier-credential.sh
```

The helper:

1. requires root and the canonical root-owned env file;
2. proves WORKS is active and healthy before mutation;
3. confirms worker enrollment is already configured by expecting a wrong challenge to return HTTP 401;
4. preserves an existing verifier token when it is already at least 32 bytes;
5. otherwise generates 32 random bytes as hexadecimal on-host;
6. atomically replaces only `WORKS_VERIFIER_TOKEN`;
7. restarts `works-api.service`;
8. rolls the env file back automatically if any postcondition fails;
9. requires a wrong verifier credential to return HTTP 401 after restart, proving ingest is configured without disclosing the real value;
10. requires worker enrollment to remain HTTP 401 for a wrong challenge.

A pre-activation verification response of HTTP 503 means the credential is not configured. HTTP 401 means the credential is already configured.

## Truth boundary

Successful activation proves only that the WORKS verifier-auth boundary is configured and fail-closed. It does **not** prove that Sentinel possesses the same secret or that an end-to-end valid receipt has been persisted. The next proof must use a real Sentinel publisher with the shared credential and then read the immutable WORKS verification projection back.

Never print, commit, upload or place the token in issue/PR text, Actions output, source code or fixtures.
