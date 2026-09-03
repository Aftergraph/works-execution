# v0.3.1 — pure-law kernels and a registry-vs-repo auditor

**Tag:** v0.3.1 → main `099a315` (squash-merged PR #33)
**Range since v0.3.0:** `0ed07ca..099a315` (1 PR, 4 disjoint slices, 1 octopus)
**Status:** shipped, merged, not yet deployed (pure-domain; runtime-verify optional)

## What landed

Four slices, all `packages/*` or `internal/*` — zero overlap with `services/`,
zero schema migration, zero wire-format change. The packages are kernel
laws (law-once-decided, then enforced wherever they are wired in later
integration PRs). The audit tool surfaces drift between the standards
registry and the actual files in `docs/standards/mappings/`.

| Slice | Package | ADR | Tests | Commit |
|---|---|---|---|---|
| k-046 | `packages/secrets` | ADR-0022 (value-never-serializes) | 60 | `1047377` |
| k-047 | `packages/obslaw` | ADR-0024 (obs vs evidence type-law) | 30 | `24f5c2c` |
| k-048 | `packages/abi` | ADR-0012/0014 (rab/1.0) | 60 | `4501b90` |
| k-049 | `internal/standards/audit` + `cmd/works-standards audit` | (tooling) | 9 funcs | `7aa3159` |

## What each slice is for

### k-046 — `secret.ref/1.0` (ADR-0022)

The law: payloads and audit records may only ever carry a `secret://`
REF; the kernel is the sole component that resolves ref→value, at
execution time, inside the worker's env. The resolved value is
radioactive — it must never be serialised (not to audit, not to the
store, not to logs, not to error messages).

- Typed `Ref` (compile-time refuses `*Ref` ↔ `string` confusion).
- `EnvResolver` mapping (documented): `secret://provider/name` under
  scope `""` → `SECRET_<PROVIDER>_<NAME>` (dashes → underscores;
  colons normalised because POSIX env names cannot contain colons).
- `Grant.Redeem(ctx, Resolver, workID)`: returns the value straight to
  the caller — never caches, never logs, never puts it in the returned
  error.
- Leak test sets a sentinel value in env, forces every failure path,
  sweeps every `error.Error()` string for the sentinel. Zero leaks.

### k-047 — observability-vs-evidence type-law (ADR-0024)

The law: events (logs/metrics) are operationally disposable — trimmable,
samplable, not signed. Evidence is a CLAIM about what happened — signed,
never trimmable. A signed event is a category error: observability
pretending to attest.

- Constructors make illegal states unrepresentable: `NewEvidence` refuses
  `signed=false`; events cannot produce `signed=true`.
- `Record` is the frozen schema shape (no Signature field — adding one
  would violate `additionalProperties:false`). Signatures live in
  `Attested` beside the record.
- `Verifier`: HMAC-SHA256 over canonical JSON of the record MINUS the
  signature (sign the shape, not the bytes). Constant-time compare.
- `TrimPolicy(kind)` exports the law as a tiny function so runbooks/UIs
  can ask.
- Tamper tests: flip one bool, verify fails. Value-copy audit: `Sign`
  must not mutate the input record.

### k-048 — `rab/1.0` runtime ABI (ADR-0012/0014)

The lower half of the CPI/RAB split. Where CPN is "the kernel's view
of a whole computer" (Handshake/Provision/Exec/Snapshot/Teardown),
RAB is the capability advertisement a runtime (browser RT, code RT,
desktop RT) publishes into the registry — WHAT it can do, not WHO owns
it.

- Closed universe of 5 caps: `screenshot, input, record, observe,
  control`. A 6th is a law violation.
- The control-token law the schema cannot express gets Go teeth:
  `caps` contains `control` ⇒ `control_token_required = true`,
  fail-closed. The schema is a partial guard; the Go law closes the gap.
- N-1 unknown-field tolerance via `Extra map[string]any` round-trip
  (forward-compat per proto.charter/1.0 ADR-0021; unknown FIELDS
  tolerated, unknown ABI VERSION rejected fail-closed).
- `Negotiate(requested)` preserves requested order, returns
  `(caps, controlTokenRequired, err)` so callers can gate control ops
  with one call.

### k-049 — standards coverage-audit tool

`works-standards audit [--repo-root=PATH] [--fail-on-warn]`

Cross-checks `docs/standards/registry.json` claims against filesystem
reality. Five check kinds, each a small pure function:

- `missing-mapping-file` (High) — registry row claims mapping `X` but
  `os.Stat(X)` fails.
- `orphan-mapping` (High) — file under `docs/standards/mappings/`
  referenced by NO registry row.
- `empty-status` (Medium) — row status is empty/whitespace.
- `duplicate-id` (Medium) — two rows share an id.
- `stale-generated-at` (Medium) — `generated_at` > 30d old (injectable
  `now` for deterministic tests).

Real-repo run at PR-open time:

```
$ ./bin/works-standards audit
orphan-mapping docs/standards/mappings/ai.md ...
orphan-mapping docs/standards/mappings/ci.md ...
orphan-mapping docs/standards/mappings/identity.md ...
orphan-mapping docs/standards/mappings/innovation.md ...
orphan-mapping docs/standards/mappings/observability.md ...
orphan-mapping docs/standards/mappings/performance.md ...
orphan-mapping docs/standards/mappings/platform-build.md ...
orphan-mapping docs/standards/mappings/platform.md ...
orphan-mapping docs/standards/mappings/policy.md ...
orphan-mapping docs/standards/mappings/supply-chain.md ...
10 findings found
exit 1
```

The 10 orphans are real drift: the mapping files were authored, but no
registry row links to them. Fixing this is a docs-wave concern, NOT a
k-049 concern — the tool's job is to make the drift loud, not to hide
it. Whoever owns the registry rows is the owner of the follow-up.

## Why a kernel package, not a runtime check?

Because every layer that touches observability or evidence needs to ask
the same question in the same way, and `services/audit/`,
`services/evidence/`, and the work journal each have their own code
paths. The package centralises the invariant so an integrator wires it
consistently. Future integration PRs (e.g. secrets resolver in
`cmd/worker/`, obslaw in the evidence bundle builder, abi in the
runtime registry) are the next step.

## Gates (octopus-merge in this worktree)

- `go build ./...` ✓
- `go vet ./...` ✓
- `go test ./... -count=1` ✓ (133 new subtests added; all green)
- `make build && ./bin/works-standards audit` runs clean on real repo
  (exits 1 with 10 High findings, exactly as designed)

## Roll-back

Each package is a fresh `packages/<name>/*` and the audit tool is
`internal/standards/audit*.go` + a subcommand line in
`cmd/works-standards/main.go`. Reverting the octopus PR is a single
`git revert` — none of the four slices shares files with anything in
`services/` or with the v0.3.0 brain wave.

## What this release does NOT do

- No wire-format change. No API surface change. No schema migration.
- No runtime integration of the new packages. The kernels are
  available; whoever wires them does so behind a follow-up slice.
- No registry content change. The 10 orphan-mapping findings are
  preserved on purpose — fixing them is a docs owner concern, not a
  release concern.
