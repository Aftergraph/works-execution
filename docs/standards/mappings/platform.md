# Platform (Action / CI) Standards — Per-Standard Mapping

> **Purpose.** Per-standard mapping for the **22 internal Action/CI
> standards** in [`docs/standards/registry.json`](../registry.json) where
> `domain == "platform"`. These are the user-mandated *new* standards
> (rows 1661–1989 in the registry) that the platform will implement across
> slices 3–5. They are the platform's own contract — the spine of how
> works-execution handles Actions, runners, evidence, and policy.
>
> **Method.** The §14 implementation rule from the user-mandated
> standards charter: (1) determine applicability, (2) map to system
> requirements, (3) identify gaps, (4) prioritize by risk and leverage,
> (5) recommend the highest-value actionable gap with an explicit file
> path. Each standard below carries all five.
>
> **Ground truth for current state.** `docs/standards/registry.json`
> rows 1661–1989 (status, enforcement_point, evidence). Slice-1 (commit
> `d3db1d1`) and slice-2 (commit `dab84f2`) shipped: Work primitive,
> SQLite store, HTTP API (`cmd/works-api`), CLI (`cmd/works`), polling
> subprocess worker (`internal/worker/worker.go`), lease-based
> scheduling, worker-loss recovery, log streaming. The Go validator
> at `internal/standards/standards.go` embeds the five existing JSON
> Schemas (action-manifest, evidence-bundle, failure-classification,
> runner-identity, workflow-provenance) from `docs/standards/schemas/`.
> The agent declaration is `docs/agents/worker.md`.
>
> **Companion documents.** `mappings/security.md`, `mappings/identity.md`,
> `mappings/policy.md`, `mappings/ssd.md`, `mappings/quality.md`,
> `mappings/supply-chain.md`, `mappings/observability.md`. These 22
> platform standards overlap with security (hermetic, zero-secret),
> identity (runner-identity), policy (policy-enforced-action),
> observability (evidence-first), and supply-chain (provenance) — cross-
> references are made inline so each standard here is treated as a
> single integrated whole, not a duplicate of a deeper document.

---

## Table of contents

