# Runbook — RAB control tokens (k-062)

**Scope:** enable and smoke-test server-verified rab/1.0 control tokens at
lease claim (k-062; closes k-058's declared scope boundary). Target:
production works-api at `127.0.0.1:18191`, state `/var/lib/works/`, env
`/etc/works/works.env`. This section mirrors the secret-enable shape of
[docs/runbooks/link-surface.md](link-surface.md): a WORKS_* secret flips a
surface from fail-closed-off to live, and the boot log tells you which
state you are in.

## Background

- **The law in two modes.** A runner whose stored RAB advertises `control`
  with `control_token_required=true` must present the
  `X-RAB-Control-Token` header at `POST /v1/leases/grant`.
  - **Unconfigured (no WORKS_RAB_CONTROL_TOKEN):** verification mode OFF —
    the k-058 advertisement law exactly: any non-empty value passes.
    Zero behavior change for existing deployments.
  - **Configured (key set):** the value must VERIFY as an HMAC credential
    bound to the CLAIMING runner. Wrong value, malformed token, or a valid
    token minted for a different runner => `403 control_token_invalid`
    BEFORE any lease state transition (missing header stays
    `control_token_required`). Observe-only RABs and runners with no RAB
    posted are unaffected in both modes.
- **Boot log states (the key value is never logged):**
  - `RAB control-token verification enabled (WORKS_RAB_CONTROL_TOKEN set)` — live
  - `RAB control-token verification disabled (no WORKS_RAB_CONTROL_TOKEN); advertisement law only` — presence-only
- **Token format (stateless, raw header value, NO scheme prefix):**
  `base64url(runner_id) + "." + hex(HMAC-SHA256(key, runner_id))` —
  see `services/api/rab_control_token.go`. Do not parse it like a Bearer
  Authorization value.

## 1. Enable: set WORKS_RAB_CONTROL_TOKEN

```bash
# generate once, append to the env file
sudo sh -c 'echo "WORKS_RAB_CONTROL_TOKEN=$(openssl rand -hex 32)" >> /etc/works/works.env'
```

The flag `--rab-control-token` overrides the env default. Restart
works-api and confirm the enabled boot-log line above.

## 2. Mint a token for a runner (operator-side only)

k-062 ships NO public mint endpoint. Operators mint out-of-band with the
exported helper `api.MintRABControlToken(key, runnerID)` (a 10-line `go
run` calling it against the same key is sufficient); hand the value to the
runner's config as the raw `X-RAB-Control-Token` header value.

## 3. Smoke test

```bash
# control RAB on file for wrkr_ct_smoke, key configured server-side:
# 1) missing header  -> 403 control_token_required
# 2) foreign/valid-for-another token -> 403 control_token_invalid
# 3) bound token for wrkr_ct_smoke -> 201, lease ACTIVE
```

After any denial, confirm the work is still queued and has no active
lease — the gate precedes the transition (`node a` in the k-058/k-062
tests; check via `GET /v1/works/{id}` and the lease endpoints).
