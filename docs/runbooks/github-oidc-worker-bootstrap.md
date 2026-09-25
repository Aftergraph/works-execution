# Runbook — GitHub OIDC native-worker bootstrap

This surface bootstraps an approved self-hosted GitHub Actions workflow into a short-lived WORKS worker bearer without copying the global `WORKS_ENROLL_SECRET` to the host.

## Authority law

Production configuration is exact, not wildcarded:

- audience: `aftergraph-works`
- repository: `Aftergraph/intelligence-systems-research`
- immutable repository id: `1356862124`
- ref: `refs/heads/bootstrap/lenovo-works-native`
- workflow ref: `Aftergraph/intelligence-systems-research/.github/workflows/bootstrap-lenovo-works-native.yml@refs/heads/bootstrap/lenovo-works-native`
- workflow SHA: exact 40-hex revision supplied at activation time
- event: `push`
- runner environment: `self-hosted`

GitHub's signature, issuer, audience, expiry/not-before, and key rotation are verified by the OIDC verifier. WORKS then applies the exact claim policy above before minting a normal short-lived worker bearer.

The bearer is a bootstrap credential only. WORKS still owns runner registration, leases, execution state and evidence. `WORKS_TOKEN` and the GitHub Actions OIDC request channel are scrubbed from leased subprocess environments.

## Production activation

Target is the canonical VDS API:

- API: `http://127.0.0.1:18191`
- service: `works-api.service`
- environment: `/etc/works/works.env`

After the OIDC-capable binary has been built from an approved/merged WORKS exact SHA:

```bash
sudo scripts/ops/works-github-oidc-enrollment.sh status
sudo scripts/ops/works-github-oidc-enrollment.sh enable <EXACT_ISR_WORKFLOW_SHA>
sudo scripts/ops/works-github-oidc-enrollment.sh status
```

`enable` atomically rewrites only the OIDC variables, preserves file owner/mode, restarts `works-api.service`, waits for health recovery, requires an invalid OIDC token to fail with HTTP 401, and rolls the environment file back if a postcondition fails.

## Physical proof

The bootstrap workflow must prove, on `JONAS-LENOVO`:

1. GitHub issues the expected OIDC claim set.
2. Exact identity enrollment returns 200 and a worker bearer.
3. Wrong audience returns 401.
4. The bearer starts `wrkr_jonas_lenovo` in pool `jonas-lenovo`.
5. Worker log contains the successful runner-registration receipt.
6. No OIDC request token or WORKS bearer crosses the leased subprocess boundary.

Do not enable a permanent shared enrollment secret on Lenovo as a fallback.
