# v0.3.5 — per-action identity completion (close the entire k-064 backlog)

**Tag:** v0.3.5 → main `9d4f92c` (squash-merged PR #37)
**Range since v0.3.4:** `526f507..9d4f92c` (1 PR; + the `3ff0db2` coverage restore from main)
**Status:** merged to main, gates green, NOT yet deployed to :18191 — deploy + live smoke on request.

> **Why this PR matters:** v0.3.4 shipped three open findings as pinned tests (k-064 B, C, D) with flip-on-fix instructions. v0.3.5 fixes all three; each pin flips in-wave from "gap" to regression — a future regression in any of them is caught immediately.

## What landed

| Slice | File | Fix | Pin (now regression) |
|---|---|---|---|
| **k-065** | `services/api/lease_owner_authz.go` + `leases.go` | `gateLeaseOwner` in `leaseItemHandler`: bearer token's `worker_id` must equal lease's `WorkerID`. **404-before-403** (no id-oracle on the verb surface). Dev mode passes. Denial provably leaves lease ACTIVE. | `TestAdversary34_NonGrantLeaseVerbsUnbound` — foreign revoke now `403 lease_not_owner`; `l.Status == LeaseActive` after the denial |
| **k-066** | `services/runner/registry.go` + `services/api/enroll.go` | `validWorkerID` delegates to exported `runner.RunnerIDPattern` (`^wrkr_[a-z0-9_-]{1,64}$`). The two charsets are now ONE law. `runner.RunnerIDPatternSource` exported for error messages. | `TestAdversary34_EnrollmentCharsetLegacyPass` — rewritten to drive the REAL `/v1/workers/enroll` endpoint (fixture mint bypasses enrollment by design; a34EnrollSecret fixture makes the HTTP path testable) |
| **k-067** | `services/api/leases.go` | `grantLease` trims `body.WorkerID` before any gate or write. Padded lookalike (`"wrkr_a "`) is the same identity as canonical, so the dev-mode control-token gate sees the real runner. | `TestAdversary34_DevModeLookalikeWorkerIDEscapesGate` + the `pPaddedTokA` matrix rows (both postures now answer identically) |

Plus the `3ff0db2` ancestor (in main) that **restores 1204 lines of `adversary_v034_test.go` dropped by v0.3.4's squash-merge**. Lesson pinned in the commit message: after squash-merge, verify the test FILES on main, not just the fix symbols (the 14 Adversary34 pins are the only way the user can see the laws holding; if they're gone, the laws are invisible).

## Composition-adversary ledger (k-064) — CLOSED

| | Finding | Status | Pin |
|---|---|---|---|
| A | k-061 broke `cmd/works-runner-id -register` in prod (CLI gained `-token`) | closed v0.3.4 | regression |
| B | Enrollment charset ⊃ registry charset | **closed v0.3.5** | regression (k-066) |
| C | Dev-mode lookalike worker id forgery | **closed v0.3.5** | regression (k-067) |
| D | Non-grant lease verbs not owner-bound | **closed v0.3.5** | regression (k-065) |
| E | AUTH.md lied about works subtree / readyz / omitted laws | closed v0.3.4 | regression |
| F | `not_runner_owner` inlined literal | closed v0.3.4 | n/a (cosmetic) |

## k-065 — owner-bind the four non-grant lease verbs

`gateLeaseOwner` sits in `leaseItemHandler` AFTER the method check and BEFORE the verb dispatch. Every action on an existing lease (heartbeat/complete/release/revoke) is now owner-bound. The grant verb's owner-bind is in `grantLease` (k-060) — distinct entry point, distinct gate, same interlock. Dev mode (no claims) passes through; e2e posture is unaffected.

Denial order is `404-before-403` — a not-found lease returns 404 (`not_found`) and never 403, so the verb surface cannot be used to confirm whether a lease id exists. k-064 (D) already proved lease ids appear on no read surface; k-065 removes even the denial-side channel.

