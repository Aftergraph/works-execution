# v0.3.4 — per-action identity binding

**Tag:** v0.3.4 → main `96ab8a9` (squash-merged PR #36)
**Range since v0.3.3:** `b9d5606..96ab8a9` (1 PR; 4 enforcement slices + composition-adversary sweep + in-wave remediation)
**Status:** merged to main. NOT yet deployed/pushed to prod — do that via the FridayOS deploy loop when you're ready (gateway on :18191 must not be disturbed by this tag step).

> **Also corrects v0.3.3's invisible gap:** k-059 closed anonymous RAB downgrade but the bearer auth it added to the abi surface broke the *operator tool* `cmd/works-runner-id -register` in the prod posture — and CI was blind to it because dev-mode still passed. The k-064 adversary found this (finding A, HIGH). v0.3.4 fixes it in-wave and adds a pin test that flips to `t.Fatal` if the CLI loses its token path again.

## What landed

v0.3.3 made the laws enforce on production paths. v0.3.4 closes the identity-confusion gaps those paths opened: every mutating runner/lease action is now bound to the caller's authenticated identity, and the control-token law can ask for a real credential.

| Slice | Area | Commit | What it enforces |
|---|---|---|---|
| k-060 | services/api (leases + grant) | 7744db6 | grant's body `worker_id` must equal the bearer token's `worker_id` — `403 worker_id_mismatch` before any store touch |
| k-061 | services/api (runner register + abi surface) | bd8a445 | bearer on register + abi POST/negotiate/GET; `gateRunnerOwnership`: `claims.worker_id == r.ID` before any registry write; legacy self-mint preserved for id-outside-pattern runners |
| k-062 | services/api (control tokens) | 1c752d6 | `WORKS_RAB_CONTROL_TOKEN` set ⇒ `X-RAB-Control-Token` must be `base64url(runner_id).hex(HMAC-SHA256(key, runner_id))` bound to the claiming runner; constant-time; value never logged; key unset ⇒ byte-identical k-058 presence law |
| k-063 | docs/standards | edbe8fc | `works-standards audit` exit 0 on the stack (84 honest registry↔mapping row-links) |
| k-064 | services/api (adversary, fresh context) | 4c31e3a | composition sweep of the stacked tree; 14 pin tests, +1204 lines. **Fixed in-wave:** A (CLI broken), E (AUTH.md lies). **Pinned backlog:** B enrollment-charset evasion, C dev-mode lookalike id, D non-grant lease verbs not owner-bound. |

### In-wave remediation (integrator, same PR)

- **A HIGH — k-061 broke `cmd/works-runner-id -register` in prod.** The operator tool sent the schema-shaped identity + Content-Type and nothing else; k-061's new bearer gate turned that into a permanent 401. CI was blind because dev-mode passes. `cmd/works-runner-id` now takes `-token` (`WORKS_RUNNER_TOKEN` env), which attaches `'Bearer '+<token>` (concatenated per the repo's source-string gotcha); the anonymous path still 401s (server correct); pin test `TestAdversary34_RunnerIDCLINoBearerBreaksInProd` rewritten to assert the new posture, not to assert that the break persists.
- **E MEDIUM — docs/AUTH.md drifted from code.** Two false claims removed/fixed: a blanket bearer row for `/v1/works/*` (the subtree reads + cancel/queue are unauthenticated operator surface, per `services/api/api.go`); a documented-but-nonexistent `/readyz`. The omitted laws added: k-058/060/062 control-token + `worker_id_mismatch`, `/v1/cache`, `/v1/brain`, and the gated work sub-actions (`/events`, `/resume`, `/suspend`, `/handoff`). The two doc-pin tests rewritten to `t.Fatal` on any future drift-back — they guard, not record.
- **F INFO — `not_runner_owner`.** Inlined literal → exported const `ReasonNotRunnerOwner` matching k-060's discipline; wire value unchanged.

### Pinned backlog (flip-on-fix tests ship WITH the gap)

| | Finding | Severity | Pin test | Where fixed |
|---|---|---|---|---|
| B | Enrollment ids outside the runner-id pattern evade the control-token law (forever in no-RAB legacy class, auth ON + key ON) | MEDIUM | `EnrollmentCharsetLegacyPass` | v0.3.5 |
| C | Dev-mode lookalike ids (`"wrkr_a "`) stored on lease+attempt unsanitized | MEDIUM (dev-only) | `DevModeLookalikeWorkerIDEscapesGate` | v0.3.5 |
| D | Grant is owner-bound; heartbeat/complete/release/revoke are bearer-but-not owner-bound | MEDIUM | `NonGrantLeaseVerbsUnbound` (with mitigation proven: 128-bit crypto lease ids on no read surface ⇒ leak=false) | v0.3.5 |

## Verified CLEAN by the composition sweep

The adversary ran on the full stacked tree (all 4 slices + k-064 remediation together), not per-branch:

- grant ordering `400 (worker_id_missing) → 403 worker_id_mismatch → 403 control_token / capability → store` with zero state movement on every denial;
- no normalization asymmetry reaches the store under auth;
- **no control-token validity oracle** — all denial answers byte-identical whether the key is set or unset;
- k-061 `gateRunnerOwnership` precedes every registry write (verified by `SingleErrorWritePerDenial` — exactly one writeError per denial path);
- abi surface: 401-before-404 (no runner-id enumeration — `ABISurfaceDenialOrder`);
- dev+key-on is strictly stronger than v0.3.3 (first test in repo to set AuthEnabled AND RABControlKey together: 24-cell cross-posture matrix);
- k-058 presence law byte-identical with key unset (existing k-058 tests pass UNEDITED — that's the compat proof for k-062).

## Gates

- `go build / vet / go test ./...` — full suite green INCLUDING 14 Adversary34 tests post-remediation (regression suite ships with the PR)
- `go test -tags=e2e ./e2e/...` — green (dev posture proof, unchanged)
- `works-standards audit` — exit 0 (k-063)
- `gofmt -l` clean on every touched file
- exact-head CI `works-execution=success`, post-merge main verified by direct grep of each fix on `origin/main`
- **production smoke NOT re-run on :18191** — tag step only; leave the live gateway untouched.

## v0.3.3 → v0.3.4 migration

- **Operator tooling:** `cmd/works-runner-id -register` in prod now requires `-token <enrollment-token>` (or `WORKS_RUNNER_TOKEN`); anonymous registration is a hard 401.
- **New env (optional):** `WORKS_RAB_CONTROL_TOKEN=<key>` — when set, control-capable runners must produce `X-RAB-Control-Token: base64url(runner_id).hex(hmac(key, runner_id))`. Unset = k-058 presence law only (zero behavior change for existing runners).
- **Docs:** `docs/AUTH.md` rewritten to match code; `docs/kanban/board.json` updated to 71/71 (66 + k-060..k-064) pending your `works-kanban report --done` renumber when you next run it.
- **Registry:** `docs/standards/registry.json` now maps every standard to a concrete mapping file (audit passes).

## What's NOT in this PR (pinned, not forgotten)

- Lease verbs other than grant are bearer-authenticated but not owner-bound (finding D) — mitigated by 128-bit crypto lease ids on no read surface; full ownership on heartbeat/complete/revoke is v0.3.5.
- Enrollment charset law to close the legacy-class evasion (B) is v0.3.5.
- Dev-mode id sanitization (C) is v0.3.5.
- `services/publisher` pre-existing race (k-impl-021) — out of scope; non-race is the project gate.
