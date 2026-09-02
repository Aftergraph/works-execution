# works-execution

> Autonomous Software Execution Infrastructure — verified software state as the output.

**Brand:** works-execution (working title; `WORKS` in the source pack).
**Status:** Slice 1 of V1 — Go monorepo + durable `Work` primitive + local worker + minimal API + CLI.
**Track:** Normal (this slice introduces code). First PR will declare Track: Fast for docs-only, Normal here.

## What this repo is

A standalone venture (not an AVC subsystem). The durable `Work` object is the source of execution truth; workers are disposable; the control plane owns state. See `docs/works-venture-starter-pack/` for the full operating plan.

## Quick start

```bash
# Build everything
make build

# Run the e2e test (boots API + worker in-process, submits a real Work)
make e2e

# Or manually:
./bin/works init                    # create works.yaml
./bin/works run --config works.yaml # submit Work to local API
./bin/works status <work_id>        # poll until SUCCEEDED
```

## Repository layout

```
cmd/
  works/         CLI (works init, works run, works status)
  works-worker/  Local worker daemon
services/
  api/           Public HTTP API
  work/store/    SQLite-backed Work persistence
packages/
  workgraph/     Work schema + state machine (no IO)
  evidence/      Evidence record schema (no IO)
docs/
  adr/           Architecture Decision Records
  works-venture-starter-pack/   Vendored source pack
e2e/             End-to-end tests
```

## Kernel & contracts (v0.2)

v0.2 freezes the kernel's external behavior behind hash-attested contracts.
Full release notes: [docs/RELEASE-v0.2.0.md](docs/RELEASE-v0.2.0.md).

- **Freeze manifest:** `contracts/manifest.json` — 21 draft-07 schemas under
  `contracts/schemas/`, attested by the SHA-256 in `contracts/manifest.sha256`.
  Editing a schema changes the hash (visible drift, by design); regenerate
  with `python3 contracts/gen_freeze.py`.
- **Contract tests:** `tests/contracts/` — 21 regression-law tests
  (conformance, baseline, adversarial, compatibility) over the frozen
  schemas. Compatibility rule: N-1 read tolerance per `proto.charter/1.0`;
  breaking = major bump.
- **/link/v1 surface** (k-link-01, ADR-0026/0027): the only routes a PULSE
  device may present to the kernel. Mounted always; without
  `WORKS_LINK_PAIRING_SECRET` every route answers 503 fail-closed. Setup:
  [docs/runbooks/link-surface.md](docs/runbooks/link-surface.md).

  | Endpoint | Method | Auth | Purpose |
  |---|---|---|---|
  | `/link/v1/pair` | POST | none (SAS code is the boundary) | `begin` (offer) → `claim` (device token) |
  | `/link/v1/mounts` | POST | Bearer device token | consent-bearing context mount (purpose-bound) |
  | `/link/v1/missions` | GET | Bearer device token (T1_read) | read-only mission projection |
  | `/link/v1/revoke` | POST | Bearer device token | local-revoke-notify, idempotent |

  `/link/v1/commands` exists in the frozen `link.wire/1.0` enum but is
  **deliberately un-mounted**: the request-only law (PULSE is never a
  controller) makes every privileged command structurally unmountable on the
  link surface — mounting a route that must refuse everything is how a hole
  gets opened later.

## Deviations from the pack

- **ADR-0005:** V1 uses SQLite instead of PostgreSQL for state. Migration path documented.
- **ADR-0006:** Brand "works-execution" chosen as working repo name; full trademark review pending.

## License

TBD (see `12_LEGAL_CHECKLIST/OPEN_SOURCE_STRATEGY.md` in the source pack).