The pin test now requires (a) `403 lease_not_owner` on a foreign revoke, (b) `l.Status == LeaseActive` after the denial (proven no state mutation), and (c) the existing mitigation sweep (no read surface leaks the id).

## k-066 — enroll and registry share one charset

`validWorkerID` no longer accepts a broad `[A-Za-z0-9_.-]{1,128}` — it delegates to `runner.RunnerIDPattern.MatchString`. The pattern is the same `^wrkr_[a-z0-9_-]{1,64}$` that `runner.BuildIdentity` enforces; the two layers are now a single invariant.

Why this closes B: the k-058 legacy-pass class is "no RAB on file" ⇒ claim sails through. With k-066, no token can exist for an id the registry rejects, so the legacy-pass path is unreachable for any authenticated identity. The pin test now drives the real `/v1/workers/enroll` endpoint (the fixture mints via `Auth.Mint`, bypassing enrollment by design — every other test uses it for clean identity fixtures; this one specifically must not). Defense in depth: even a hand-minted token cannot register the id (the registry's own `Validate` refuses it).

## k-067 — trim the worker id at grant

`grantLease` calls `strings.TrimSpace(body.WorkerID)` immediately after the `missing_field` guard and BEFORE the k-060 owner gate. A whitespace-only id is rejected with 400 `invalid_worker_id`. Otherwise, padded and canonical are the same string for the rest of the request.

The ordering law is preserved: identity question (owner bind) still precedes capability question (control-token-required), but with a normalized body. The `TestAdversary34_ClaimGateOrderOwnerFirst` weird-rows pin splits into two classes — padded (now `403 control_token_required` because they trim to the canonical runner and hit the capability gate) and other (still `403 worker_id_mismatch` because they mismatch the verified identity exactly).

## Gates (this head, /tmp/wt-v035 + main post-merge)

- `go build ./...` — green
- `go vet ./...` — green
- `go test ./... -count=1` — green, 9 packages with output
- `go test -tags=e2e ./e2e/... -count=1` — green
- `go test ./services/api/ -run Adversary34` — all 14 pins green
- `./bin/works-standards audit` — exit 0
- `gofmt -l` clean on every file touched in this PR (3 pre-existing repo flags on base untouched)
- Id-collision check: `wrkr_prod_1`, `wrkr_v034smoke`, `wrkr_v033smoke` all match the new pattern (direct regex check)

## v0.3.4 → v0.3.5 migration (operators)

- **API only** — no env, no schema, no wire-format change.
- **Behavior change (prod, auth ON):** heartbeat / complete / release / revoke now require the bearer's `worker_id` to equal the lease's `WorkerID`. Any internal service holding a lease issued under a different identity will get `403 lease_not_owner` — check the lease id and the bearer before deploy.
- **Behavior change (prod, enrollment):** worker IDs that don't match `^wrkr_[a-z0-9_-]{1,64}$` (e.g. uppercase `WRKR_…`, or longer than 64 chars after `wrkr_`) will be refused at `/v1/workers/enroll` with `400 invalid_worker_id`. Production worker id `wrkr_prod_1` matches.
- **Behavior change (dev, padding):** `wrkr_a ` and `wrkr_a` are now the same identity at grant; the dev-mode lookalike path is closed.

## Live smoke (post-deploy, on :18191)

`smoke_v035.sh` extends smoke_v034's 9 clauses with:
- foreign revoke → `403 lease_not_owner`, victim lease still `ACTIVE`
- enroll(WRKR_A) → `400 invalid_worker_id`
- padded claim → control-token gate (`403 control_token_required`), not 201

## What's NOT in this PR

- The internal/worker Go client's enroll path uses its own identity generation (`runner.MintRunnerID()`), which already matches the new pattern — no client change required.
- `services/publisher` pre-existing race (k-impl-021) — out of scope; non-race is the project gate.
