# Runbook — Company Brain surface (/v1/brain)

**Scope:** enable and smoke-test the WORKS Company Brain surface
(k-041, k-042, k-043; ADR-0023). Target: production works-api at
`127.0.0.1:18191`, state `/var/lib/works/`, env `/etc/works/works.env`.

This runbook mirrors the auth-handling shape of
[docs/runbooks/link-surface.md](link-surface.md) and is intended to
be read alongside it; the only routes that change are the
`/v1/brain/*` ones documented below. Do not invent flags or routes
that are not in the table — every command here is correct against
the route spec in [docs/RELEASE-v0.3.0.md](../RELEASE-v0.3.0.md#k-043--v1brain-rest-surface-k-impl-v03brain-cli)
and the contracts under `contracts/schemas/`.

## Background

- **Enable posture.** The brain surface has **no env opt-in**: it
  auto-enables once the binary's store satisfies the `BrainBackend`
  interface (wired in `cmd/works-api/main.go` via type assertion,
  `api.NewBrainServiceFromStore(st, st)`). Until k-042 (schema v11)
  is in the running binary, every `/v1/brain/*` route answers
  `503 brain_unavailable` — fail-closed and loud by design, same
  shape as the link surface. There is no secret to set: auth is
  bearer-token, the same worker enrollment tokens the kernel already
  mints at `POST /v1/workers/enroll`.
- **The store and the API land together.** The brain routes only
  become reachable once k-041 (kernel laws), k-042 (schema v11),
  and k-043 (REST surface) are all merged and the binary is
  restarted. A binary that predates k-042 fails the store type
  assertion, so routes answer 503 `brain_unavailable` — the boot log
  tells you which prerequisite is missing.
- **The boot log line is the truth.** After a successful restart
  with all three slices live:
  ```
  Brain surface enabled (/v1/brain/)
  ```
  This is the line you grep for. The mirror failure line
  (`Brain surface mounted but unavailable`) means the k-043 routes
  are mounted but the running binary's store cannot satisfy the
  `BrainBackend` assertion — rebuild + restart, never a rollback.
- **Auth model (v1).** Bearer enrollment tokens, the same JWTs
  workers get from `POST /v1/workers/enroll`. The same `WORKER_TOKEN`
  you use for `GET /v1/works` works for `GET /v1/brain/objects`
  and `POST /v1/brain/mounts`. Promote and tombstone routes
  require a write scope on the token; read routes require only a
  read scope.
- **Why the runbook uses real `wrk_<32hex>` ids.** Every write to
  `brain_objects` carries an `evidence_ref` field, and the store
  validates that the referenced work id actually exists in
  `works(id)`. This is the ADR-0023 "writes require evidence
  reference" rule. A made-up id is rejected at the store. The
  runbook below shows how to grab a real id from `GET /v1/works`
  using a worker token; if the smoke loop is run on a fresh DB,
  the `GET /v1/works?limit=1` step is the only thing that turns
  a phantom id into a real one.

## 1. Verify preconditions

The brain surface needs (a) the v0.3 binary on disk, (b) a
restarted works-api so the v10 → v11 migration has run, (c)
the k-042 store methods in the running binary, (d) a worker
enrollment secret in `WORKS_ENROLL_SECRET` (so you can mint a
bearer token to call the routes with). Items (a) and (b) are the
integrator's job; the runbook only checks them.

```bash
# (a) binary on disk
stat -c '%y' /opt/works/bin/works-api
# confirm date is today, not a stale v0.2 binary

# (b) schema version after the v0.3 restart
sqlite3 /var/lib/works/works.db 'select max(version) from schema_version'
# expect: 11   (v0.2 baseline measured on this host: 10; v0.3
#              moves it to 11 idempotently on first restart)
```

If you see `10`, the binary on disk is still v0.2. Build v0.3,
restart, then re-check. Do not smoke-test /v1/brain until
this returns `11`.

## 2. Enable: rebuild + restart (no env flag exists)

The surface has no opt-in: `cmd/works-api/main.go` wires
`api.NewBrainServiceFromStore(st, st)` unconditionally, and the type
assertion to `BrainBackend` is the interlock. Landing all three
slices and restarting IS the enable:

```bash
sudo systemctl restart works-api works-worker
```

Verify the restart picked up the new binary (this is the same
trap that burned the team in v0.2; check it):

```bash
sudo systemctl show works-api -p ExecMainStartTimestamp
# must be newer than the stat -c '%y' output above
```

Then the boot log line:

```bash
sudo journalctl -u works-api -n 20 --no-pager | grep "Brain surface"
# expect: Brain surface enabled
```

If the line says `Brain surface mounted but unavailable`, the
surface is mounted but a prerequisite is missing — check
the store assertion passed, that the v0.3 binary is actually
running, and that schema v11 is recorded. Do not "fix" by turning
the env var off; that hides the misconfiguration.

## 3. Mint a worker token and grab a real `wrk_<32hex>` id

This is the runbook equivalent of the link-surface `pair` step:
authenticate once, get a bearer token, and confirm the surface
answers you. The brain surface does not pair — it reuses the
worker enrollment tokens the kernel already mints.

```bash
# Mint a worker token (POST /v1/workers/enroll is the only public route).
# NOTE: throughout this runbook, the literal "Bearer ***" stands
# in for "Bearer ${WORKER_TOKEN}" — copy-paste once and substitute.
WORKER_TOKEN=$(curl -s -X POST http://127.0.0.1:18191/v1/workers/enroll \
  -H 'Content-Type: application/json' \
  -d '{"worker_id":"brain_smoke_1","challenge":"'"$WORKS_ENROLL_SECRET"'"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
echo "token len: ${#WORKER_TOKEN}"
# expect: a JWT, ~hundreds of chars, three dot-separated base64url parts
```

Grab a real work id to use as `evidence_ref`. `GET /v1/works?limit=1`
returns the most-recently-created work in the store. On a fresh
DB there will be zero rows and you will need to create one — see
the note below.

```bash
# Try the read first. (Bearer *** == Bearer ${WORKER_TOKEN})
curl -s "http://127.0.0.1:18191/v1/works?limit=1" \
  -H "Authorization: Bearer ***" > /tmp/works_list.json
WRK_ID=$(python3 -c 'import json; d=json.load(open("/tmp/works_list.json")); print(d["works"][0]["id"] if d.get("works") else "")')

if [ -z "$WRK_ID" ]; then
  echo "no works in store; create one with POST /v1/works"
  # The store is happy with a minimal workgraph.Work; the smoke below
  # just needs the row to exist so the evidence_ref check passes.
  curl -s -X POST http://127.0.0.1:18191/v1/works \
    -H "Authorization: Bearer ***" \
  -H 'Content-Type: application/json' \
    -d "{\"id\":\"wrk_smoke_evidence_${RANDOM}\",\"inputs\":{}}"
  # then re-list:
  curl -s "http://127.0.0.1:18191/v1/works?limit=1" \
    -H "Authorization: Bearer ***" > /tmp/works_list.json
  WRK_ID=$(python3 -c 'import json; print(json.load(open("/tmp/works_list.json"))["works"][0]["id"])')
fi
echo "WRK_ID=$WRK_ID"
# expect: wrk_<32 hex chars>   (matches packages/workgraph NewID("wrk"))
```

The `wrk_` prefix is what the store's evidence-reference check
looks for; the body is 32 lowercase hex chars. The
`newID("wrk")` in `packages/workgraph/workgraph.go` is the source
of truth.

## 4. Smoke loop — write, read, promote, tombstone, mount, list, revoke

All seven steps use the same bearer token. Each command is
correct against the route spec; do not add or rename flags.

```bash
ORG=0a1b
PATH="/org/${ORG}/decisions/adr-0023-smoke.md"
HUMAN_ID="jonas-abde-2026-09-03"

# (a) write a new revision
curl -s -X POST http://127.0.0.1:18191/v1/brain/objects \
  -H "Authorization: Bearer ***" \
  -H 'Content-Type: application/json' \
  -d '{
    "path": "'"$PATH"'",
    "class": "immutable",
    "body": "# ADR-0023 smoke\n\nKnowledge mounted, not prompted.",
    "evidence_ref": "'"$WRK_ID"'"
  }'
# expect: 201, returns the new revision number and the stored row

# (b) read latest non-tombstone revision at that exact path
ENCODED_PATH=$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1],safe=''))" "$PATH")
curl -s "http://127.0.0.1:18191/v1/brain/objects?path=${ENCODED_PATH}" \
  -H "Authorization: Bearer ***"
# expect: 200, body matches the smoke content; revision is the one from (a)

# (c) list under an org-rooted prefix
curl -s "http://127.0.0.1:18191/v1/brain/objects?prefix=/org/${ORG}/decisions/" \
  -H "Authorization: Bearer ***"
# expect: 200, an array containing the smoke entry

# (d) promote to authoritative via a human stamp
curl -s -X POST http://127.0.0.1:18191/v1/brain/objects/promote \
  -H "Authorization: Bearer ***" \
  -H 'Content-Type: application/json' \
  -d '{
    "path": "'"$PATH"'",
    "revision": 1,
    "human_id": "'"$HUMAN_ID"'"
  }'
# expect: 201, a new revision with authoritative:true, promotion:human_stamped
# expect: 403/409 if you tried to promote an ephemeral-class object — the
#          central law refuses to make it authoritative

# (e) tombstone the object (appends a new revision, does not erase history)
curl -s -X POST http://127.0.0.1:18191/v1/brain/objects/tombstone \
  -H "Authorization: Bearer ***" \
  -H 'Content-Type: application/json' \
  -d '{
    "path": "'"$PATH"'",
    "revision": 1
  }'
# expect: 201, a new revision with tombstone:true

# (f) verify the tombstone took effect on the default read
curl -s "http://127.0.0.1:18191/v1/brain/objects?path=${ENCODED_PATH}" \
  -H "Authorization: Bearer ***"
# expect: 404 (the object is hidden by the latest tombstone revision)

# (g) create a read mount
curl -s -X POST http://127.0.0.1:18191/v1/brain/mounts \
  -H "Authorization: Bearer ***" \
  -H 'Content-Type: application/json' \
  -d '{
    "path": "'"$PATH"'",
    "scope": "read",
    "reason": "k-045 smoke loop"
  }'
# expect: 201, returns the mount id; the mount's org_id equals the
#          object's org (0a1b). A cross-org path is rejected at the
#          store with 403 org_boundary_violation.

# (h) list my mounts
curl -s "http://127.0.0.1:18191/v1/brain/mounts?subject=self" \
  -H "Authorization: Bearer ***"
# expect: 200, array contains the mount from (g)

# (i) revoke the mount
curl -s -X POST http://127.0.0.1:18191/v1/brain/mounts/revoke \
  -H "Authorization: Bearer ***" \
  -H 'Content-Type: application/json' \
  -d '{
    "path": "'"$PATH"'",
    "subject": "self"
  }'
# expect: 200, idempotent. Re-running returns 200 with the same state.
```

## 5. Auth failure shapes (so you recognise them under pressure)

| HTTP | Code | Meaning |
|---|---|---|
| 401 | `token_required` | no `Authorization: Bearer <token>` header |
| 401 | `token_invalid` | JWT signature mismatch, expired, or malformed |
| 401 | `scope_insufficient` | token's scope does not include the route's required scope (read vs write) |
| 403 | `org_boundary_violation` | mount's `org_id` does not match the object's `org_id` |
| 404 | (no body) | the exact path has no non-tombstone revision; a tombstone-revisioned path looks like 404 to the default read |
| 409 | `authoritative_without_human` | central-law refusal: `authoritative == true` would be set without `promotion == human_stamped` |
| 409 | `ephemeral_cannot_authoritate` | promote attempted on an `ephemeral` object (the central law's structural guard) |
| 413 | `payload_too_large` | body exceeded 64 KiB |
| 422 | `evidence_ref_not_found` | `evidence_ref` does not match an existing `wrk_<32hex>` work id |
| 503 | `brain_unavailable` | the surface is mounted but the running binary's store lacks the k-042 methods (rebuild + restart) |
| 503 | `brain_surface_unwired` | the surface env var is set but the underlying store is not at schema v11 (re-check step 1) |

## 6. Rollback: the kill-switch

The brain surface ships with `WORKS_BRAIN_KILL_SWITCH` populated
from day one (per the release.rings law, see
[docs/RELEASE-v0.3.0.md](../RELEASE-v0.3.0.md#k-044--releaserings-law-k-impl-v03release-rings)).
Flipping it on reverts the surface to a previous ring without a
redeploy — every `/v1/brain/*` route starts answering 503 with
`brain_kill_switch_active`, the RevertLog gets an entry, and the
surface stays at the lower ring until you flip the switch back
and the release tool promotes it again.

```bash
sudo sh -c 'echo "WORKS_BRAIN_KILL_SWITCH=true" >> /etc/works/works.env'
sudo systemctl restart works-api
# routes now 503; RevertLog records the reason you set above
```

This is the path you take when the smoke loop is green but
production traffic shows a bug. Do not "fix" a misbehaving brain
route by editing the schema — the schema is frozen law, the kill
switch is the operational tool.

## Mounted endpoints vs. un-mounted routes

| Route | Mounted | Notes |
|---|---|---|
| `POST /v1/brain/objects` | yes (auto-gated by the store assertion) | append-only; `evidence_ref` required |
| `GET  /v1/brain/objects?path=...` | yes | latest non-tombstone revision at exact path |
| `GET  /v1/brain/objects?prefix=...` | yes | list latest non-tombstone revisions under an org-rooted prefix |
| `POST /v1/brain/objects/promote` | yes | appends a new `human_stamped` revision; rejected on `ephemeral` |
| `POST /v1/brain/objects/tombstone` | yes | appends a `tombstone:true` revision; default reads hide the object |
| `POST /v1/brain/mounts` | yes | org-boundary checked at the store |
| `GET  /v1/brain/mounts?subject=...` | yes | defaults to the caller's own subject |
| `POST /v1/brain/mounts/revoke` | yes | idempotent self-revoke; cross-subject revoke is a different slice |
| `GET  /v1/brain/search` | **not mounted** | deliberately: search is a presentation concern, never a source of truth (ADR-0023 §5) |

Payload cap: 64 KiB per request (KB-class per ADR-0026 §9,
consistent with the link surface).
