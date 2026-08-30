# Agent operating contract — works-execution

This file is the local execution contract for coding agents, bots, reviewers, and orchestrators working in this repo.

## Authority

- ADRs in `docs/adr/` are authoritative for architectural decisions.
- `docs/works-venture-starter-pack/` is the source-of-truth venture plan.
- Velocity track: Normal by default. Fast only for docs/ADRs with no runtime impact.

## Stack

- Language: Go 1.23+.
- State: SQLite (see ADR-0005).
- Worker: local subprocess for V1.
- CLI: Go (`cmd/works`).
- API: Go `net/http` + `chi` router.

## Gate requirements (Normal track)

- `go vet ./...` clean.
- `go build ./...` clean.
- `go test ./...` green, including `e2e/...`.
- All ADRs reviewed and accepted before merge.

## Non-negotiables

- Works cannot be marked SUCCEEDED without authoritative state machine transition AND evidence on disk.
- Workers are disposable; control plane owns state.
- V1 must work without AI.
