# Platform, Containers & Build/Reproducibility — Per-Standard Mapping

**Document ID:** `works-standards-platform-build-mapping`
**Venture:** works-execution (`github.com/JonasAbde/works-execution`)
**Generated:** 2026-08-31
**Slice context:** Slice 1 (`d3db1d1`) shipped the `Work` primitive, SQLite
store, HTTP API, CLI, and polling subprocess worker. Slice 2 (`dab84f2`)
added lease-based scheduling, worker-loss recovery, and log streaming.
This document maps the **14 platform/containers + build/reproducibility
rows enumerated by the parent task** (8 platform/containers + 6 build/
reproducibility, per the explicit list in the task brief).

**Companion documents:**
- `docs/standards/registry.json` — authoritative machine-readable registry (130 rows)
- `docs/standards/mappings/identity.md`, `policy.md`, `supply-chain.md`, `ssd.md`,
  `quality.md`, `security.md`, `observability.md` — sibling mappings.
- `docs/standards/mappings/platform-build.md` — this document.
- The CI domain rows are owned by `docs/standards/registry.json` itself
  (`oci-runtime-spec`, `oci-image-spec`, `oci-distribution-spec`,
  `bazel-remote-execution-api`, `json-schema-2020-12`). Three of the
  rows in this mapping (`oci-runtime`, `oci-image`, `oci-distribution`)
  are explicit registry duplicates of those CI-domain rows and are
  tracked here **only for traceability — they are not double-counted**
  in the cross-mapping rollup at `docs/standards/registry.json`.

---

## §14 Implementation Rule (binding)

Every standard in this document is processed through the five-step rule
from the user-mandated standards charter:

1. **Determine applicability** — is this standard in-scope for works-execution V1?
2. **Map to system requirements** — which concrete component, contract, or test enforces it?
3. **Identify gaps** — what is missing today (Slice 1 + Slice 2)?
4. **Prioritize by risk and leverage** — score each gap on (risk-of-omission × leverage-on-platform-correctness).
5. **Recommend highest-value actionable gap with file path** — the next concrete change, where it lands, and the acceptance evidence.

---

## §1. Scope and deduplication (14 enumerated rows = 11 distinct active + 3 duplicates of CI-domain rows)

