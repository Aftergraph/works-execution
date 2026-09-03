# v0.3.2 — integration wave: secrets, obslaw, abi wired into runtime

**Tag:** v0.3.2 → main `e217869` (squash-merged PR #34)
**Range since v0.3.1:** `0c6e2c2..e217869` (1 PR, 4 slices, 1 composition-adversary gate)
**Status:** shipped, merged, deployed, runtime-verified

## What landed

Three pure-law kernels from v0.3.1 (`packages/{secrets,obslaw,abi}`) are now wired into the services that have a real runtime use for them. Each slice landed on its own branch with disjoint file ownership, then stacked for composition verification. The composition-adversary sweep (k-054) ran in a fresh context against the stacked tree and pinned three real, reproducible seams — two fixed in this PR, one pinned as a separate auth-surface question.

| Slice | Branch | Commit | Files | New tests |
|---|---|---|---|---|
| k-051 secret-ref runtime | v032/secret-runtime | e7e3504f | services/runner/{secrets,secrets_test}.go (new), real.go (+34/-3) | 9 funcs / 19 cases |
| k-052 obslaw evidence+audit | v032/obs-evidence | b2d0fa1c | services/evidence/obslaw.go, services/audit/obslaw.go (new), bundle.go +16, cloud_events.go +9 | 15 (9+6) |
| k-053 rab/1.0 registry | v032/abi-registry | ae996bc9 | services/api/runner_abi.go (new, 320 lines), runner_abi_test.go, runner_register.go +8, api.go +13 | ~60 cases |
| k-054 composition-adversary | v032/adversary | d17b537b | services/api/adversary_test.go (new, 312 lines) | 3 (1 pin + 2 regressions) |

## k-051 — secret refs at execution time (ADR-0022)

`SecretResolver.ResolveEnv(ctx, scope, env)` resolves any `secret://…` value in `Step.Env` immediately before `exec.CommandContext`. Values land only in `cmd.Env`; never in `Result`/`StepResult`, never in any error string, never cached. `NewEnvSecretResolver` is an adapter over `packages/secrets.EnvResolver` (nil lookup → `os.LookupEnv`, fresh kernel struct per call). Nil resolver is byte-for-byte legacy pass-through (pinned by test).

**Deviation:** `cmd/works-worker` wiring is honestly documented as a non-trivial follow-up: there is no one-line enrollment/Run call site; the worker today uses `internal/worker/worker.go` and `runner` is not on that path. Stray `secret://…` strings in the worker are inert (never resolved, cached, or echoed into evidence/audit); surfacing the wire is the integrator's call.

## k-052 — obslaw boundary in evidence and audit (ADR-0024)

**Evidence side.** `(*Bundle).ObsLawRecord()` projects a bundle into the law (`Kind=evidence`, `Signed=true`, `Trimmable=false`); `CitesHash` is empty by design — the kernel law requires 64-hex, but the wire-side `bundle_id` is `evb_` + sha256[:32hex] and the full digest is not retained on the struct (intentional). Documented in the source. `AttestBundle(b, key)` layers a law-level `Attested{Record,Signature}` via `obslaw.Verifier` in addition to (never replacing) the existing `Signatures`. A fail-fast law check is wired into the `Produce` hot path — untriggerable today by construction, but it is the real choke point that catches future drift.

**Audit side.** `LawRecord(e)` projects a `CloudEvent` as `Kind=event, Signed=false`. `CheckEvent` is wired into `SQLiteEmitter.Emit` as a fail-fast assertion. Tests prove the teeth: a hand-crafted `Kind=event + Signed=true` record is rejected at the kernel with `ErrEventCannotBeSigned`.

Zero schema edits. Zero behavior change for existing consumers.

## k-053 — rab/1.0 in the runner registry (ADR-0012/0014)

`POST /v1/runners/{id}/abi` publishes a validated `rab/1.0` advertisement, `GET` reads it, `POST …/negotiate` returns the law-level `(granted_caps, control_token_required, err)`. Routes are mounted in `api.go` AFTER the existing `/v1/runners/{id}` prefix handler so the more-specific patterns win. The control-token law (caps contains `control` ⇒ `control_token_required` must be true) is enforced at POST with the kernel's own error message and is **visible on the wire** in negotiate responses so callers can gate privileged operations. Identity-first order: a RAB requires a registered identity (404 if not). Overwrite is idempotent upsert; no DELETE.

## k-054 — composition-adversary gate

Fresh-context sweep against the stacked tree. Three real seams found:

**A — `cloneRAB` shallow copy of `Extra` (now fixed).** The original implementation did `out.Extra[k] = v`, which is a **shared reference** for any nested map/slice — the N-1 kernel shape accepts arbitrary JSON, so any nested object aliased the stored advertisement in both directions. Reader mutating a record handed out by `getABI`/`getRuntimeABI`/`listABI` silently rewrote what every future GET served. **Fix:** `cloneRAB` now re-canonicalises `Extra` through the kernel via `json.Marshal + json.Unmarshal` into a fresh `map[string]any` — a complete deep clone of every JSON value the kernel accepts. `TestAdversary_RABCopyOutPromiseBrokenBySharedNestedExtra` was a PIN test; flipped to a regression assertion in this commit.

**B — `RuntimeABI.MarshalJSON` flattened-with-overlay (now fixed).** Linkage keys `runner_id` and `registered_at` were placed at the top level of the response, silently overwriting any N-1 advertised field whose name happened to collide. **Fix:** linkage moved under the namespaced `rab_runtime_meta` object so the advertised document round-trips bit-for-bit while bookkeeping stays discoverable. `TestAdversary_RABFlattenDestroysCollidingAdvertisedField` was a PIN; flipped to a regression assertion.

**C — unauthenticated RAB downgrade (pinned, not fixed).** k-053's POST /abi inherits the pre-existing zero-auth runner registry surface (k-002's enrollment model). Any client can POST a smaller RAB for any runner id and the victim's own `negotiate(control)` immediately returns an empty grant. Pinned as `TestAdversary_UnauthenticatedRABDowngradeOfForeignRunner` — the fix is a separate auth-surface decision and not in this slice's domain.

