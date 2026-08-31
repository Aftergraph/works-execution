# RFC-0003: M1 — External Repository Pilot

**Status:** IMPLEMENTED (2026-08-31)
**Author:** Hermes Agent (atlas)
**Date:** 2026-08-31
**Track:** Hard
**Supersedes:** none
**Related:** ADR-0007 (open-core), RFC-0002 (slice 5)

## Context

Slice 5 closed with a 1.6s end-to-end pilot on synthetic
`echo`/`uname`/`alpine:3.20` workloads. That's a smoke test, not a
proof. WORKS' venture thesis is that it can execute a *real* build and
test on a *real* repository, recover from worker loss, publish a GitHub
Check, and be measurably comparable to GitHub Actions.

Without M1, the standards registry, hermetic sandbox, and evidence
bundles are toys.

## Decision

M1 = the smallest production-shaped vertical slice that connects a
real GitHub repository to WORKS, executes its real build/test, recovers
from worker loss, and publishes an authoritative GitHub Check.

Pilot repo: **`JonasAbde/works-execution`** (our own). Rationale:
Go stack, admin access, no coordination cost, demonstrates
WORKS-on-WORKS.

## Scope (in)

1. **GitHub webhook receiver** with HMAC-SHA256 signature verification
   and idempotent delivery handling (`X-GitHub-Delivery` as the dedup
   key).
2. **Real repo source** — clone exact SHA into an isolated workspace
   using a work-scoped token, not a long-lived PAT.
3. **Real build/test execution** — detect Go (`go.mod`) and run
   `go vet ./...` + `go test ./... -count=1`. No synthetic echo as
   terminal evidence.
4. **Worker-loss recovery** — chaos test that kills the worker mid-build
   and asserts a replacement worker re-executes, with the prior attempt
   preserved as evidence.
5. **GitHub Check Run publisher** — uses the GitHub REST API to create
   a Check Run for the commit SHA with the conclusion (success/failure)
   and details URL pointing to the evidence endpoint.
6. **Evidence enrichment** — repo URL, commit SHA, runtime fingerprint
   (`go version`, `uname -a` of the worker, container image digest
   if Docker), artifact SHA256, timestamps, attempt→lease chain.
7. **Benchmark** — same repo, same SHA, three runs each on:
   - GitHub Actions (ubuntu-latest, no cache)
   - WORKS (local subprocess)
   - WORKS (Docker, no cache)
   Capture cold run, warm run, recovery run, queue time, execution
   time, wall time, CPU time, est. cost. Do not claim speed wins
   before evidence exists.
8. **Pilot UX** — `works connect github` (registers a GitHub App /
   webhook), `works pilot <repo>` (submits + watches + reports
   Check URL).

## Scope (out, explicit)

- AI remediation / agent-native work (M6+).
- Hosted multi-tenant control plane (commercial).
- Actions compatibility / conversion wedge (M3).
- Distributed cache + affected-only execution (M4).
- Worker mesh (M5).
- SAML/SSO, billing, audit export.
- Anything not in `JonasAbde/works-execution` as the pilot target.

## Architecture

```
┌──────────────┐ webhook          ┌─────────────────┐
│  GitHub.com  │ ───────────────► │ works-webhook   │
│  (smee.io)   │  HMAC-SHA256     │ :8080/v1/webhook│
└──────┬───────┘                  └────────┬────────┘
       │                                    │ enqueue Work
       │                                    ▼
       │                           ┌────────────────┐
       │                           │   works-api    │  (slice 1-5)
       │                           │   SQLite + ... │
       │                           └────────┬───────┘
       │                                    │ lease
       │                                    ▼
       │                           ┌────────────────┐
       │                           │ works-worker   │
       │                           │  + git clone   │
       │                           │  + go test     │
       │                           │  + sandbox     │
       │                           └────────┬───────┘
       │                                    │ evidence
       │ Check Run                           ▼
       └◄─────────────────────────  ┌────────────────┐
           works-publisher            │  evidence DB  │
                                     │  + /v1/...    │
                                     └────────────────┘
```

## Cards (kanban IDs k-impl-018 through k-impl-025)

| ID | Title | LOC budget | Verification |
|---|---|---|---|
| 018 | `services/webhook/github.go` — receiver, HMAC verify, idempotent delivery | ~300 | unit + invalid-sig + duplicate |
| 019 | `services/source/git.go` — work-scoped token, exact SHA checkout, isolated workspace | ~300 | unit + clone-into-tmpdir |
| 020 | `services/runner/real.go` — stack detect (Go/modules), run `go vet` + `go test ./...` | ~400 | integration against pilot repo |
| 021 | `services/publisher/check.go` — GitHub Check Run via REST, conclusion, details URL | ~300 | live test with PAT |
| 022 | `internal/worker/worker.go` chaos test — kill mid-build, recovery, attempt preserved | test-only | chaos test green |
| 023 | `cmd/works-bench/main.go` + `services/bench/` — Actions vs WORKS, cold/warm/recovery, JSON report | ~500 | live Actions workflow + WORKS run |
| 024 | `cmd/works connect github` + `works pilot <repo>` UX | ~300 | e2e manual |
| 025 | Evidence enrichment — repo URL, commit SHA, runtime fingerprint, artifact SHA256, attempt→lease | ~200 | evidence schema test |

**Total LOC budget:** ~2,300 across 8 cards (excluding tests).

## Definition of Done (must all be true before M1 closes)

- [ ] PR opened in `JonasAbde/works-execution` → webhook received
      → Work created with exact SHA → worker clones → runs
      `go vet ./...` + `go test ./... -count=1` → Check Run posted
      with correct conclusion → benchmark report published.
- [ ] Chaos: worker killed mid-build, replacement recovers, prior
      attempt preserved, evidence row exists, Work reaches terminal
      state.
- [ ] Invalid webhook signature → 401, no Work created.
- [ ] Duplicate webhook delivery (same `X-GitHub-Delivery`) → one
      Work only.
- [ ] Fork PR → secret boundary enforced (no write access token, only
      read).
- [ ] Benchmark report in `docs/benchmarks/m1-YYYY-MM-DD.md` with
      Actions vs WORKS, cold/warm/recovery, queue time, exec time,
      wall time, CPU time, est. cost. Speed claims only with measured
      evidence.
- [ ] No long-lived plaintext PAT on any worker.
- [ ] Docker sandbox defaults unchanged (--read-only, --cap-drop=ALL,
      --network=none, no-new-privileges).
- [ ] All `go vet`, `go test ./...`, e2e, e2e_docker, standards-validate,
      kanban-validate gates green.

## Lock (no scope creep before M1 closes)

- No new standards additions.
- No new features in `internal/scheduler`, `internal/sandbox`,
  `services/api`, `services/worker` unless directly required for a
  card above.
- No UX work, no billing, no multi-tenant.
- No agent-native work, no AI remediation.
- Pilot is one repo: `JonasAbde/works-execution`. No broadening.

## References

- https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
- https://docs.github.com/en/rest/checks/runs
- https://docs.github.com/en/apps/creating-github-apps
- ADR-0007 (open-core — GitHub integration belongs in OSS)
- RFC-0002 (slice 5 sandbox — reused for M1 worker)