The registry contains **36 rows** in the `platform` domain. This document
covers the **14 platform/containers + build/reproducibility rows** that
the parent task enumerates explicitly (8 platform/containers — 3 OCI
duplicates + `wasm-component-model`, `wasi`, `k8s-api-conventions`,
`cloudevents`, `cncf-serverless-workflow`; 6 build/reproducibility —
`reproducible-builds`, `nix-reproducibility`, `bazel-hermetic`,
`cas-patterns`, `remote-execution-api`, `remote-cache`). The parent
task header says "16 total"; the explicit list above sums to 14. This
document treats the explicit enumeration as authoritative and maps
exactly the 14 standards named; the off-by-two gap between "16" and
"14" should be reconciled with the parent task at the registry
level (likely a counting error in the task header — none of the 36
platform-domain registry rows fits both the "platform/containers" and
"build/reproducibility" categorization beyond the 14 enumerated here
except `platform-content-addressed`, `platform-hermetic-execution`,
`platform-portable-action`, `platform-portable-cache`, and
`platform-reproducible-execution`, which are tracked under separate
"platform-internal" rows that fall outside this mapping's brief).
Three of the 14 rows are registry-flagged duplicates of rows already
mapped under the CI domain and are **not double-counted** in the
active set.

| #  | Standard                                  | Registry row             | Status (today) | Active? |
|----|-------------------------------------------|--------------------------|----------------|---------|
| 1  | OCI Runtime Spec (duplicate)              | `oci-runtime`            | NOT_APPLICABLE | **duplicate** of `oci-runtime-spec` (CI) |
| 2  | OCI Image Spec (duplicate)                | `oci-image`              | NOT_APPLICABLE | **duplicate** of `oci-image-spec` (CI) |
| 3  | OCI Distribution Spec (duplicate)         | `oci-distribution`       | NOT_APPLICABLE | **duplicate** of `oci-distribution-spec` (CI) |
| 4  | WebAssembly Component Model               | `wasm-component-model`   | PLANNED        | active |
| 5  | WASI                                      | `wasi`                   | PLANNED        | active |
| 6  | Kubernetes API Conventions                | `k8s-api-conventions`    | PLANNED        | active |
| 7  | CloudEvents                               | `cloudevents`            | PLANNED        | active |
| 8  | CNCF Serverless Workflow Specification    | `cncf-serverless-workflow` | PLANNED      | active |
| 9  | Reproducible Builds principles            | `reproducible-builds`    | PLANNED        | active |
| 10 | Nix reproducibility model                 | `nix-reproducibility`    | PLANNED        | active |
| 11 | Bazel hermetic build principles           | `bazel-hermetic`         | PLANNED        | active |
| 12 | Content-Addressable Storage patterns      | `cas-patterns`           | **IMPLEMENTED**| active |
| 13 | Remote Execution API                      | `remote-execution-api`   | PLANNED        | active |
| 14 | Remote Cache protocols                    | `remote-cache`           | PLANNED        | active |

**Net active count after deduplication: 11.** The three OCI duplicates
carry `status: NOT_APPLICABLE` and `exceptions: ["Duplicate entry…"]`
in the registry; their enforcement_point, test, and evidence fields are
all `null` with `implementation: "See oci-runtime-spec."` (or its
sibling). They are recorded in the traceability table (§6) for
completeness and explicitly flagged so the cross-mapping rollup does
not inflate the "active standards" denominator.

**Excluded from per-standard treatment in this document:**
- `oci-runtime`, `oci-image`, `oci-distribution` — tracked only as
  cross-references to the CI-domain rows `oci-runtime-spec`,
  `oci-image-spec`, `oci-distribution-spec` in §6 and in §3.1 below.
  All implementation, enforcement-point, test, and evidence work for
  those three standards lives on the CI side of the registry.

> **Naming convention.** "Wasm CM" = WebAssembly Component Model;
> "WASI" = WebAssembly System Interface; "K8s API conv." = Kubernetes
> API Conventions; "RE-API" = Remote Execution API (Bazel Build
> Remote Execution API v2); "CAS" = Content-Addressable Storage;
> "REPRO" = Reproducible Builds.

---

## §2. Summary table

| # | Standard | Status | Risk/Leverage | Top next step |
|---|----------|--------|---------------|---------------|
| 4 | WebAssembly Component Model | PLANNED | Medium / experimental | `docs/standards/mappings/wasm-adoption-rationale.md` (new) |
| 5 | WASI | PLANNED | Bundled with #4 | Bundled with #4 |
| 6 | Kubernetes API Conventions | PLANNED | High (when operator arrives) | `internal/k8s/conventions.md` (new) |
| 7 | CloudEvents | PLANNED | High (audit trail) | `services/observability/audit.go` (new) emit CloudEvents 1.0 |
| 8 | CNCF Serverless Workflow Specification | PLANNED | Low (workflow DSL import) | `docs/standards/mappings/sw-dsl-gaps.md` (new) |
| 9 | Reproducible Builds principles | PLANNED | High (provenance + supply chain) | `ci/local-runner/reproducible.sh` (new) + `Makefile` `-trimpath` |
| 10 | Nix reproducibility model | PLANNED | Medium (alternative hermetic runtime) | Deferred to slice 5+ — see §3.10 |
| 11 | Bazel hermetic build principles | PLANNED | Medium (cross-feed into hermetic-execution) | `tests/hermetic/bazel_principles_test.go` (new) |
| 12 | Content-Addressable Storage patterns | **IMPLEMENTED** | Done | n/a — see §3.12 for the existing-evidence pointer |
| 13 | Remote Execution API | PLANNED | Bundled with #11 / #14 | Bundled with #11 |
| 14 | Remote Cache protocols | PLANNED | High (Slice 3 portable cache #127) | `internal/cache/cas.go` (new) on top of `services/work/store/store.go` |

---

## §3. Per-standard mapping

### 3.1 OCI Runtime Spec (duplicate) (`oci-runtime`)

**Requirement (registry):** "(duplicate of oci-runtime-spec)".

**Applicability (§14.1):** **NOT_APPLICABLE — duplicate.** This row is
flagged in the registry as a duplicate of the CI-domain row
`oci-runtime-spec`. All enforcement work, conformance tests, and
evidence for the OCI Runtime Specification live on the CI side of the
registry under `oci-runtime-spec` (slice 3 plan: docker worker sandbox
conforms to OCI runtime spec, enforcement point
`tests/conformance/oci_test.go`). Tracking it here would inflate the
cross-mapping rollup; we point readers at the CI row instead.

**Current status (registry):** `NOT_APPLICABLE`. `control_id: "OCI-DUP"`,
`exceptions: ["Duplicate entry; consolidated under oci-runtime-spec."]`,
`enforcement_point: null`, `test: null`, `evidence: null`,
`implementation: "See oci-runtime-spec."`.

**Gap (§14.3):** **None on the platform side.** The CI-side row
(`oci-runtime-spec`) owns the gap analysis. Cross-cutting gap: no
`tests/conformance/` directory exists yet (referenced by both CI rows
and the OCI distribution duplicate below) — this is a single shared
fix that lives under the CI mapping, not this one.

**Next step (§14.5):** No new file path on the platform side. The
CI-side next step is `tests/conformance/oci_test.go` (new) and is
documented in `docs/standards/registry.json` row `oci-runtime-spec`.

**Risk × Leverage:** n/a — duplicate, not double-counted.

### 3.2 OCI Image Spec (duplicate) (`oci-image`)

**Requirement (registry):** "(duplicate of oci-image-spec)".

**Applicability (§14.1):** **NOT_APPLICABLE — duplicate.** Duplicate
of the CI-domain row `oci-image-spec` (`control_id: "OCI-IMG-DUP"`,
`exceptions: ["Duplicate entry; consolidated under oci-image-spec."]`,
`implementation: "See oci-image-spec."`). Bundled with the OCI Runtime
spec work.

**Current status (registry):** `NOT_APPLICABLE`. `enforcement_point`,
`test`, and `evidence` fields are all `null`.

**Gap (§14.3):** **None on the platform side.** Owned by the CI row
`oci-image-spec` (enforcement_point: `tests/conformance/`).

**Next step (§14.5):** No new file path on the platform side. CI-side
next step is `tests/conformance/oci_image_test.go` (new), bundled with
`oci-runtime-spec`.

**Risk × Leverage:** n/a — duplicate, not double-counted.

### 3.3 OCI Distribution Spec (duplicate) (`oci-distribution`)

**Requirement (registry):** "(duplicate of oci-distribution-spec)".

**Applicability (§14.1):** **NOT_APPLICABLE — duplicate.** Duplicate
of the CI-domain row `oci-distribution-spec` (`control_id: "OCI-DIST-DUP"`,
`exceptions: ["Duplicate entry."]`, `implementation: "See
oci-distribution-spec."`).

**Current status (registry):** `NOT_APPLICABLE`. `enforcement_point`,
`test`, and `evidence` fields are all `null`.

**Gap (§14.3):** **None on the platform side.** Owned by the CI row
`oci-distribution-spec` (future: image registry support). On the
platform side, the only future need is **registry-backed artifact
distribution** for the Portable Cache standard (§3.14) — that work
will reuse whatever registry the CI side adopts rather than carrying
its own distribution implementation.

**Next step (§14.5):** No new file path on the platform side. CI-side
next step is documented under `oci-distribution-spec`.

**Risk × Leverage:** n/a — duplicate, not double-counted.

### 3.4 WebAssembly Component Model (`wasm-component-model`)

**Requirement (registry):** "Wasm component model." `control_id: "WASM-CM"`,
`implementation: "Slice 5+: experimental wasm worker."`,
`enforcement_point: null`, `test: null`, `evidence: null`.

**Applicability (§14.1):** **In-scope, deferred to Slice 5+.** works-execution
is a multi-runtime worker platform; today the only worker type is
subprocess (`internal/worker/worker.go`). The Wasm component model is
the canonical portable, sandboxed alternative. The pack rule
`04_PLATFORM/RUNTIME_BASELINE.md` allows "alternative runtime only with
explicit hermeticity equivalence evidence"; Wasm CM plus WASI satisfies
that bar if we adopt it. Today, no wasm runtime is shipped.

**Current status (registry):** `PLANNED`. No Go Wasm host in
`internal/worker/`; no `wasmtime-go` (or equivalent) dependency in
`go.mod`.

**Gap (§14.3):**
1. No design doc stating **why** Wasm CM is the target (vs. e.g.
   gVisor or Firecracker-only sandboxing).
2. No `internal/worker/wasm/` package or interface to mount a wasm
   worker alongside the existing subprocess worker.
3. No conformance test even against a "hello world" wasm component.
4. No decision recorded on the wasm runtime (wasmtime, wasmer, wasi-sdk).

**Next step (§14.5):**
Create `docs/standards/mappings/wasm-adoption-rationale.md` (new)
recording (a) why Wasm CM was selected over Firecracker/gVisor,
(b) the chosen runtime and host SDK (`wasmtime-go` is the recommended
default — single-binary, embedded, MIT/Apache), (c) the conformance
test that will anchor it (`tests/wasm/component_model_test.go` new,
asserting a minimal WIT fixture loads, links, and runs), and
(d) the trigger that promotes this row from PLANNED to IMPLEMENTED
(slice 5 wasm-worker slice merge).

**Acceptance evidence:** `docs/standards/mappings/wasm-adoption-rationale.md`
exists; the rationale references the pack rule
`04_PLATFORM/RUNTIME_BASELINE.md`; `go.mod` does not yet need to pull
`wasmtime-go` — that is the slice 5 change.

**Risk × Leverage:** Medium × Low (today — purely experimental).

### 3.5 WASI (`wasi`)

**Requirement (registry):** "WebAssembly system interface."
`control_id: "WASI"`, `implementation: "Bundled with
wasm-component-model."`, `enforcement_point: null`, `test: null`,
`evidence: null`.

**Applicability (§14.1):** **In-scope, bundled with §3.4.** WASI is the
*system-interface* layer that the Wasm Component Model sits on top of;
they are inseparable for the works-execution use case (a worker that
needs filesystem + clock + random must have WASI preview 2 wired
through the component model). The registry already records this as
"bundled with wasm-component-model"; we re-affirm it here so the
duplicate-tracking stays explicit.

**Current status (registry):** `PLANNED`.

**Gap (§14.3):**
1. No WASI capability set recorded in the platform docs (which
   `wasi:filesystem`, `wasi:io`, `wasi:random`, `wasi:clocks` are
   in scope vs. out of scope).
2. No decision recorded on WASI preview 1 vs preview 2.

**Next step (§14.5):**
Bundled with §3.4 — the `wasm-adoption-rationale.md` (new) document
includes a "WASI capability set" subsection that names the four core
capabilities (filesystem, io, random, clocks) and freezes preview 2
as the target. No additional standalone next step on the WASI row.

**Acceptance evidence:** the §3.4 doc has a WASI capability subsection;
`docs/standards/mappings/wasm-adoption-rationale.md` references WASI
preview 2.

**Risk × Leverage:** Low × Low — bundled with §3.4.

### 3.6 Kubernetes API Conventions (`k8s-api-conventions`)

**Requirement (registry):** "K8s API design."
`control_id: "K8S-API"`, `implementation: "Slice 6+: K8s operator for
works-execution."`, `enforcement_point: null`, `test: null`,
`evidence: null`.

**Applicability (§14.1):** **In-scope, deferred to Slice 6+.** works-execution
will eventually publish a CRD (`Work`, `WorkLease`?) and an operator;
that surface must follow the upstream Kubernetes API conventions
(standard list/watch verbs, `metadata.name` and `metadata.namespace`
shape, `apiVersion` form, status subresource, condition types). Today
the only K8s-shaped work in the repo is the implicit API style of
`services/api/api.go` (RESTful JSON, not K8s-shaped), so this row is
future-looking.

**Current status (registry):** `PLANNED`. No CRD manifest, no
`internal/k8s/` package, no `controller-runtime` (or kubebuilder) code
generator in `tools/`.

**Gap (§14.3):**
1. No `internal/k8s/conventions.md` enumerating the k8s-API-conventions
   items we commit to (e.g. list/watch idempotency, status subresource,
   conditions with `type`, `status`, `observedGeneration`).
2. No CRD manifest skeleton (`manifests/crds/work.yaml` or similar).
3. No `tools/codegen/` directory hosting the operator codegen pipeline.

**Next step (§14.5):**
Create `internal/k8s/conventions.md` (new) that pins the conventions
list we will honor and references each upstream sig-api-machines
section. This is the lowest-cost forward step — it gives the slice 6
operator author a written contract to code against and lets us assert
adherence at code-review time even before the operator exists. The
slice 6 deliverable is `internal/k8s/operator/` (new) plus
`manifests/crds/work.yaml` (new), gated by this conventions doc.

**Acceptance evidence:** `internal/k8s/conventions.md` exists and
references https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
sections explicitly; the document is referenced from the registry row
on promotion to PARTIAL.

**Risk × Leverage:** Low today / High when operator arrives.

### 3.7 CloudEvents (`cloudevents`)

**Requirement (registry):** "Event format."
`control_id: "CLOUDEVENTS"`, `implementation: "Slice 3: audit events
emitted as CloudEvents."`, `enforcement_point:
"services/observability/audit.go."`, `test: "tests/audit/."`,
`evidence: "PLANNED"`.

**Applicability (§14.1):** **In-scope, high leverage.** works-execution
already records `work_evidence` rows in `services/work/store/store.go`
(slice 1, table at store.go:118-131) but emits them as plain DB rows,
not as wire-format events. Slice 3's audit trail is the natural
emission point, and CloudEvents 1.0 is the lingua franca for cross-tool
audit consumption (Tekton, Argo, Knative, OpenTelemetry, SIEMs).

**Current status (registry):** `PLANNED`. No
`services/observability/audit.go` file exists; no `tests/audit/`
directory exists; no CloudEvents SDK in `go.mod` (the canonical Go SDK
is `github.com/cloudevents/sdk-go/v2`).

**Gap (§14.3):**
1. No `services/observability/` directory (it does not exist on disk).
2. No CloudEvents struct definitions (`type AuditEvent struct {
   ce.ContextV1; Data json.RawMessage }`).
3. No emission site — `services/work/store/store.go` writes
   `work_evidence` rows but does not emit a CloudEvent.
4. No `tests/audit/` directory; no schema-validation test against the
   CloudEvents JSON Schema.

**Next step (§14.5):**
Create `services/observability/audit.go` (new) with two exports:
`type AuditEmitter interface { Emit(ctx, event AuditEvent) error }` and
the canonical implementation `SQLiteAuditEmitter` that wraps
`services/work/store/store.go` `RecordEvidence` calls and additionally
serializes the event as a CloudEvents 1.0 structured-mode message
written to a new `audit_events` table (so the SQLite store doubles as
the event log — no external broker required for V1). Add
`tests/audit/cloudevents_test.go` (new) that round-trips a fixture
event through `SQLiteAuditEmitter.Emit` and asserts the row matches
the CloudEvents 1.0 JSON Schema (use the official schema pinned at
`docs/standards/schemas/cloudevents-1.0.schema.json` new, vendored
from https://github.com/cloudevents/spec/blob/v1.0/json-schema.json).
Mount the emitter from `services/api/api.go` so work-create and
work-completion endpoints emit on the user's behalf.

**Acceptance evidence:** `services/observability/audit.go` exists,
`tests/audit/cloudevents_test.go` passes against the pinned
CloudEvents 1.0 schema, `make vet` is green.

**Risk × Leverage:** High × High — audit trail is the platform's
"did we do what we said" proof and CloudEvents is the format every
downstream tool already speaks.

### 3.8 CNCF Serverless Workflow Specification (`cncf-serverless-workflow`)

**Requirement (registry):** "Workflow DSL."
`control_id: "SW-DSL"`, `implementation: "Future: import workflows from
CNCF Serverless Workflow DSL."`, `enforcement_point: null`,
`test: null`, `evidence: null`.

**Applicability (§14.1):** **In-scope, deferred.** works-execution has
its own internal DAG definition (the `Work` graph) shipped in slice 1;
importing from CNCF Serverless Workflow DSL is an *interop* feature,
not a runtime correctness requirement. We will adopt when a customer
asks, or when the works-execution workflow model itself adopts the
SW-DSL shape — neither has happened.

**Current status (registry):** `PLANNED`.

**Gap (§14.3):**
1. No `docs/standards/mappings/sw-dsl-gaps.md` enumerating the gaps
   between works-execution's work-graph model and the CNCF SW-DSL
   schema (specifically `run`, `tasks`, `switch`, `foreach`, `raise`,
   `emit` shape).
2. No SW-DSL parser dependency in `go.mod` (the reference parser is
   `github.com/serverlessworkflow/sdk-go`).

**Next step (§14.5):**
Create `docs/standards/mappings/sw-dsl-gaps.md` (new) with two
sections: (a) "current model" — a brief description of the work-DAG
in `services/work/store/store.go` and `packages/workgraph/workgraph.go`;
(b) "DSL gap matrix" — a table mapping each SW-DSL top-level keyword
to the current works-execution primitive (`run` → `WorkSpec`,
`tasks` → `NodeSpec`, `switch` → no equivalent, `foreach` → no
equivalent, `emit` → planned CloudEvents emission from §3.7). This
document is the trigger condition for promoting the row from PLANNED
to PARTIAL: if any gap matrix cell is filled with "no equivalent" and
a customer pulls in a workflow that needs that shape, we promote.

**Acceptance evidence:** `docs/standards/mappings/sw-dsl-gaps.md`
exists and references the upstream CNCF spec at
https://serverlessworkflow.io/schemas/0.8/workflow.json.

**Risk × Leverage:** Low × Low today; becomes Medium × High if a
workflow arrives that needs it.

### 3.9 Reproducible Builds principles (`reproducible-builds`)

**Requirement (registry):** "Bit-identical builds."
`control_id: "REPRO"`, `implementation: "Slice 3: build with -trimpath,
fixed SOURCE_DATE_EPOCH."`, `enforcement_point:
"ci/local-runner/reproducible.sh."`, `test:
"tests/build/reproducible_test.go"`, `evidence: "PLANNED"`.

**Applicability (§14.1):** **In-scope, high leverage.** Reproducible
builds are the prerequisite for cryptographic provenance (SLSA L3+)
and for the `cas-patterns` row (§3.12) to mean anything beyond "we
stored a hash." Without `SOURCE_DATE_EPOCH` + `-trimpath` + a pinned
toolchain, two builds of the same source produce different binaries,
which means content-addressing identifies an artifact but not a
*reproducible* artifact.

**Current status (registry):** `PLANNED`. `Makefile` (read above) has
4 lines — does not currently pass `-trimpath` or set
`SOURCE_DATE_EPOCH`. No `ci/local-runner/` directory exists. No
`tests/build/` directory exists.

**Gap (§14.3):**
1. `Makefile` does not pass `-trimpath` to `go build`.
2. `Makefile` does not export `SOURCE_DATE_EPOCH=$(git log -1
   --pretty=%ct)` before invoking the toolchain.
3. No `ci/local-runner/reproducible.sh` — the registry points at it
   before the file exists.
4. No `tests/build/reproducible_test.go` — the registry points at it
   before the file exists.
5. Toolchain is pinned in `go.mod` only at the module level; Go's
   default build does not record the compiler version into the binary,
   which is required for true reproducibility checks (need `go version
   -m file` and a recorded compiler version in evidence).

**Next step (§14.5):**
Three concrete edits, all small:
1. `Makefile` (extend): add `GOFLAGS := -trimpath` and a
   `SOURCE_DATE_EPOCH ?= $(shell git log -1 --pretty=%ct)` line;
   export both before the `build:` target. Allow override via env.
2. Create `ci/local-runner/reproducible.sh` (new) — a wrapper script
   that (a) records the toolchain version (`go version`), (b) runs
   the build twice into two distinct output dirs, (c) `sha256sum`s
   both binaries, (d) diffs the two sums, (e) exits non-zero on
   mismatch.
3. Create `tests/build/reproducible_test.go` (new) that invokes
   `ci/local-runner/reproducible.sh` from the test (skipping
   gracefully if `bash` is unavailable) and asserts the two
   resulting sha256 sums are equal.

**Acceptance evidence:** `make build` produces identical sha256 across
two runs on the same commit; `tests/build/reproducible_test.go`
passes; `ci/local-runner/reproducible.sh` exists and is executable.

**Risk × Leverage:** High × High — feeds directly into SLSA L3
provenance and into the supply-chain mapping (`docs/standards/mappings/supply-chain.md`).

### 3.10 Nix reproducibility model (`nix-reproducibility`)

**Requirement (registry):** "Pure functional builds."
`control_id: "NIX"`, `implementation: "Slice 5+: experimental Nix worker."`,
`enforcement_point: null`, `test: null`, `evidence: null`.

**Applicability (§14.1):** **In-scope, deferred to Slice 5+.** Nix is
an alternative hermetic runtime for the works-execution worker; today
all builds run through the host's Go toolchain (see §3.9). Adopting
Nix would buy true pure-functional builds at the cost of adding a
`nix/` flake and a Nix-based worker implementation. There is no V1
demand; the registry defers this to slice 5+ alongside the Wasm
worker (§3.4).

**Current status (registry):** `PLANNED`. No `flake.nix` in repo root;
no `internal/worker/nix/` package.

**Gap (§14.3):**
1. No `flake.nix` to anchor a Nix-based build.
2. No design doc comparing Nix-based reproducibility to the
   `-trimpath` + `SOURCE_DATE_EPOCH` approach from §3.9.
3. No worker harness that can take a Nix store path as input.

**Next step (§14.5):**
**Deferred — no new file path in this mapping.** The slice 5+ trigger
is "any audit asks for functional-purity reproducibility that
`-trimpath` + `SOURCE_DATE_EPOCH` cannot deliver" or "we adopt Wasm CM
and want a hermetic toolchain for the *worker binary* itself." When
the trigger fires, the slice 5 deliverable is `flake.nix` (new) plus
`internal/worker/nix/` (new). Until then, §3.9 covers our
reproducibility needs at lower cost.

**Acceptance evidence:** None today. Promote to PARTIAL when
`flake.nix` lands.

**Risk × Leverage:** Low today / Medium when triggered.

### 3.11 Bazel hermetic build principles (`bazel-hermetic`)

**Requirement (registry):** "Hermetic builds."
`control_id: "BAZEL-HERM"`, `implementation: "Slice 3: hermetic
execution standard (#111) takes inspiration."`,
`enforcement_point: "tests/hermetic/.", test: null`, `evidence: null`.

**Applicability (§14.1):** **In-scope, design influence.** Bazel's
hermetic execution model (sandboxed actions, content-addressed inputs,
explicit toolchain declarations) is the conceptual reference for the
in-house "Hermetic Execution Standard" (`platform-hermetic-execution`,
slice 3, separate registry row outside the scope of this document).
This row records that we *consult* Bazel's principles when designing
in-house hermeticity — we are not adopting the Bazel toolchain itself.

**Current status (registry):** `PLANNED`. `enforcement_point:
"tests/hermetic/."` but no `tests/hermetic/` directory exists.

**Gap (§14.3):**
1. No `tests/hermetic/` directory; no `tests/hermetic/bazel_principles_test.go`.
2. No `docs/standards/mappings/bazel-principles-applied.md` listing the
   specific Bazel principles we adopted (sandboxed actions, declared
   toolchains, content-addressed inputs) and the works-execution
   component that realizes each.

**Next step (§14.5):**
Create `tests/hermetic/bazel_principles_test.go` (new) — a
documentary test that enumerates the Bazel principles
(sandboxed_action, declared_toolchain, content_addressed_input,
hermetic_workspace) and asserts that each has a documented
works-execution counterpart, with the counterparts cited inline. The
test's body can be entirely string-table driven — its purpose is to
keep the mapping alive and reviewable, not to test runtime
behavior. File path: `tests/hermetic/bazel_principles_test.go` (new),
plus `docs/standards/mappings/bazel-principles-applied.md` (new) as
the per-principle evidence pointer the test reads from.

**Acceptance evidence:** `tests/hermetic/bazel_principles_test.go`
passes; `docs/standards/mappings/bazel-principles-applied.md` exists
and is referenced from this mapping.

**Risk × Leverage:** Medium × Medium — feeds hermetic-execution
design without itself being runtime-critical.

### 3.12 Content-Addressable Storage patterns (`cas-patterns`) — IMPLEMENTED

**Requirement (registry):** "Content-addressed storage."
`control_id: "CAS"`, `implementation: "Already implemented in slice 1
(#116 Content-Addressed Everything)."`,
`enforcement_point: "tests/cas/."`, `test: "services/work/store tests"`,
`evidence: "services/work/store/store.go"`, `status: "IMPLEMENTED"`.

**Applicability (§14.1):** **In-scope, IMPLEMENTED in slice 1.**
works-execution's artifact table in `services/work/store/store.go`
identifies every artifact by a content hash:

```sql
CREATE TABLE IF NOT EXISTS work_artifacts (
    work_id    TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    id         TEXT NOT NULL, -- content hash
    node_id    TEXT NOT NULL,
    mime_type  TEXT NOT NULL,
    size       INTEGER NOT NULL,
    path       TEXT NOT NULL,
    PRIMARY KEY (work_id, id)
);
```

The `id` column is the content hash; the `(work_id, id)` composite
primary key is the canonical CAS key for that artifact within the
work. Slice 1's commit (`d3db1d1`) is the implementation evidence.

**Current status (registry):** `IMPLEMENTED`. The registry's
`enforcement_point` field points at `tests/cas/`, which does not exist
on disk yet — the test directory is a slice 3 deliverable that
*attests* the implementation rather than creating it. The
implementation evidence (`services/work/store/store.go`, line ~113 —
the `work_artifacts` table) is concrete and present.

**Gap (§14.3):**
1. `tests/cas/` directory does not exist; the registry points at it
   before the tests are written. The implementation is real but the
   dedicated test package is not.
2. The CAS key type is currently a raw `TEXT` column; no Go-level
   `type ArtifactID [32]byte` (or hex-string alias) wrapping it.
   Search-and-replace for `sha256-` style identifiers is implicit in
   `services/work/store/store_test.go:245` (`ID: "sha256_same"`) but
   not formalized.

**Next step (§14.5):**
Create `tests/cas/patterns_test.go` (new) that exercises the CAS
contract end-to-end:
- `TestArtifactIDIsContentHash` — write a fixture file, hash it, assert
  that `store.RecordArtifact` stores the hash as the row `id`.
- `TestSameContentDifferentWorkCollides` — assert that two works
  uploading the same content produce the same `id` (CAS collision by
  design).
- `TestArtifactIDIsHexSHA256` — assert the `id` column is a 64-char
  lowercase hex string (matches the sha256-hex convention used
  elsewhere in the repo).

Also add `internal/fingerprint/fingerprint.go` (new) defining
`type ArtifactID string` with a `func IDForBytes([]byte) ArtifactID`
constructor, and call it from `services/work/store/store.go`'s
`RecordArtifact` so the type is enforced at the API boundary rather
than relying on stringly-typed discipline at the call site.

**Acceptance evidence:** `tests/cas/patterns_test.go` exists and
passes; `internal/fingerprint/fingerprint.go` exists and is wired
into `RecordArtifact`; `make test` is green.

**Risk × Leverage:** Low (already implemented) × High (foundation
for every other build/reproducibility row and for the supply-chain
mapping's provenance chain).

### 3.13 Remote Execution API (`remote-execution-api`)

**Requirement (registry):** "Remote build exec."
`control_id: "RE-API"`, `implementation: "Bundled with
bazel-remote-execution-api."`, `enforcement_point: null`, `test: null`,
`evidence: null`.

**Applicability (§14.1):** **In-scope, bundled with §3.14 and the
CI-domain row `bazel-remote-execution-api`.** The Remote Execution API
v2 (the Bazel Build Remote Execution API plus its RE-API v2 update)
is the protocol we'd speak if we hosted a remote build execution
service. We have no such service today and no concrete plan to host
one — the works-execution worker is a subprocess executor and the
plan stays on the local-host / single-tenant execution model for V1.
The registry records this as "bundled with bazel-remote-execution-api"
and we re-affirm the bundling here so duplicate-tracking stays clean.

**Current status (registry):** `PLANNED`.

**Gap (§14.3):**
1. No RE-API server scaffold (e.g. `internal/remoteexec/`).
2. No decision recorded on whether to *be* an RE-API server (expose
   our executor remotely) or *speak* RE-API to a third party
   (e.g. BuildBarn, BuildBuddy) for hosted cache + exec.

**Next step (§14.5):**
Bundled with the CI-side row `bazel-remote-execution-api` — no new
file path on the platform side. When the row promotes to PARTIAL, the
shared deliverable is `docs/standards/mappings/remote-exec-decision.md`
(new) recording the be-it-or-speak-it decision and referencing the
upstream Bazel RE-API v2 spec at
https://github.com/bazelbuild/remote-apis.

**Acceptance evidence:** none today. Promote to PARTIAL when
`remote-exec-decision.md` lands.

**Risk × Leverage:** Low × Low — fully bundled.

### 3.14 Remote Cache protocols (`remote-cache`)

**Requirement (registry):** "Remote build cache."
`control_id: "REMOTE-CACHE"`, `implementation: "Slice 3: content-addressed
cache (#127 Portable Cache)."`, `enforcement_point: null`, `test: null`,
`evidence: null`.

**Applicability (§14.1):** **In-scope, slice 3 deliverable.** The
"Portable Cache" standard (`platform-portable-cache`, separate registry
row, slice 3) is the works-execution-flavored realization of this row:
a content-addressed cache that is portable across hosts (HTTP REST
+ S3-compatible backend) and identified by the same `ArtifactID` type
introduced in §3.12. The Remote Cache protocols row is the *standard*
this implementation conforms to (the closest upstream shape is the
Bazel Remote Cache API, which is a strict subset of RE-API v2's
`ActionCache` + `ContentAddressableStorage` services).

**Current status (registry):** `PLANNED`. No `internal/cache/cas.go`
on disk; no `cmd/works-cache/` binary; no HTTP route exposing
cache-by-hash in `services/api/api.go`.

**Gap (§14.3):**
1. No `internal/cache/` package at all.
2. No `GET /v1/cache/blobs/{sha256}` or `PUT /v1/cache/blobs/{sha256}`
   route on the works-execution HTTP API.
3. No `cmd/works-cache/` binary that fronts a remote cache (S3 or
   filesystem-backed) with the works-execution auth middleware from
   `services/api/auth.go` (see identity mapping §2.1).
4. No eviction policy recorded.

**Next step (§14.5):**
Create `internal/cache/cas.go` (new) with:
- `type Cache interface { Get(ctx, id ArtifactID) (io.ReadCloser, error); Put(ctx, id ArtifactID, r io.Reader) error; Has(ctx, id ArtifactID) (bool, error) }`
- a `FilesystemCache` implementation backed by a configurable root
  directory (sha256-sharded: `root/aa/bb/<rest of hash>`).
- a `RemoteCache` implementation speaking HTTP to a configurable
  base URL with bearer-token auth.

Wire `FilesystemCache` into `services/work/store/store.go`'s
`RecordArtifact` so the in-process store and the CAS cache share
writes; wire `RemoteCache` into the works-execution CLI
(`cmd/works/`) so CLI users can pull cached artifacts from a remote
cache before submitting a work. Add `tests/cache/cas_test.go` (new)
with table-driven cases for Get/Put/Has on both backends.

**Acceptance evidence:** `internal/cache/cas.go` exists; `make test`
runs `tests/cache/cas_test.go` green; an integration smoke in
`e2e/cache_test.go` (new) demonstrates a CLI `works pull` from a
local `RemoteCache` against a fresh `FilesystemCache` populator.

**Risk × Leverage:** Medium × High — the Portable Cache is what makes
works-execution *not* require every user to have every dep locally.

---

## §4. Cross-cutting observations

1. **Three explicit duplicates.** `oci-runtime`, `oci-image`, and
   `oci-distribution` are registry-flagged duplicates of the
   CI-domain rows `oci-runtime-spec`, `oci-image-spec`, and
   `oci-distribution-spec`. They are recorded in this mapping **for
   traceability only** and must **not** be counted in the
   "active standards" denominator. §6 below calls them out
   explicitly so the rollup at `docs/standards/registry.json` and
   any downstream PR-comment checks do not double-count them.
2. **Missing test directories claimed by the registry.** `tests/cas/`
   (§3.12), `tests/audit/` (§3.7), `tests/build/` (§3.9), `tests/hermetic/`
   (§3.11), `tests/wasm/` (§3.4), `tests/cache/` (§3.14) all do not
   exist yet; the registry points at them. Highest-leverage single fix:
   stand up the six packages as part of the slice 3 deliverables named
   in each row's §3.5/§3.7/§3.9/§3.11/§3.12/§3.14.
3. **`ci/local-runner/` does not exist.** `tests/hermetic/` and
   `tests/build/reproducible_test.go` both point at
   `ci/local-runner/reproducible.sh`, but no `ci/local-runner/`
   directory is on disk. Establishing the directory is a precondition
   for §3.9 and §3.11.
4. **CAS is the foundation row.** `cas-patterns` (§3.12) is the only
   IMPLEMENTED row in this domain; `reproducible-builds` (§3.9),
   `remote-cache` (§3.14), `platform-content-addressed` (sibling
   row outside scope), and the supply-chain provenance chain all
   sit on top of it. Promote §3.12 to VERIFIED as soon as
   `tests/cas/patterns_test.go` lands — that promotion is the
   single biggest leverage move in the platform domain.
5. **OCI row consolidation ticket.** The three `oci-runtime` /
   `oci-image` / `oci-distribution` duplicates are tracked here only
   because the registry carries them; a registry-cleanup ticket that
   removes these three rows from the platform domain (or moves them
   to a `merged` sub-status) would tighten the rollup. Out of scope
   for this mapping document; tracked separately.

---

## §5. Highest-value actionable gaps (rolled up, with file paths)

| Rank | Standard | File path (new) | Acceptance |
|------|----------|-----------------|------------|
| 1 | `cas-patterns` → VERIFIED | `tests/cas/patterns_test.go`, `internal/fingerprint/fingerprint.go` | `make test` green; type enforced at `RecordArtifact` boundary |
| 2 | `cloudevents` → PARTIAL | `services/observability/audit.go`, `tests/audit/cloudevents_test.go`, `docs/standards/schemas/cloudevents-1.0.schema.json` | audit event round-trips through CE 1.0 JSON Schema |
| 3 | `reproducible-builds` → PARTIAL | `Makefile` (extend), `ci/local-runner/reproducible.sh`, `tests/build/reproducible_test.go` | two `make build` runs produce identical sha256 |
| 4 | `remote-cache` → PARTIAL | `internal/cache/cas.go`, `tests/cache/cas_test.go`, `e2e/cache_test.go` | CLI pulls a cached artifact via `RemoteCache` |
| 5 | `bazel-hermetic` → PARTIAL | `tests/hermetic/bazel_principles_test.go`, `docs/standards/mappings/bazel-principles-applied.md` | documentary test passes; mapping doc referenced |

The single highest-value actionable change is **§3.12 / Rank 1**:
stand up `tests/cas/patterns_test.go` and `internal/fingerprint/fingerprint.go`.
It anchors the only IMPLEMENTED row in this domain, makes the
existing CAS contract testable, and gives every downstream row
(§3.9 reproducible builds, §3.14 remote cache, the supply-chain
provenance chain) a typed, enforced CAS identifier to build on.

---

## §6. Traceability table (all 14 enumerated rows)

| #  | Standard | Registry row | Status | Enforcement point (file) | Test (file) | Evidence pointer | Active? |
|----|----------|--------------|--------|--------------------------|-------------|------------------|---------|
| 1  | OCI Runtime Spec (duplicate) | `oci-runtime` | NOT_APPLICABLE | (CI row owns: `tests/conformance/oci_test.go`) | (CI row owns) | (CI row owns) | **DUPLICATE — not double-counted** |
| 2  | OCI Image Spec (duplicate) | `oci-image` | NOT_APPLICABLE | (CI row owns: `tests/conformance/oci_image_test.go`) | (CI row owns) | (CI row owns) | **DUPLICATE — not double-counted** |
| 3  | OCI Distribution Spec (duplicate) | `oci-distribution` | NOT_APPLICABLE | (CI row owns: `oci-distribution-spec` mapping) | (CI row owns) | (CI row owns) | **DUPLICATE — not double-counted** |
| 4  | WebAssembly Component Model | `wasm-component-model` | PLANNED | `internal/worker/wasm/` (slice 5+) | `tests/wasm/component_model_test.go` (new) | `docs/standards/mappings/wasm-adoption-rationale.md` (new) | active |
| 5  | WASI | `wasi` | PLANNED | bundled with #4 | bundled with #4 | bundled with #4 | active (bundled) |
| 6  | Kubernetes API Conventions | `k8s-api-conventions` | PLANNED | `internal/k8s/conventions.md` (new) | n/a (slice 6+) | this doc + conventions.md | active |
| 7  | CloudEvents | `cloudevents` | PLANNED | `services/observability/audit.go` (new) | `tests/audit/cloudevents_test.go` (new) | this doc + test output + CE 1.0 schema | active |
| 8  | CNCF Serverless Workflow Specification | `cncf-serverless-workflow` | PLANNED | n/a (deferred) | n/a (deferred) | `docs/standards/mappings/sw-dsl-gaps.md` (new) | active |
| 9  | Reproducible Builds principles | `reproducible-builds` | PLANNED | `ci/local-runner/reproducible.sh` (new) + `Makefile` (extend) | `tests/build/reproducible_test.go` (new) | this doc + reproducible.sh output | active |
| 10 | Nix reproducibility model | `nix-reproducibility` | PLANNED | deferred to slice 5+ | deferred | deferred | active (deferred) |
| 11 | Bazel hermetic build principles | `bazel-hermetic` | PLANNED | `tests/hermetic/bazel_principles_test.go` (new) | (self-test) | `docs/standards/mappings/bazel-principles-applied.md` (new) | active |
| 12 | Content-Addressable Storage patterns | `cas-patterns` | **IMPLEMENTED** | `services/work/store/store.go` (line ~113, `work_artifacts.id`) | `services/work/store/store_test.go` + `tests/cas/patterns_test.go` (new) | `services/work/store/store.go` | **active — promote to VERIFIED** |
| 13 | Remote Execution API | `remote-execution-api` | PLANNED | bundled with CI row `bazel-remote-execution-api` | bundled | bundled | active (bundled) |
| 14 | Remote Cache protocols | `remote-cache` | PLANNED | `internal/cache/cas.go` (new) | `tests/cache/cas_test.go` (new) + `e2e/cache_test.go` (new) | this doc + test output | active |

---

## §7. Cross-mapping rollup pointer

For the cross-mapping rollup at `docs/standards/registry.json` and any
downstream PR-comment / governance checks:

- **Total rows in scope: 14** (8 platform/containers + 6 build/
  reproducibility, per the parent task's explicit enumeration).
- **Distinct active rows after de-dup: 11** (14 − 3 duplicates). These
  are the 11 rows in the §6 traceability table flagged as "active":
  3.4–3.14.
- **Duplicate rows: 3** (`oci-runtime`, `oci-image`,
  `oci-distribution`) — these resolve to the CI-domain rows
  `oci-runtime-spec`, `oci-image-spec`, `oci-distribution-spec` and
  must be **counted once under the CI domain**, not twice.
- **Status totals contributed by this mapping (distinct active rows
  only, NOT counting duplicates):** 1 × IMPLEMENTED (`cas-patterns`),
  10 × PLANNED.
- **Duplicates contribute: 3 × NOT_APPLICABLE** — these are reported
  here for traceability and are not added to the active-status
  denominator at the registry level.

---

## §8. Acceptance for this mapping document

- [x] All 14 platform/containers + build/reproducibility rows mapped (§§3.1–3.14).
- [x] Three explicit duplicates (`oci-runtime`, `oci-image`, `oci-distribution`) called out and not double-counted (§1, §6, §7).
- [x] Per-standard fields complete for each active row: applicability, status, gap, next step, file path (§3).
- [x] §14 five-step rule applied to every row.
- [x] Highest-value actionable gaps identified with file paths (§5).
- [x] `cas-patterns` (§3.12) recorded as the only IMPLEMENTED row with concrete slice-1 evidence pointer (`services/work/store/store.go` `work_artifacts.id` content-hash column).
- [x] Cross-mapping rollup pointer (§7) makes the "11 active + 3 duplicates" arithmetic explicit for downstream consumers.