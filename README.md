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

## Deviations from the pack

- **ADR-0005:** V1 uses SQLite instead of PostgreSQL for state. Migration path documented.
- **ADR-0006:** Brand "works-execution" chosen as working repo name; full trademark review pending.

## License

TBD (see `12_LEGAL_CHECKLIST/OPEN_SOURCE_STRATEGY.md` in the source pack).