**Surfaces verified clean (no test, "just works" reported):** nil-resolver legacy pass-through on the only runner exec path; k-051 unwired from works-worker but stray `secret://` is inert; `Produce` rejects empty HMACKey without panic; law hook cannot leak on error paths; `Attested` correctly lives outside the bundle JSON; `evt_`/`evb_` ID prefixes are disjoint; registry RWMutex is consistent; POST /abi before register is a clean 404.

## Gates (octopus-merge in `/tmp/wt-v032`)

- `go build ./...` ✓
- `go vet ./...` ✓
- `gofmt -l` empty on every file we touched in this PR ✓
- `go test ./... -count=1` (non-race) — full suite green, 0 FAIL
- `go test -tags=e2e ./e2e/...` green
- `go test ./services/api/ -count=1 -run 'Adversary'` — 3/3 pass

## Live runtime-verify (post-deploy 06:08)

1.  **Enroll + Register:** Worker enrolled, identity minted (`wrkr_98f0...`), registered with `trust_class: standard`.
2.  **RAB POST (k-053):** Accepted `rab/1.0` with N-1 fields (`spec`, `x_meta`). Response includes the advertised document intact.
3.  **RAB GET (k-054 fix verified):** The response returns the full advertised document **including** the nested `spec` object and `x_meta`. Server linkage (`runner_id`, `registered_at`) is namespaced under `rab_runtime_meta` so user fields round-trip safely.
4.  **Negotiate (k-053 law):** Requested `["observe", "control"]`. Returned `{"caps": ["observe"], "control_token_required": false}`. Correctly granted only `observe` (since the RAB didn't advertise `control`) and correctly reported `false` for the token requirement. Fail-closed on `control`.
5.  **Brain surface:** Still enabled (`Brain surface enabled` in logs).

## Roll-back

Each slice is a single-purpose PR-of-one-branch. Reverting this PR is `git revert`; the three pure-law kernels from v0.3.1 are untouched and remain usable.

## What's NOT in this PR

- No new schema migration.
- No wire-format change for any pre-existing endpoint.
- No fix for the k-002 auth surface (separate decision).
- No cmd/works-worker wiring for k-051 (documented deviation; integrator's call).
