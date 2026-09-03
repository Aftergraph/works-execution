# v0.3.3 — enforcement wave: the laws now run on production paths

**Tag:** v0.3.3 → main `dadfb6a` (squash-merged PR #35)
**Range since v0.3.2:** `606a28e..dadfb6a` (1 PR; k-054 re-land + 3 new slices)
**Status:** shipped, merged, deployed, runtime-verified

> **Also corrects v0.3.2:** PR #34 was merged from an integration tip that predated the k-054 adversary results, so main received slices k-051/52/53 only. v0.3.3 re-lands the full k-054 set (deep-copy fix, namespaced linkage, adversary tests) — see docs/RELEASE-v0.3.2.md correction note. Verified by `git grep` on origin/main, not diffstats.

## What landed

v0.3.1 shipped kernel laws. v0.3.2 wired them into service internals. v0.3.3 makes them **enforce where production actually runs** — plus the re-land.

| Slice | Area | Commit | What it enforces |
|---|---|---|---|
| k-054 (re-land) | services/api | (in #35) | cloneRAB deep-copies Extra via kernel JSON round-trip; RuntimeABI linkage namespaced under `rab_runtime_meta`; 3 adversary tests (A/B regressions, C now closed) |
| k-057 | internal/worker | 5b67942 | ADR-0022 on the REAL exec path: secret refs resolved once per item before the docker/command branch |
| k-058 | services/api | 38f0ce9 | rab/1.0 control-token law gates lease grant: control-capable RAB ⇒ claim without X-RAB-Control-Token => 403 BEFORE any state transition |
| k-059 | services/api | 245398a | bearer auth on mutating POST /abi (closes k-054 finding C); reads stay public; anonymous downgrade => 401 |

## k-057 — secrets on the production worker path

The `internal/worker` daemon executes `ReadyItem.Env` literally. A `secret://` value there previously reached the child env unresolved. Now `resolveItemEnv` replaces every ref value with its resolved secret at execution time (mapping from the `packages/secrets` kernel; `SECRET_<PROVIDER>_<NAME>`), and a resolution failure means the node NEVER EXECUTES — the error names the ref only, never a value. Zero-ref items are byte-identical to the old behavior (pinned by test). Exec-level test proves resolution reaches `cmd.Env` via `printenv`; sentinel leak-sweep over every failure path.

## k-058 — the control-token law gates claims

A runner whose advertised RAB `RequiresControlToken()` can no longer grant itself a lease without presenting `X-RAB-Control-Token`. Denied claims return 403 with the lease state UNCHANGED (the test asserts the pre-transition ordering deterministically). Interlock shipped: the claiming `worker_id` resolved against the runner registry — the `worker_id == runner_id` convention already load-bearing for BYOC pool enforcement; no RAB on file ⇒ legacy pass (pre-k-053 runners unaffected). Token VALUE verification against an issuing authority is explicitly out of scope and documented at the check site — this enforces the ADVERTISEMENT law, which is what rab/1.0 is.

## k-059 — no more anonymous capability downgrade

`POST /v1/runners/{id}/abi` is behind `requireBearer`. The k-054 pin flipped to `TestAdversary_UnauthenticatedRABDowngradeBlocked`: anonymous downgrade now 401s and the victim's RAB is unchanged; an authenticated cross-runner rewrite still succeeds — pinned as the residual per-action-authz gap (auth.go's own 'NOT a substitute for per-action authz' boundary; next slice if it becomes load-bearing).

## Live runtime-verify (prod :18191, post-deploy 06:38)

```
register:201
anon-abi-POST:401                     ← k-059 closed
auth-abi-POST:200  (control+required)
claim WITHOUT control token:  HTTP 403 {"error":"control_token_required", ...}   ← k-058 law live
claim WITH    control token:  HTTP 201 lease granted                              ← gate precedes state change
meta-linkage: True | caps: ['observe','control']                                  ← k-054 B fix live
```
Smoke work cancelled after the test; zero errors/panics in journal since restart.

## Gates (stacked integration worktree, per k-050/k-054 doctrine)

- go build / go vet / full `go test ./...` — green on the STACK, not just per-branch
- e2e suite green; Adversary+ClaimGate+ABI suites green together; internal/worker green
- gofmt clean on all touched files
- exact-head CI `works-execution=success`, post-merge main verified by direct grep of each fix on origin/main

## What's NOT in this PR

- Per-action authz (which token may rewrite WHICH runner) — open by design, pinned by test
- Token value verification (issuing-authority check) — out of scope per k-058 docstring
- services/publisher pre-existing race (k-impl-021; non-race is the project gate)
