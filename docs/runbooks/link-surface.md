# Runbook — WORKS-Link surface (/link/v1)

**Scope:** enable and smoke-test the WORKS-Link device surface (k-link-01,
ADR-0026/0027). Target: production works-api at `127.0.0.1:18191`, state
`/var/lib/works/`, env `/etc/works/works.env`.

## Background

- The link surface is **always mounted** in works-api. Without a pairing
  secret every route answers `503 link_unavailable` — that is the fail-closed
  law (L6), loud by design. The log line at boot tells you which state you
  are in:
  - `WORKS-Link enabled (/link/v1, pairing secret configured)` — live
  - `WORKS-Link mounted but unavailable (no WORKS_LINK_PAIRING_SECRET)` — 503s
- **Auth model (v1):** pairing-secret HMAC device tokens. `pair` is
  unauthenticated — the 6-char SAS code displayed on both ends IS the
  human-proof boundary. `mounts`/`missions`/`revoke` carry
  `Authorization: Bearer <device token>` (the literal `Bearer ` scheme; the
  token is minted exactly once at claim, TTL 24h, bound to the device's
  durable state — a re-pair invalidates earlier tokens).
- **mTLS is the v2 transport upgrade path (ADR-0026).** The frozen wire's
  `auth` const is `mTLS+device_token`; in v1 only the device-token half is
  live. Do not advertise mTLS as enabled.

## 1. Enable: set WORKS_LINK_PAIRING_SECRET

The secret must be ≥ 32 bytes; anything shorter keeps the surface 503ing.

```bash
# generate once, append to the env file
sudo sh -c 'echo "WORKS_LINK_PAIRING_SECRET=$(openssl rand -hex 32)" >> /etc/works/works.env'
```

(`openssl rand -hex 32` → 64 hex chars = 64 bytes, comfortably above the
32-byte floor.) The file is read by both systemd units via `EnvironmentFile`.

## 2. Build and restart

```bash
cd /opt/works   # or wherever the repo checkout lives
make build
sudo systemctl restart works-api works-worker
```

**Verify the restart actually picked up the new binary — this exact check has
burned the team twice:**

```bash
sudo systemctl show works-api -p ExecMainStartTimestamp
stat -c '%y' bin/works-api
```

`ExecMainStartTimestamp` must be **newer** than `bin/works-api` mtime. If the
timestamp is older, systemd restarted the *old* binary — rebuild and restart
again before anything else.

Then confirm the boot log line:

```bash
sudo journalctl -u works-api -n 20 --no-pager | grep WORKS-Link
# expect: WORKS-Link enabled (/link/v1, pairing secret configured)
```

## 3. Schema migration check

First restart with the new binary also migrates the store (v9 →
v10, idempotent, no backfill):

```bash
sqlite3 /var/lib/works/works.db 'select max(version) from schema_version'
# expect: 10
```

## 4. Smoke test: pair a device (begin → claim)

Pairing is a two-step state machine over one endpoint; the body shape
selects the step. Begin returns a 6-char SAS code (202):

```bash
curl -s -X POST http://127.0.0.1:18191/link/v1/pair \
  -H 'Content-Type: application/json' \
  -d '{"device_id":"dev_smoke1","scopes":["T1_read"]}'
# {"state":"DISPLAY_CODE","sas_code":"ABC234","device_id":"dev_smoke1","expires_in":300}
```

Claim with the displayed code (body carries `sas_code` → claim step; returns
the device token exactly once, 200):

```bash
curl -s -X POST http://127.0.0.1:18191/link/v1/pair \
  -H 'Content-Type: application/json' \
  -d '{"device_id":"dev_smoke1","sas_code":"ABC234"}'
# {"state":"PAIRED","device_id":"dev_smoke1","scopes":["T1_read"],
#  "token":"<device token>","expires_in":86400}
```

Wrong code for the device fails closed and the offer survives (a typo is not
a burn). The offer expires after 5 minutes if unclaimed.

Check the durable device row:

```bash
sqlite3 /var/lib/works/works.db 'select device_id,state from link_devices'
# dev_smoke1|PAIRED
```

## 5. Authenticated smoke: missions feed

```bash
TOKEN=<token from claim>
curl -s http://127.0.0.1:18191/link/v1/missions \
  -H "Authorization: Bearer $TOKEN"
# {"missions":[...],"device_id":"dev_smoke1"}   (missions only; CI works never appear)
```

A missing/expired/bad token answers `401 device_token_required` /
`device_token_invalid`; a revoked device answers `403 device_revoked` even
with a still-signed token (the service re-reads durable state on every call).

## 6. Revoke (cleanup)

```bash
curl -s -X POST http://127.0.0.1:18191/link/v1/revoke \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"device_id":"dev_smoke1"}'
# {"state":"REVOKED","device_id":"dev_smoke1"}
```

Revoking twice is an idempotent no-op (still 200). A revoked device cannot
re-pair without operator intervention (local revoke always wins).

## Mounted endpoints vs. un-mounted commands

| Route | Mounted | Notes |
|---|---|---|
| `POST /link/v1/pair` | yes | begin/claim SAS pairing (`pairing/1.0`) |
| `POST /link/v1/mounts` | yes | T1/T2 only; T2 requires `purpose_bindings` naming the work |
| `GET /link/v1/missions` | yes | T1_read required; missions-only projection |
| `POST /link/v1/revoke` | yes | self-revoke only, idempotent |
| `/link/v1/commands` | **not mounted** | in the frozen `link.wire/1.0` enum, deliberately un-mounted: request-only law (PULSE is never a controller) — 404, never 405-with-hints |

Payload cap: 64 KiB per link request (KB-class per ADR-0026 §9).