| § | Standard | Status | Slice 3 priority |
|---|----------|--------|------------------|
| 1 | `platform-portable-action` (#109) | PLANNED | **P1** |
| 2 | `platform-action-manifest` (#110) | PLANNED | **P1** |
| 3 | `platform-hermetic-execution` (#111) | PLANNED | **P1** |
| 4 | `platform-reproducible-execution` (#112) | PLANNED | P2 |
| 5 | `platform-evidence-first` (#113) | PLANNED | **P1** |
| 6 | `platform-zero-secret` (#114) | PLANNED | P2 |
| 7 | `platform-content-addressed` (#116) | PARTIAL | **P1** |
| 8 | `platform-intelligent-scheduling` (#115) | PLANNED | P2 |
| 9 | `platform-self-healing` (#117) | PLANNED | P2 |
| 10 | `platform-ai-failure-intel` (#118) | PLANNED | deferred (slice 5+) |
| 11 | `platform-continuous-verification` (#119) | PLANNED | P3 |
| 12 | `platform-universal-compat` (#120) | PLANNED | P3 |
| 13 | `platform-runner-identity` (#121) | PLANNED | **P1** |
| 14 | `platform-workflow-provenance` (#122) | PLANNED | **P1** |
| 15 | `platform-action-attestation` (#123) | PLANNED (bundled #122) | (bundled) |
| 16 | `platform-execution-evidence` (#124) | PLANNED (bundled #113) | (bundled) |
| 17 | `platform-policy-enforced-action` (#125) | PLANNED | **P1** |
| 18 | `platform-runtime-isolation` (#126) | PLANNED | P2 |
| 19 | `platform-portable-cache` (#127) | PLANNED | P3 |
| 20 | `platform-cross-provider` (#128) | PLANNED (bundled #120) | (bundled) |
| 21 | `platform-autonomous-remediation` (#129) | PLANNED | P3 |
| 22 | `platform-continuous-innovation` (#130) | PLANNED | P3 |

**Final traceability table** at the bottom maps every `standard_id` to
its section and to the row in `registry.json`. The "**8 to IMPLEMENT in
slice 3**" subset is detailed at the end with explicit file paths and
acceptance criteria.

---

## 1. `platform-portable-action` (#109) — Portable Action Standard

**Applicability.** Applicable. Every Action the platform runs must be
portable across local, Linux, Windows, macOS, containers, VMs, K8s, VPS,
cloud runners, and edge nodes without rewriting business logic. This is
the platform's portability contract.

**System requirements it maps to.**
- Action manifest declares `runtime.os` and `runtime.arch` (schema
  exists at `docs/standards/schemas/action-manifest.schema.json`).
- Scheduler selects workers whose declared capabilities match
  (`internal/scheduler/` — slice 3).
- Worker is a static Go binary that runs unmodified on every target.
- Toolchain is declared and resolved against the runner's capability
  set, not hard-coded in the action.

**Current status — PLANNED.** `registry.json` row 1661:
`implementation: "Slice 3: capability manifest #110 carries os/arch;
scheduler selects capable worker; workers in Go run anywhere."`
`enforcement_point: "internal/scheduler/capability.go."`
`test: "tests/capability/portability_test.go."`
`evidence: "PLANNED"`.
The Go binary in `cmd/works-worker/` is already portable (single static
binary, no cgo in slice 2), so the *runtime* portability is in place;
the *scheduling* portability (matching work to capable worker) is the
slice 3 work.

**Gap.** `internal/scheduler/` does not exist; `cmd/works-worker/main.go`
does not advertise its os/arch capabilities at enrollment; the API does
not filter `/v1/workers/ready` by capability. The portability test
surface (`tests/capability/portability_test.go`) is PLANNED, not
present.

**Highest-leverage next step.** Add capability-aware scheduling so the
existing portability is *exposed* — the Go binary already runs anywhere,
so the only missing piece is the scheduler reading the action's
declared `runtime.os` / `runtime.arch` and the worker's declared
capabilities and matching them.

- **File to create:** `internal/scheduler/capability.go` (capability
  matching; selector against `runner-identity.schema.json`).
- **File to modify:** `internal/worker/worker.go` (advertise os/arch +
  labels at enrollment; honor `runtime.toolchain`).
- **File to modify:** `services/api/leases.go` (filter `/v1/workers/ready`
  by capability match).
- **File to create:** `tests/scheduler/capability_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-portable-action` |
| Registry §line | 1661 |
| Registry status | PLANNED |
| Linked schemas | `action-manifest.schema.json`, `runner-identity.schema.json` |
| Linked agent decl. | `docs/agents/worker.md` §Capabilities, §Tools |
| Enforcement point (planned) | `internal/scheduler/capability.go` |
| Test path (planned) | `tests/capability/portability_test.go` |

---

## 2. `platform-action-manifest` (#110) — Action Capability Manifest Standard

**Applicability.** Applicable. Every Action that runs through
works-execution must carry a manifest declaring id, version, runtime,
image, inputs, outputs, CPU, memory, GPU, env, FS, network, secrets,
permissions, timeout, retries, cache, artifacts, side effects, and
rollback. This is the load-bearing contract for §1, §3, §6, §11, §13,
§17, §21.

**System requirements it maps to.**
- Schema at `docs/standards/schemas/action-manifest.schema.json` already
  declares all 17 fields the standard requires (verified by inspection
  of the file: `action_id`, `version`, `runtime`, `inputs`, `outputs`,
  `resources`, `environment`, `filesystem`, `network`, `secrets`,
  `permissions`, `timeout_seconds`, `retries`, `cache`,
  `side_effects`, `rollback`, `evidence_required`).
- Validator embedded by `internal/standards/standards.go` (load-once,
  schema-keyed-by-`$id`).
- Admission hook in API (`services/api/admission.go` — PLANNED).
- Manifest is content-addressed (sha256 of canonicalized JSON) and
  stored next to the action version.

**Current status — PLANNED.** `registry.json` row 1676:
`implementation: "Slice 3: internal/manifest package + JSON Schema
2020-12 + validator in admission."`
`enforcement_point: "services/api/admission.go."`
`test: "tests/manifest/."`
`evidence: "docs/standards/schemas/action-manifest.schema.json"` (the
schema is already in the repo, embedded by `standards.go`).
**First-pass schema already exists and compiles** — that is why this
standard has the highest leverage in slice 3: half the work is done.

**Gap.** No `internal/manifest/` package; no admission enforcement
(`services/api/admission.go` does not exist); no content-addressed
manifest storage; no test surface under `tests/manifest/`. The schema
itself is unused by any code path.

**Highest-leverage next step.** Wire the existing schema into the
admission path. Because the schema is already loadable via
`standards.Load()` and `standards.ValidateBytes()`, the next step is a
thin adapter + admission call — *not* schema authoring.

- **File to create:** `internal/manifest/manifest.go` (typed Go struct
  mirroring the schema; `Validate(b []byte) error`; `ContentAddress()
  string` returning `sha256:` of canonical JSON).
- **File to create:** `services/api/admission.go` (calls
  `standards.ValidateBytes("action-manifest.schema.json", body)` on
  every action-create; rejects on failure).
- **File to create:** `tests/manifest/manifest_test.go`,
  `tests/manifest/admission_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-action-manifest` |
| Registry §line | 1676 |
| Registry status | PLANNED |
| Existing schema | `docs/standards/schemas/action-manifest.schema.json` (already embedded) |
| Linked agent decl. | `docs/agents/worker.md` §Identity, §Permissions |
| Enforcement point (planned) | `services/api/admission.go` |
| Test path (planned) | `tests/manifest/` |

---

## 3. `platform-hermetic-execution` (#111) — Hermetic Execution Standard

**Applicability.** Applicable. Default network=deny, secrets=none,
FS=isolated, permissions=minimal. Broader authority must be explicit
and policy-approved. The agent declaration
(`docs/agents/worker.md §Permissions, §Prohibited actions`) already
codifies this as the worker's default posture.

**System requirements it maps to.**
- Subprocess sandboxing (`internal/sandbox/hermetic.go` — PLANNED).
- Default network deny enforced at the subprocess layer (today only at
  the worker HTTP-client level).
- Admission rejects manifests whose `permissions` array asks for
  `network`/`privileged`/`secrets` without an explicit `policy_id`.
- Capability manifest `network.policy` defaults to `deny`
  (already in the schema).

**Current status — PLANNED.** `registry.json` row 1691:
`implementation: "Slice 3: default-deny subprocess sandbox; admission
rejects undeclared capabilities."`
`enforcement_point: "internal/sandbox/hermetic.go."`
`test: "tests/hermetic/."`
`evidence: "PLANNED"`.
The schema-level default (`network.policy=deny`) is in place; the
subprocess-level enforcement is not.

**Gap.** `internal/sandbox/` does not exist; the worker today runs
`sh -c <command>` with the host network namespace and the host
filesystem; there is no netfilter / seccomp / capability-drop in front
of the subprocess. `tests/hermetic/` does not exist.

**Highest-leverage next step.** Implement the hermetic *network* layer
first — it is the simplest and highest-impact (deny-by-default outbound
DNS + TCP from the subprocess). Filesystem isolation and seccomp are
layered on top in the same slice.

- **File to create:** `internal/sandbox/hermetic.go` (subprocess wrapper
  that sets `CLONE_NEWNET`, drops capabilities, sets `NO_NEW_PRIVS`,
  redirects `/etc/resolv.conf` to `/dev/null`).
- **File to modify:** `internal/worker/worker.go` (route every
  `exec.CommandContext` through the hermetic wrapper; honor the
  action's `network.allow_list` when policy=allow-list).
- **File to create:** `tests/sandbox/hermetic_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-hermetic-execution` |
| Registry §line | 1691 |
| Registry status | PLANNED |
| Linked schemas | `action-manifest.schema.json` (network.policy, secrets, permissions) |
| Linked agent decl. | `docs/agents/worker.md` §Permissions, §Prohibited actions |
| Enforcement point (planned) | `internal/sandbox/hermetic.go` |
| Test path (planned) | `tests/hermetic/` |

---

## 4. `platform-reproducible-execution` (#112) — Reproducible Execution Standard

**Applicability.** Applicable. Identical source + Action + inputs +
deps + toolchain + env → identical or *explainably equivalent* results.
This is the "rerun the same thing and get the same answer" contract.

**System requirements it maps to.**
- A canonical fingerprint over (source, action, inputs, env, deps,
  toolchain) — sha256 of canonicalized JSON.
- The fingerprint is the cache key (`platform-portable-cache` §19) and
  the cache-hit signal.
- "Explainably equivalent" is captured by the workflow provenance
  (`platform-workflow-provenance` §14) recording `reproducible: bool`
  per `workflow-provenance.schema.json` `metadata.reproducible`.

**Current status — PLANNED.** `registry.json` row 1706:
`implementation: "Slice 3: fingerprint = sha256(source + action +
inputs + env); cache key includes fingerprint; equivalent results
explained via fingerprint."`
`enforcement_point: "internal/fingerprint/."`
`test: "tests/fingerprint/."`
`evidence: "PLANNED"`.

**Gap.** No `internal/fingerprint/` package; cache keys today (slice 2)
are content-derived only for *artifacts*, not for the full execution
context; no deterministic environment capture (env vars, cwd, umask).

**Highest-leverage next step.** Land the fingerprint primitive as a
*pure* package (no I/O) so it is trivially testable and reused by
portable-cache, evidence-bundle, and workflow-provenance in the same
slice.

- **File to create:** `internal/fingerprint/fingerprint.go` (canonical
  JSON encode + sha256; inputs: source rev, action manifest, inputs,
  env, deps lockfile, toolchain pin).
- **File to create:** `tests/fingerprint/fingerprint_test.go` (golden
  vectors; canonicalization is stable across runs).

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-reproducible-execution` |
| Registry §line | 1706 |
| Registry status | PLANNED |
| Linked schemas | `workflow-provenance.schema.json` (`metadata.reproducible`), `evidence-bundle.schema.json` (`bundle_id`) |
| Linked agent decl. | `docs/agents/worker.md` §Evidence requirements |
| Enforcement point (planned) | `internal/fingerprint/` |
| Test path (planned) | `tests/fingerprint/` |

---

## 5. `platform-evidence-first` (#113) — Evidence-First CI Standard

**Applicability.** Applicable. Every execution generates a durable
evidence bundle: id, timestamps, actor, source, workflow, Actions,
runner, inputs, env, logs, tests, security findings, artifacts,
provenance, signatures, final result.

**System requirements it maps to.**
- Schema at `docs/standards/schemas/evidence-bundle.schema.json` already
  encodes the bundle shape (verified: `bundle_id`, `work_id`, `runner`,
  `created_at`, `summary`, `components.{attempts,artifacts,evidence,
  leases}`, `signatures`).
- Producer at `services/evidence/` (PLANNED).
- Retrieval endpoint `GET /v1/works/{id}/evidence` (declared in
  `docs/agents/worker.md` §Evidence requirements).
- Worker emits evidence records on every `/complete` (slice 2 already
  emits attempts + artifact sha256 — the bundle wraps these).

**Current status — PLANNED.** `registry.json` row 1721:
`implementation: "Slice 3: services/evidence produces a signed JSON-LD
or CBOR bundle per work, with sha256 manifest; bundle is fetched via
GET /v1/works/{id}/evidence."`
`enforcement_point: "services/evidence/."`
`test: "tests/evidence/."`
`evidence: "docs/standards/schemas/evidence-bundle.schema.json"` (the
schema already exists and is embedded by `standards.go`).
**Schema exists and is loadable** — same posture as §2.

**Gap.** `services/evidence/` does not exist; worker
(`internal/worker/worker.go`) emits attempt + artifact records via
`/v1/leases/{id}/complete` but does not yet produce a signed bundle;
no `GET /v1/works/{id}/evidence` route; no Sigstore-style signing path
in the API.

**Highest-leverage next step.** Build the bundle assembler from the
records the slice 2 worker already emits — do *not* change the worker's
emission shape. The bundle is a deterministic aggregation of those
records.

- **File to create:** `services/evidence/bundle.go` (read attempt,
  artifact, evidence, lease records from the store; assemble per
  `evidence-bundle.schema.json`; `bundle_id` = sha256 over canonical
  JSON; sign with ed25519 key from API host).
- **File to modify:** `services/api/api.go` (add
  `GET /v1/works/{id}/evidence`).
- **File to create:** `tests/evidence/bundle_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-evidence-first` |
| Registry §line | 1721 |
| Registry status | PLANNED |
| Existing schema | `docs/standards/schemas/evidence-bundle.schema.json` (already embedded) |
| Linked agent decl. | `docs/agents/worker.md` §Evidence requirements |
| Enforcement point (planned) | `services/evidence/` |
| Test path (planned) | `tests/evidence/` |

---

## 6. `platform-zero-secret` (#114) — Zero-Secret CI Standard

**Applicability.** Applicable. No static cloud credentials; prefer OIDC
federation, SPIFFE workload identity, short-lived tokens, JIT
credentials, scoped credentials. The agent declaration
(`docs/agents/worker.md §Prohibited actions`) already forbids
"persist long-lived credentials anywhere on disk".

**System requirements it maps to.**
- Worker enrollment uses OIDC token exchange (or dev-mode SPIFFE-like
  signed enrollment token).
- API rejects static API keys (today the worker uses a static token —
  `services/api/api.go` `Authorization` header check).
- Action manifest `secrets[]` entries are short-lived (the schema
  enforces `ttl_seconds` ≤ 3600).
- Secret material is never written to disk; only injected into the
  subprocess environment for the duration of one execution.

**Current status — PLANNED.** `registry.json` row 1736:
`implementation: "Slice 3: worker enrollment uses OIDC token exchange
(or dev-mode SPIFFE-like signed enrollment token); API rejects static
API keys."`
`enforcement_point: "services/api/auth.go."`
`test: "tests/auth/zero_secret_test.go."`
`evidence: "PLANNED"`.

**Gap.** Worker today authenticates with a static bearer token; the API
issues and verifies a static token (`services/api/api.go`); no OIDC
federation or SPIFFE-style enrollment; no JIT secret broker.

**Highest-leverage next step.** Replace the static-token path with a
short-lived signed enrollment token (dev-mode SPIFFE surrogate). This
single change unblocks the worker-side adoption story even before
production OIDC is wired, and makes the static-token test explicit.

- **File to create:** `services/api/auth.go` (OIDC exchange handler +
  enrollment-token issuer; rejects static tokens by default with a
  documented `WORKS_AUTH_DEV_TOKEN` escape hatch).
- **File to modify:** `internal/worker/worker.go` (use the enrollment
  token; refresh on TTL).
- **File to create:** `tests/auth/zero_secret_test.go` (rejects static
  keys; accepts signed enrollment tokens).

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-zero-secret` |
| Registry §line | 1736 |
| Registry status | PLANNED |
| Linked schemas | `action-manifest.schema.json` (`secrets[].ttl_seconds` ≤ 3600) |
| Linked agent decl. | `docs/agents/worker.md` §Prohibited actions, §Allowed actions |
| Enforcement point (planned) | `services/api/auth.go` |
| Test path (planned) | `tests/auth/zero_secret_test.go` |

---

## 7. `platform-content-addressed` (#116) — Content-Addressed Everything Standard

**Applicability.** Applicable. Identify immutable content by digest:
source, deps, Actions, envs, caches, artifacts, workflows.

**System requirements it maps to.**
- Artifacts already sha256-named in slice 1 (`services/work/store/
  store.go`).
- Slice 3 extends the same pattern to: source revisions, action
  manifests, environment captures, cache entries (via §4 fingerprint
  and §19 portable-cache).
- Workflow provenance records material digests
  (`workflow-provenance.schema.json` `materials[].digest.sha256`).

**Current status — PARTIAL.** `registry.json` row 1751:
`implementation: "Already partial in slice 1 (artifacts sha256).
Slice 3: extend to sources, actions, envs, cache keys."`
`enforcement_point: "internal/fingerprint/."`
`test: "tests/fingerprint/."`
`evidence: "services/work/store/store.go (slice 1)"` — verified by
inspection: the slice 1 store hashes artifacts on write.

**Gap.** Only artifacts are content-addressed today; sources, actions,
environments, and cache keys are not. The fingerprint package
(`internal/fingerprint/`) does not exist.

**Highest-leverage next step.** Reuse the §4 fingerprint package for
everything non-artifact — content-addressed sources, actions, envs, and
caches all key off the same primitive. No new package is needed; the
work is wiring.

- **File to modify:** `services/work/store/store.go` (store source rev
  by digest; store action manifest by digest; store env capture by
  digest).
- **File to create:** `internal/fingerprint/fingerprint.go` (jointly
  with §4; share the same package).

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-content-addressed` |
| Registry §line | 1751 |
| Registry status | PARTIAL (only artifacts in slice 1) |
| Linked schemas | `evidence-bundle.schema.json` (`artifactRef.digest`), `workflow-provenance.schema.json` (`materials[].digest`) |
| Linked agent decl. | `docs/agents/worker.md` §Evidence requirements (artifact sha256) |
| Existing evidence | `services/work/store/store.go` (slice 1) |
| Enforcement point (planned) | `internal/fingerprint/` |
| Test path (planned) | `tests/fingerprint/` |

---

## 8. `platform-intelligent-scheduling` (#115) — Intelligent Scheduling Standard

**Applicability.** Applicable. Runner selection considers OS, arch, CPU,
RAM, GPU, trust, classification, region, queue load, cache locality,
latency, cost, energy.

**System requirements it maps to.**
- Scheduler scoring per `scheduler_design.md` (PLANNED) with explicit
  explainability record per assignment (which signals contributed how
  much to the score).
- Inputs: runner-identity (`runner-identity.schema.json`) + live
  load + cache locality (from §19 portable-cache) + policy
  classification.
- Output: ranked runner list + the per-decision explanation that
  travels into the evidence bundle.

**Current status — PLANNED.** `registry.json` row 1766:
`implementation: "Slice 3: scheduler scoring per scheduler_design.md
with explainability record per assignment."`
`enforcement_point: "internal/scheduler/."`
`test: "tests/scheduler/."`
`evidence: "PLANNED"`.

**Gap.** No scheduler package; selection today is *first ready wins*
(API FIFO in `services/api/leases.go`); no scoring; no explainability
record.

**Highest-leverage next step.** Land the scoring + explainability
record as a single unit — they are inseparable. Without the record,
operators cannot debug "why did my work run on that runner?", which is
the single most-asked scheduling question.

- **File to create:** `internal/scheduler/scoring.go` (weighted scoring
  across os/arch fit, resource fit, trust class, cache locality, queue
  load; returns ranked list).
- **File to create:** `internal/scheduler/explain.go` (records per-
  signal contribution; persisted into the evidence bundle).
- **File to modify:** `services/api/leases.go` (consume ranked list;
  persist explanation).
- **File to create:** `tests/scheduler/scoring_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-intelligent-scheduling` |
| Registry §line | 1766 |
| Registry status | PLANNED |
| Linked schemas | `runner-identity.schema.json`, `evidence-bundle.schema.json` |
| Linked agent decl. | `docs/agents/worker.md` §Capabilities |
| Enforcement point (planned) | `internal/scheduler/` |
| Test path (planned) | `tests/scheduler/` |

---

## 9. `platform-self-healing` (#117) — Self-Healing CI Standard

**Applicability.** Applicable. Classify failures; recovery behavior
depends on class. At minimum: code, test, flaky test, runner, infra,
network, dependency, capacity, policy, credential. The
`failure-classification.schema.json` already enumerates exactly these 10
classes plus `unknown`.

**System requirements it maps to.**
- Failure classifier at `services/classifier/` (PLANNED).
- Recovery policy per class (mapping table; e.g. `flaky_test` →
  retry once with no backoff; `network_failure` → exponential backoff
  with circuit-breaker; `policy_failure` → no retry, surface to
  operator).
- Retry policy declared in the action manifest
  (`action-manifest.schema.json` `retries.retry_on[]`).

**Current status — PLANNED.** `registry.json` row 1781:
`implementation: "Slice 3: failure classification in services/
classifier; recovery policy per class."`
`enforcement_point: "services/classifier/."`
`test: "tests/classifier/."`
`evidence: "PLANNED"`.
**Schema already exists** at `failure-classification.schema.json` and
is embedded by `standards.go`.

**Gap.** No classifier; today a subprocess failure is `exit_code != 0`
and nothing else — no classification; retries today are entirely
client-side.

**Highest-leverage next step.** The classifier is a deterministic
function over the worker's already-emitted records (exit code,
stderr/stdout signal, lease lifecycle, policy check result). Build it as
a pure function first so it is testable without I/O.

- **File to create:** `services/classifier/classifier.go` (pure
  function: `(attempt, lease, policyResult) → Class`; uses the
  embedded `failure-classification.schema.json` for enum
  validation).
- **File to create:** `internal/recovery/policy.go` (per-class
  recovery policy: retryable, max_retries, backoff, human_required).
- **File to create:** `tests/classifier/classifier_test.go` (golden
  vectors per class).

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-self-healing` |
| Registry §line | 1781 |
| Registry status | PLANNED |
| Existing schema | `docs/standards/schemas/failure-classification.schema.json` (already embedded) |
| Linked schemas | `action-manifest.schema.json` (`retries.retry_on[]`) |
| Linked agent decl. | `docs/agents/worker.md` §Escalation rules |
| Enforcement point (planned) | `services/classifier/` |
| Test path (planned) | `tests/classifier/` |

---

## 10. `platform-ai-failure-intel` (#118) — AI-Assisted Failure Intelligence Standard

**Applicability.** Applicable, but **deferred to slice 5+**. The agent
declaration (`docs/agents/worker.md §Model`) explicitly states
"**None.** The worker is a deterministic subprocess executor. It does
not call any ML model, LLM, or external AI service. This is by design
(ADR-0003)… and the design review… explicitly defers AI involvement to
a later slice."

**System requirements it maps to.**
- AI may classify, correlate, summarize, suggest, reproduce, remediate —
  within policy-bounded authority.
- Authority controlled by capability manifest (additive `ai.*`
  permissions in slice 5+).
- Output is auditable: AI suggestions are evidence records, not state
  changes.

**Current status — PLANNED.** `registry.json` row 1796:
`implementation: "Slice 5+: optional LLM-backed failure analysis.
Authority controlled by capability manifest."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** No LLM call path; no AI capability admission. Deferred by
design. Cross-references `mappings/ai.md` for LLM-Top10 etc.

**Highest-leverage next step.** Not applicable in slice 3 — the slice
work that *enables* this is shipping the capability manifest (§2),
zero-secret (§6), and self-healing (§9). Once those are in, AI
assistance becomes an additive capability that respects the existing
admission + evidence + secrets boundaries.

- **File to defer:** no slice-3 file. Re-evaluate at slice 5 design
  review.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-ai-failure-intel` |
| Registry §line | 1796 |
| Registry status | PLANNED, deferred to slice 5+ |
| Linked agent decl. | `docs/agents/worker.md` §Model |
| Slice-3 action | none — depends on §2, §6, §9 |

---

## 11. `platform-continuous-verification` (#119) — Continuous Verification Standard

**Applicability.** Applicable. Don't stop after deploy. Pipeline:
Build → Test → Scan → Verify → Attest → Sign → Deploy → Observe →
Runtime Verify.

**System requirements it maps to.**
- Pipeline shape encoded as policy + scripts (`ci/local-runner/
  continuous-verify.sh` — PLANNED).
- Each step emits an evidence record into the bundle (§5).
- "Runtime Verify" is the post-deploy hook; for V1 this is
  `GET /v1/works/{id}/evidence` consumed by a downstream monitor.

**Current status — PLANNED.** `registry.json` row 1811:
`implementation: "Slice 3+ in concert with evidence bundles and
attestations."`
`enforcement_point: "ci/local-runner/continuous-verify.sh."`
`test: null`.
`evidence: null`.

**Gap.** No `ci/local-runner/continuous-verify.sh`; the local runner is
not yet authored (the venture inherits an AVC `avc/ci-local` contract
at the platform level, but the *platform's own* continuous-verify
script does not exist).

**Highest-leverage next step.** Wire the existing evidence endpoint
(§5) into a minimal `continuous-verify.sh` that asserts the bundle is
signed, attempts are non-empty, and the final result matches the policy
declaration. This makes "did the run actually verify?" a script
answerable in one line.

- **File to create:** `ci/local-runner/continuous-verify.sh` (fetches
  `/v1/works/{id}/evidence`; checks signature, attempt count, final
  result vs policy).
- **File to create:** `ci/local-runner/continuous-verify_test.go` (the
  shell is invoked by a Go test; the test asserts the script's exit
  code on golden bundles).

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-continuous-verification` |
| Registry §line | 1811 |
| Registry status | PLANNED |
| Linked schemas | `evidence-bundle.schema.json` |
| Linked agent decl. | `docs/agents/worker.md` §Evidence requirements |
| Enforcement point (planned) | `ci/local-runner/continuous-verify.sh` |
| Test path (planned) | `ci/local-runner/continuous-verify_test.go` |

---

## 12. `platform-universal-compat` (#120) — Universal Action Compatibility Standard

**Applicability.** Applicable but lower-priority. Compat or migration
for GitHub Actions, GitLab CI, Jenkins, CircleCI, Buildkite, Tekton,
shell, Docker, Bazel, Nix.

**System requirements it maps to.**
- Import path that translates a foreign workflow file (`.github/
  workflows/*.yml`, `.gitlab-ci.yml`, `Jenkinsfile`, `.circleci/
  config.yml`, `buildkite.yml`, Tekton TaskRun, Dockerfile,
  `BUILD.bazel`, `flake.nix`) into a works-execution action graph.
- The translated graph carries a capability manifest so §3, §11, §17
  still apply.

**Current status — PLANNED.** `registry.json` row 1826:
`implementation: "Slice 3+: GitHub Actions runner compatibility first."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** No importer for any provider; no translation layer.

**Highest-leverage next step.** GitHub Actions compat first — it is the
largest install base and the schema for `runs-on`, `steps[].run`, and
`needs` maps cleanly onto workgraph.

- **File to create:** `internal/compat/github_actions/import.go` (parse
  `.github/workflows/*.yml`; emit Work + node graph).
- **File to create:** `internal/compat/github_actions/import_test.go`
  (golden workflows → golden graphs).
- **File to defer:** other providers (GitLab, Jenkins, etc.) — track as
  follow-on.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-universal-compat` |
| Registry §line | 1826 |
| Registry status | PLANNED |
| Linked schemas | `action-manifest.schema.json` |
| Linked agent decl. | `docs/agents/worker.md` §Capabilities |
| Slice-3 deliverable | GitHub Actions importer only |

---

## 13. `platform-runner-identity` (#121) — Runner Identity Standard

**Applicability.** Applicable. Every runner has: unique identity,
trust classification, capability declaration, platform info, lifecycle
state, attestation, audit history. The schema
`docs/standards/schemas/runner-identity.schema.json` already declares
all of these.

**System requirements it maps to.**
- Schema already exists (`runner-identity.schema.json`); embeds via
  `internal/standards/standards.go`.
- Worker enrollment produces a Runner Identity record (`services/runner/
  registry.go` — PLANNED).
- Trust classification (`untrusted`/`standard`/`privileged`) is the
  keystone for §17 (policy-enforced) and §3 (hermetic trust-gated
  egress).

**Current status — PLANNED.** `registry.json` row 1841:
`implementation: "Slice 3: services/runner/registry.go with SPIFFE IDs
and capability advertisement."`
`enforcement_point: "services/runner/."`
`test: "tests/runner/."`
`evidence: "PLANNED"`.
**Schema already exists** and is embedded. The agent declaration
(`docs/agents/worker.md §Identity`) already declares the planned
SPIFFE ID format `spiffe://works-execution/ns/default/sa/<worker-id>`.

**Gap.** No runner registry; worker identity is implicit (slice 2
worker has no SPIFFE ID, just a `worker_id` string passed in the lease
body); no trust-class gating.

**Highest-leverage next step.** Mint the runner identity at enrollment
and persist it; this is the single primitive that unblocks §1 (portable
matching), §3 (trust-gated egress), §8 (scheduling signals), and §17
(policy-enforced). One record, four downstream standards.

- **File to create:** `services/runner/registry.go` (enrollment handler;
  mints SPIFFE-like ID; persists `runner-identity.schema.json`
  document).
- **File to modify:** `internal/worker/worker.go` (calls enrollment on
  startup; presents SPIFFE ID on every API call).
- **File to create:** `tests/runner/registry_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-runner-identity` |
| Registry §line | 1841 |
| Registry status | PLANNED |
| Existing schema | `docs/standards/schemas/runner-identity.schema.json` (already embedded) |
| Linked agent decl. | `docs/agents/worker.md` §Identity (planned SPIFFE ID) |
| Enforcement point (planned) | `services/runner/` |
| Test path (planned) | `tests/runner/` |

---

## 14. `platform-workflow-provenance` (#122) — Workflow Provenance Standard

**Applicability.** Applicable. Every workflow execution traceable to
repo, source revision, workflow revision, caller, policy version,
runner, Actions, artifacts. The schema
`docs/standards/schemas/workflow-provenance.schema.json` already
encodes the SLSA / in-toto shape: `predicateType`, `subject`,
`predicate.{builder, invocation, materials, metadata}`.

**System requirements it maps to.**
- Schema already exists and is embedded.
- Producer at `services/provenance/` (PLANNED) emits one provenance
  attestation per work, included in the evidence bundle (§5).
- Validation that `predicate.metadata.reproducible` aligns with the
  fingerprint (§4).

**Current status — PLANNED.** `registry.json` row 1856:
`implementation: "Slice 3: services/provenance/ produces SLSA-style
provenance attestation per work."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.
**Schema already exists**.

**Gap.** No `services/provenance/` package; no SLSA attestation emitted;
`workflow-provenance.schema.json` is unused.

**Highest-leverage next step.** Build the provenance emitter as a
pure transform: `(work, attempts, evidence, fingerprints) → provenance
attestation`. Because all inputs already exist in the slice 2 store,
the emitter is a thin adapter.

- **File to create:** `services/provenance/provenance.go` (pure
  transform; emits per `workflow-provenance.schema.json`).
- **File to create:** `tests/provenance/provenance_test.go` (golden
  vectors).

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-workflow-provenance` |
| Registry §line | 1856 |
| Registry status | PLANNED |
| Existing schema | `docs/standards/schemas/workflow-provenance.schema.json` (already embedded) |
| Linked schemas | `evidence-bundle.schema.json` (provenance lives in `components.evidence`) |
| Enforcement point (planned) | `services/provenance/` |

---

## 15. `platform-action-attestation` (#123) — Action Attestation Standard

**Applicability.** Applicable. Per-action attestations: identity,
source, build, runtime, results. `registry.json` row 1871 explicitly
binds this to §14: `implementation: "Bundled with platform-workflow-
provenance."`

**System requirements it maps to.**
- One attestation per node in the work graph (vs §14's per-work).
- Identity = action_id + version + sha256 of the manifest.
- Source = source revision digest.
- Build = build evidence record (passed/failed).
- Runtime = runner identity + hermetic posture.
- Results = attempt record.

**Current status — PLANNED (bundled).** `registry.json` row 1871:
`implementation: "Bundled with platform-workflow-provenance."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** None independent of §14. The action attestation is a slice of
the per-work provenance with one extra `per_node_attestations[]`
array; reusing the same producer.

**Highest-leverage next step.** No independent slice-3 deliverable —
delivered jointly with §14.

- **File (joint):** `services/provenance/provenance.go` includes
  `per_node_attestations[]` derived from attempts.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-action-attestation` |
| Registry §line | 1871 |
| Registry status | PLANNED (bundled with #122) |
| Linked schema | `workflow-provenance.schema.json` (extended per-node) |
| Slice-3 deliverable | bundled with §14 |

---

## 16. `platform-execution-evidence` (#124) — Execution Evidence Standard

**Applicability.** Applicable. Per-execution evidence: input/output
hashes, logs, results, attester identity, timestamp. `registry.json`
row 1886 explicitly binds this to §5: `implementation: "Bundled with
platform-evidence-first."`

**System requirements it maps to.**
- All fields already present in `evidence-bundle.schema.json`:
  `artifacts[].digest`, `attempts[].command` (redacted), `summary.*`,
  `signatures[].signed_at`.
- Attester identity = `signer` field on every evidence record.

**Current status — PLANNED (bundled).** `registry.json` row 1886:
`implementation: "Bundled with platform-evidence-first."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** None independent of §5.

**Highest-leverage next step.** No independent slice-3 deliverable —
delivered jointly with §5.

- **File (joint):** `services/evidence/bundle.go` covers this standard
  by construction.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-execution-evidence` |
| Registry §line | 1886 |
| Registry status | PLANNED (bundled with #113) |
| Linked schema | `evidence-bundle.schema.json` |
| Slice-3 deliverable | bundled with §5 |

---

## 17. `platform-policy-enforced-action` (#125) — Policy-Enforced Action Standard

**Applicability.** Applicable. Policy evaluation before every action
execution. The agent declaration
(`docs/agents/worker.md §Standards mapping`) already asserts that "every
action runs after policy check (the lease grant IS the policy check)".
That is a slice 1+2 simplification; slice 3 formalizes it.

**System requirements it maps to.**
- Admission hook in `services/api/admission.go` (same file as §2 — the
  two are inseparable in practice).
- Policy bundles are content-addressed; policy version recorded on the
  evidence bundle (`evidence-bundle.schema.json` `policy_version`).
- Policy decisions are evidence records, not silent allow/deny.

**Current status — PLANNED.** `registry.json` row 1901:
`implementation: "Slice 3+: admission hook in services/api/admission.go."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** No `services/api/admission.go`; no policy bundles; lease grant
is currently "first ready wins" with no policy check beyond RBAC.

**Highest-leverage next step.** Build admission as a *single* Go
function that every action-touching endpoint calls. Reusing it across
the action-create endpoint (§2), the lease-grant endpoint, and the
secret-request endpoint makes the policy surface uniform.

- **File to create:** `services/api/admission.go` (single
  `Admit(ctx, principal, action) → Decision`; called by action-create,
  lease-grant, evidence-write).
- **File to create:** `tests/policy/admission_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-policy-enforced-action` |
| Registry §line | 1901 |
| Registry status | PLANNED |
| Linked schemas | `evidence-bundle.schema.json` (`policy_version`), `action-manifest.schema.json` (`permissions`) |
| Linked agent decl. | `docs/agents/worker.md` §Standards mapping |
| Enforcement point (planned) | `services/api/admission.go` |
| Test path (planned) | `tests/policy/admission_test.go` |

---

## 18. `platform-runtime-isolation` (#126) — Runtime Isolation Standard

**Applicability.** Applicable. Worker runs untrusted code in isolated
runtime (container, VM, sandbox).

**System requirements it maps to.**
- Docker worker sandbox (per registry row 1922).
- Filesystem isolation is also covered by the action manifest
  `filesystem.mode = isolated` (default in
  `action-manifest.schema.json`).
- Trust-class gating: `privileged` runners are the only ones that
  execute `privileged`-permitted actions (cross-ref §13).

**Current status — PLANNED.** `registry.json` row 1916:
`implementation: "Slice 3: Docker worker sandbox (per #109)."` (note:
the registry says #109 here but that is a typo — it should reference
§18 / §3; not a substantive issue.)
`enforcement_point: "internal/sandbox/."`
`test: "tests/sandbox/."`
`evidence: "PLANNED"`.

**Gap.** Worker runs on the host today; no Docker sandbox; no VM
sandbox; no OCI image build pipeline for the worker.

**Highest-leverage next step.** Land the subprocess-level isolation
(§3 hermetic) first as the *minimum* runtime isolation — a container
sandbox without a hermetic subprocess is a leaky abstraction. Then
add the Docker wrapper in the same slice for defense in depth.

- **File to create:** `internal/sandbox/hermetic.go` (jointly with §3).
- **File to create:** `internal/sandbox/docker.go` (Docker executor
  using the OCI image declared in `action-manifest.schema.json`
  `runtime.image`).
- **File to create:** `tests/sandbox/docker_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-runtime-isolation` |
| Registry §line | 1916 |
| Registry status | PLANNED |
| Linked schemas | `action-manifest.schema.json` (`runtime.image`, `filesystem.mode`) |
| Linked agent decl. | `docs/agents/worker.md` §Identity (planned Docker image) |
| Enforcement point (planned) | `internal/sandbox/` |
| Test path (planned) | `tests/sandbox/` |

---

## 19. `platform-portable-cache` (#127) — Portable Cache Standard

**Applicability.** Applicable. Cache scheme that works across providers.

**System requirements it maps to.**
- Cache key = content-addressed fingerprint (§4 + §7), not provider-
  specific path.
- L1 (worker-local) / L2 (organization) / L3 (global) scopes per
  `action-manifest.schema.json` `cache.scope`.
- Cache hit/miss recorded as evidence (`evidence-bundle.schema.json`
  `evidence[].type=artifact`).

**Current status — PLANNED.** `registry.json` row 1931:
`implementation: "Slice 3: content-addressed cache (#116), portable
across L1/L2/L3."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** No portable cache; L1 only today (artifact files in worker
`ArtifactsDir`).

**Highest-leverage next step.** Land the L1 cache as a content-
addressed directory keyed by the fingerprint from §4. L2/L3 are
storage choices on top of the same key.

- **File to create:** `internal/cache/l1.go` (content-addressed L1;
  key = fingerprint from `internal/fingerprint`).
- **File to create:** `tests/cache/l1_test.go` (golden vectors; hit
  reduces execution time).

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-portable-cache` |
| Registry §line | 1931 |
| Registry status | PLANNED |
| Linked schemas | `action-manifest.schema.json` (`cache.{key_inputs, scope}`) |
| Enforcement point (planned) | `internal/cache/` |
| Test path (planned) | `tests/cache/` |

---

## 20. `platform-cross-provider` (#128) — Cross-Provider Workflow Standard

**Applicability.** Applicable. Workflows portable across providers.
`registry.json` row 1946 explicitly bundles with §12:
`implementation: "Bundled with platform-universal-compat."`

**System requirements it maps to.**
- Workflows expressed in terms of the action manifest (§2) and workgraph
  (`packages/workgraph/workgraph.go`) — both provider-neutral.
- Translation happens at the edge (§12); the *runtime* representation
  is provider-neutral.

**Current status — PLANNED (bundled).** `registry.json` row 1946:
`implementation: "Bundled with platform-universal-compat."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** None independent of §12.

**Highest-leverage next step.** No independent slice-3 deliverable —
delivered jointly with §12.

- **File (joint):** the §12 GitHub Actions importer demonstrates cross-
  provider portability by construction.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-cross-provider` |
| Registry §line | 1946 |
| Registry status | PLANNED (bundled with #120) |
| Slice-3 deliverable | bundled with §12 |

---

## 21. `platform-autonomous-remediation` (#129) — Autonomous Remediation Standard

**Applicability.** Applicable. Automated remediation only when:
failure classified, authority permits, blast radius understood,
evidence preserved, rollback exists, retry limits enforced.

**System requirements it maps to.**
- All six preconditions map to existing slice 3 deliverables: failure
  classification (§9), authority = capability manifest (§2), blast
  radius = capability `permissions` + hermetic posture (§3), evidence
  = evidence bundle (§5), rollback = manifest `rollback.strategy`,
  retry limits = `failure-classification.schema.json`
  `max_retries`.
- Remediation actions declared in capability manifest
  (`action-manifest.schema.json` `autonomous_remediation[]` via the
  embedded `failure-classification.schema.json`).

**Current status — PLANNED.** `registry.json` row 1961:
`implementation: "Slice 3: bounded retry policy per failure class;
remediation actions declared in capability manifest."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** No remediation executor; no blast-radius analysis.

**Highest-leverage next step.** Land the *bounded retry policy* first
(max 5 attempts, exponential backoff capped, human_required flag) —
without a bound, autonomous remediation becomes a foot-gun. The
remediation executor is layered on top in a later slice.

- **File to create:** `internal/recovery/policy.go` (jointly with §9;
  enforces max_retries from `failure-classification.schema.json`).
- **File to create:** `tests/recovery/policy_test.go`.

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-autonomous-remediation` |
| Registry §line | 1961 |
| Registry status | PLANNED |
| Linked schemas | `failure-classification.schema.json`, `action-manifest.schema.json` (`autonomous_remediation[]`) |
| Enforcement point (planned) | `internal/recovery/` |
| Test path (planned) | `tests/recovery/` |

---

## 22. `platform-continuous-innovation` (#130) — Continuous Innovation Feedback Standard

**Applicability.** Applicable. Innovation lifecycle: Signal →
Opportunity → Hypothesis → Experiment → Evidence → Decision →
Implementation → Measurement → Learning.

**System requirements it maps to.**
- Lifecycle document at `docs/standards/innovation/lifecycle.md`
  (PLANNED).
- Each `Evidence` step references a slice-3 evidence bundle (§5) so
  the platform's own innovation loop is content-addressed and
  auditable.
- `Decision` step is an ADR (`docs/adr/`) — already practiced in the
  venture (ADR-0002, ADR-0003 referenced in
  `docs/agents/worker.md`).

**Current status — PLANNED.** `registry.json` row 1976:
`implementation: "Slice 3: docs/standards/innovation/lifecycle.md."`
`enforcement_point: null`.
`test: null`.
`evidence: null`.

**Gap.** No `docs/standards/innovation/lifecycle.md`; no
innovation-flow record in the evidence bundles.

**Highest-leverage next step.** Author the lifecycle document plus a
template that forces each experiment to reference a slice-3 evidence
bundle. This makes "we tried X" auditable on the same substrate as
"the platform ran Y".

- **File to create:** `docs/standards/innovation/lifecycle.md` (the
  9-step lifecycle with the Evidence step explicitly referencing
  `evidence-bundle.schema.json`).
- **File to create:** `docs/standards/innovation/EXPERIMENT_TEMPLATE.md`
  (templated experiment record; references an `evidence_bundle_id`).
- **File to modify:** `evidence-bundle.schema.json` (optional additive
  `experiment_id` field; backward compatible).

**Traceability.**

| Field | Value |
|---|---|
| `standard_id` | `platform-continuous-innovation` |
| Registry §line | 1976 |
| Registry status | PLANNED |
| Linked schemas | `evidence-bundle.schema.json` (additive `experiment_id`) |
| Enforcement point (planned) | `docs/standards/innovation/lifecycle.md` |
| Test path (planned) | `docs/standards/innovation/EXPERIMENT_TEMPLATE.md` |

---

## Cross-cutting summary — priority-ordered concrete next steps

In order of leverage (each step delivers value for multiple standards):

| # | File to create/modify | Standards it advances |
|---|-----------------------|-----------------------|
| 1 | `internal/manifest/manifest.go` + `services/api/admission.go` + `tests/manifest/` | §2, §17, §1, §18, §6 |
| 2 | `services/evidence/bundle.go` + `services/api/api.go` (`GET /v1/works/{id}/evidence`) | §5, §16, §11, §14 |
| 3 | `services/runner/registry.go` + `internal/worker/worker.go` enrollment | §13, §1, §3, §8, §17 |
| 4 | `internal/sandbox/hermetic.go` + `internal/worker/worker.go` subprocess routing | §3, §18, §17 |
| 5 | `internal/fingerprint/fingerprint.go` + `services/work/store/store.go` extension | §7, §4, §19, §14 |
| 6 | `services/provenance/provenance.go` + `services/api/api.go` route | §14, §15, §4 |
| 7 | `services/classifier/classifier.go` + `internal/recovery/policy.go` | §9, §21, §11 |
| 8 | `services/api/auth.go` (zero-secret enrollment) + `internal/worker/worker.go` | §6, §13, §17 |

---

## The 8 most leverage-heavy standards to move to IMPLEMENTED in slice 3

These eight are the *one-slice* deliverable. Each carries the existing
schema (where applicable), the file paths, and the acceptance criteria
that gate the status flip from PLANNED → IMPLEMENTED. The flip happens
only when the acceptance criteria pass and the registry row is updated
in the same commit.

| # | standard_id | Existing schema | Slice-3 deliverable files | Status flip trigger |
|---|---|---|---|---|
| 1 | `platform-action-manifest` (#110) | `action-manifest.schema.json` (embedded) | `internal/manifest/manifest.go`; `services/api/admission.go`; `tests/manifest/{manifest,admission}_test.go` | §A1 |
| 2 | `platform-evidence-first` (#113) | `evidence-bundle.schema.json` (embedded) | `services/evidence/bundle.go`; `services/api/api.go` (add `GET /v1/works/{id}/evidence`); `tests/evidence/bundle_test.go` | §A2 |
| 3 | `platform-hermetic-execution` (#111) | (schema-level default in action-manifest) | `internal/sandbox/hermetic.go`; `internal/worker/worker.go`; `tests/sandbox/hermetic_test.go` | §A3 |
| 4 | `platform-runner-identity` (#121) | `runner-identity.schema.json` (embedded) | `services/runner/registry.go`; `internal/worker/worker.go` (enrollment); `tests/runner/registry_test.go` | §A4 |
| 5 | `platform-content-addressed` (#116) | (combined with #4 + #7 + #14) | `internal/fingerprint/fingerprint.go`; `services/work/store/store.go` (extend) | §A5 |
| 6 | `platform-workflow-provenance` (#122) | `workflow-provenance.schema.json` (embedded) | `services/provenance/provenance.go`; `tests/provenance/provenance_test.go` | §A6 |
| 7 | `platform-policy-enforced-action` (#125) | (combined with #110 admission) | `services/api/admission.go` (jointly with #1); `tests/policy/admission_test.go` | §A7 |
| 8 | `platform-portable-action` (#109) | (combined with #121 capabilities) | `internal/scheduler/capability.go`; `services/api/leases.go` (filter); `tests/scheduler/capability_test.go` | §A8 |

**Why these eight (and not others).**

- **Three of them (#1, #2, #6) have their JSON Schemas already authored
  and embedded** by `internal/standards/standards.go`. That means the
  hardest design work — getting the contract right — is already done.
  The slice-3 work is *wiring*, not authoring.
- **Two of them (#3, #7) ride on the same `services/api/admission.go`
  file** that #1 ships, so #7 costs effectively zero additional surface
  area once #1 is in.
- **#5 and #6 together** deliver content-addressing + provenance +
  reproducibility, which is the substrate for self-healing (#9),
  evidence (#5/#16), and continuous-verification (#11) — but #5 and
  #6 are *both* required for any of those to mean anything, so they
  must ship together.
- **#8 is the only one that exposes existing portability** — the slice
  2 binary already runs anywhere; #8's slice-3 work is the
  scheduler/registry telling the platform that fact.

The remaining 14 are deferred: #4 (#112), #6 (#114), #8 (#115), #9
(#117), #11 (#119), #12 (#120), #18 (#126), #19 (#127), #21 (#129),
#22 (#130) are slice 3+ but not in the *first* slice 3 cut; #10
(#118) is slice 5+ by design (per `docs/agents/worker.md §Model`);
#15, #16, #20 are bundled with their primary standard.

### Slice 3 acceptance criteria (gate the status flip)

Each flip from PLANNED → IMPLEMENTED requires **all** of the following
for the corresponding standard:

#### §A1. `platform-action-manifest` (#110)

1. `internal/manifest/manifest.go` defines the Go struct that mirrors
   every `required` field in
   `docs/standards/schemas/action-manifest.schema.json` and exposes
   `Validate(b []byte) error` calling
   `standards.ValidateBytes("action-manifest.schema.json", b)`.
2. `services/api/admission.go` rejects any action-create request whose
   body fails validation, with HTTP 422 and a JSON error body citing
   the failing schema path.
3. `tests/manifest/manifest_test.go` covers: positive case (golden
   manifest validates), negative case (missing `action_id` rejected),
   canonicalization is stable (sha256 matches across runs).
4. `tests/manifest/admission_test.go` covers: API rejects malformed
   manifests; happy path persists.
5. `internal/standards/standards.go` unchanged (already embeds the
   schema).
6. `docs/agents/worker.md` §Standards mapping cross-reference updated
   to point at `services/api/admission.go`.
7. Registry row 1676 `status` flips to `"IMPLEMENTED"` in the same
   commit; `evidence` becomes
   `"internal/manifest/manifest.go (slice 3); docs/standards/schemas/
   action-manifest.schema.json"`.

#### §A2. `platform-evidence-first` (#113)

1. `services/evidence/bundle.go` reads (work, attempts, artifacts,
   evidence, leases) from the slice 2 store and emits a JSON document
   that satisfies
   `docs/standards/schemas/evidence-bundle.schema.json` (validated by
   `standards.ValidateBytes`).
2. `bundle_id` = `evb_<first-32-hex-of-sha256>` of the canonicalized
   JSON.
3. `services/api/api.go` exposes `GET /v1/works/{id}/evidence` that
   returns the bundle with content-type `application/json` and
   `Cache-Control: no-store`.
4. `signatures[]` carries at least one ed25519 signature using a key
   loaded from `WORKS_API_SIGNING_KEY` env var (with a documented
   dev-mode fallback for tests).
5. `tests/evidence/bundle_test.go` covers: end-to-end (create work →
   run node → fetch evidence → validate against schema → verify
   signature).
6. `docs/agents/worker.md` §Evidence requirements updated to point at
   the new endpoint.
7. Registry row 1721 `status` flips to `"IMPLEMENTED"`.

#### §A3. `platform-hermetic-execution` (#111)

1. `internal/sandbox/hermetic.go` provides
   `HermeticExec(ctx, cmd, networkPolicy, allowList) (exitCode int,
   err error)` that sets `CLONE_NEWNET`, `NO_NEW_PRIVS`, drops
   ambient capabilities, and (when `networkPolicy=deny`) redirects
   `/etc/resolv.conf` to `/dev/null`.
2. `internal/worker/worker.go` routes every `exec.CommandContext`
   through `sandbox.HermeticExec`; honors `action.manifest.network
   .policy` and `allow_list`.
3. `tests/sandbox/hermetic_test.go` covers: a subprocess cannot
   resolve DNS (network=deny); a subprocess can resolve a hostname in
   `allow_list` (network=allow-list).
4. Registry row 1691 `status` flips to `"IMPLEMENTED"`.

#### §A4. `platform-runner-identity` (#121)

1. `services/runner/registry.go` exposes `POST /v1/runners/enroll`
   that mints `spiffe://works-execution/ns/default/sa/<worker-id>`
   (the format already declared in
   `docs/agents/worker.md §Identity`) and persists a
   `runner-identity.schema.json` document.
2. `internal/worker/worker.go` calls enrollment on startup, stores the
   issued identity, and presents it on every API call via
   `Authorization: Bearer <enrollment-token>` (overlaps §A6 below).
3. The persisted identity record validates against
   `docs/standards/schemas/runner-identity.schema.json` via
   `standards.ValidateBytes`.
4. `tests/runner/registry_test.go` covers: enrollment round-trip;
   rejected duplicate `runner_id`; rejected malformed capabilities.
5. Registry row 1841 `status` flips to `"IMPLEMENTED"`.

#### §A5. `platform-content-addressed` (#116)

1. `internal/fingerprint/fingerprint.go` exposes
   `Fingerprint(work, action, inputs, env, deps) string` returning
   `sha256:` of the canonicalized JSON.
2. The canonicalization (RFC 8785 / JCS) is deterministic across runs
   and tested via golden vectors.
3. `services/work/store/store.go` stores source revisions, action
   manifests, and environment captures keyed by their digest (in
   addition to the slice 1 artifact digest).
4. `tests/fingerprint/fingerprint_test.go` covers: same inputs →
   same digest; canonicalization is stable across runs.
5. Registry row 1751 `status` flips from `"PARTIAL"` to
   `"IMPLEMENTED"` (this row is already PARTIAL; the flip is the
   *completion* of PARTIAL → IMPLEMENTED).

#### §A6. `platform-workflow-provenance` (#122)

1. `services/provenance/provenance.go` emits one
   `workflow-provenance.schema.json` document per work, included in
   the evidence bundle (`components.evidence[].type=policy` or new
   `type=provenance`).
2. `materials[]` includes every source digest (from §A5).
3. `predicate.metadata.reproducible` is set from the fingerprint
   equality check.
4. `tests/provenance/provenance_test.go` covers: golden work →
   golden provenance document.
5. Registry row 1856 `status` flips to `"IMPLEMENTED"`.

#### §A7. `platform-policy-enforced-action` (#125)

1. `services/api/admission.go` exposes a single
   `Admit(ctx, principal, action) → Decision` function called by:
   - the action-create endpoint (§A1)
   - the lease-grant endpoint
   - the secret-request endpoint (§A6 / zero-secret)
2. `Decision{Allow: false, Reason: ...}` is returned (with the failing
   policy id) when:
   - the action manifest is missing required permissions (§A1)
   - the runner trust class is below the action's required trust
     class (§A4)
   - the requested secret exceeds its `ttl_seconds` (§A6)
3. Policy decisions are persisted as evidence records
   (`evidence-bundle.schema.json` `components.evidence[].type=policy`).
4. `tests/policy/admission_test.go` covers: each deny path; the
   allow path; the audit-trail path.
5. Registry row 1901 `status` flips to `"IMPLEMENTED"`.

#### §A8. `platform-portable-action` (#109)

1. `internal/scheduler/capability.go` exposes
   `Match(action, runners []RunnerIdentity) []ScoredRunner` that
   filters runners by `runtime.os`/`runtime.arch` fit and
   `runtime.toolchain` availability.
2. `services/api/leases.go` `/v1/workers/ready` returns only runners
   that match the action's capability requirements.
3. `tests/scheduler/capability_test.go` covers: linux/amd64 action
   matches linux/amd64 runner only; linux/arm64 action never matches
   linux/amd64 runner; toolchain mismatch excludes runner.
4. Registry row 1661 `status` flips to `"IMPLEMENTED"`.

#### Cross-cutting acceptance gates

In addition to the per-standard criteria, all 8 flips require:

- `make vet` clean.
- `make test` clean (with new tests).
- `make e2e` clean (the e2e test in `e2e/e2e_test.go` extended to
  exercise at least one path through each new module).
- The registry `summary.by_status` recount reflects the flips
  (regenerated by the docs tool — manual recount not required).
- The agent declaration `docs/agents/worker.md §Standards mapping`
  points at every new file path.
- No GitHub Actions workflow added — `docs/operations/
  GITHUB_ACTIONS_BOUNDARY.md` is the boundary, not a CI gate.

---

## Traceability table (registry `standard_id` → this document)

Every platform row in `docs/standards/registry.json` maps to a section
above. `registry.json` lines are 1-indexed from the file.

| standard_id (registry) | registry §line | This doc § | registry status | slice 3 priority |
|------------------------|---------------:|-----------:|-----------------|------------------|
| `platform-portable-action` | 1661 | §1 | PLANNED | **P1** |
| `platform-action-manifest` | 1676 | §2 | PLANNED | **P1** |
| `platform-hermetic-execution` | 1691 | §3 | PLANNED | **P1** |
| `platform-reproducible-execution` | 1706 | §4 | PLANNED | P2 |
| `platform-evidence-first` | 1721 | §5 | PLANNED | **P1** |
| `platform-zero-secret` | 1736 | §6 | PLANNED | P2 |
| `platform-content-addressed` | 1751 | §7 | PARTIAL | **P1** |
| `platform-intelligent-scheduling` | 1766 | §8 | PLANNED | P2 |
| `platform-self-healing` | 1781 | §9 | PLANNED | P2 |
| `platform-ai-failure-intel` | 1796 | §10 | PLANNED (deferred slice 5+) | — |
| `platform-continuous-verification` | 1811 | §11 | PLANNED | P3 |
| `platform-universal-compat` | 1826 | §12 | PLANNED | P3 |
| `platform-runner-identity` | 1841 | §13 | PLANNED | **P1** |
| `platform-workflow-provenance` | 1856 | §14 | PLANNED | **P1** |
| `platform-action-attestation` | 1871 | §15 | PLANNED (bundled #122) | (bundled) |
| `platform-execution-evidence` | 1886 | §16 | PLANNED (bundled #113) | (bundled) |
| `platform-policy-enforced-action` | 1901 | §17 | PLANNED | **P1** |
| `platform-runtime-isolation` | 1916 | §18 | PLANNED | P2 |
| `platform-portable-cache` | 1931 | §19 | PLANNED | P3 |
| `platform-cross-provider` | 1946 | §20 | PLANNED (bundled #120) | (bundled) |
| `platform-autonomous-remediation` | 1961 | §21 | PLANNED | P3 |
| `platform-continuous-innovation` | 1976 | §22 | PLANNED | P3 |

---

## Open items and forward references

- **Slice 5+ AI work (§10).** Out of slice 3 scope by design. The
  capability manifest, zero-secret, and self-healing slice-3 work
  provides the policy substrate that AI assistance will respect when
  it lands.
- **Bundle schema extension (§22).** The proposed additive
  `experiment_id` field on `evidence-bundle.schema.json` is
  backward-compatible (`additionalProperties: true` is already set)
  but should still be coordinated with the §A2 acceptance criteria
  — do not land the additive field in the same commit as the bundle
  itself unless explicitly reviewed.
- **Registry typo at row 1922.** "Slice 3: Docker worker sandbox (per
  #109)." — should reference §18, not §1. Not a substantive issue;
  flagged here for the next registry refresh.
- **Cross-references to other mappings.**
  - §3 hermetic ↔ `mappings/security.md` §9 zero-trust.
  - §5 evidence ↔ `mappings/observability.md` audit events.
  - §6 zero-secret ↔ `mappings/security.md` §9 zero-trust.
  - §13 runner-identity ↔ `mappings/identity.md` workload identity.
  - §17 policy-enforced ↔ `mappings/policy.md`.

> **Slice-3 closure criterion.** The slice 3 venture-level completion
> gates when all 8 P1 standards (§1, §2, §3, §5, §7, §13, §14, §17)
> have their `status` flipped from PLANNED/PARTIAL to IMPLEMENTED in
> the registry, with the acceptance criteria above satisfied and the
> new test files passing in `make test` and `make e2e`.