# RFC-0006: Self-Hosted CI — works builds itself

**Status:** IMPLEMENTED (2026-08-31)
**Author:** Hermes Agent (atlas)
**Date:** 2026-08-31
**Track:** Normal
**Supersedes:** none
**Related:** RFC-0004 (BYOC pools), RFC-0005 (cache), RFC-0007 (web view)

## Problem

The venture thesis is "WORKS replaces GitHub Actions for repos that want
self-hosted, evidence-first CI". A control plane that cannot run its own
CI is unproven. Before this RFC, works-execution's CI ran on GitHub
Actions — the very thing it claims to displace.

## Decision

**Dogfood all the way down:** works-execution's own pipeline runs on its
own control plane. A push to `main` triggers the webhook → the control
plane creates a Work → a BYOC pool runner (`avc-core`) leases the nodes,
runs the pipeline, and the publisher posts the result back to the commit
as a GitHub status. Zero GitHub Actions.

## Design

### 1. Pipeline definition — `works.yml`

```yaml
work:
  verify:
    triggers: [push, pull_request]
    requirements:
      confidence: development
      os: linux
      arch: amd64
      pool: avc-core        # RFC-0004: pinned to the BYOC pool
    nodes:
      vet:
        run: go vet ./...
        cache: true         # RFC-0005: byte-identical inputs skip execution
        timeout_s: 120
      test:
        needs: [vet]
        run: go test ./... -count=1
        timeout_s: 600
      build:
        needs: [test]
        run: make build
        timeout_s: 120
```

Each node becomes a graph node with `needs` edges preserved; the DAG is
`vet → test → build`.

### 2. Driver — `cmd/works-ci`

- `works-ci run [--config works.yml] [--api URL] [--enroll-secret S] [--timeout-s N]`
  submits the pipeline as a single Work and waits for a terminal state.
- `works-ci watch <work_id>` re-attaches to a running pipeline.
- Exit codes: `0` pipeline SUCCEEDED, `1` FAILED/CANCELLED/timeout,
  `2` usage.

### 3. Failure diagnosability

Evidence details carry the error log tail on failure — a red pipeline
tells the operator *why*, not just *that*.

### 4. Sandbox toolchain

The hermetic sandbox environment gains the Go toolchain
(`PATH`/`GOMODCACHE`/`GOPATH`/`GOCACHE` under `/var/lib/works`), and the
systemd worker override grants repo write access for the build step.

## Verification (production VDS, 2026-08-31)

- Pipeline PASS: **21s cold → 3s warm** (all-cache-hit, RFC-0005).
- Failure diagnosability verified: `evidence.details.error` carries the
  log tail.
- k-impl-029 done; `make build` includes `works-ci`.

## References

- `works.yml`, `cmd/works-ci/main.go`, `cmd/works-ci/auth.go`
- `internal/sandbox/hermetic.go`, `Makefile`
- RFC-0004 (pool pinning), RFC-0005 (cache)
