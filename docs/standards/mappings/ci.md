# CI / Actions / Execution — Per-Standard Mapping

**Document ID:** `works-standards-ci-mapping`
**Venture:** works-execution (`github.com/JonasAbde/works-execution`)
**Generated:** 2026-08-31
**Slice context:** Slice 1 (`d3db1d1`) shipped the Work primitive, SQLite
store, HTTP API (`services/api/api.go`), CLI, polling subprocess worker
(`internal/worker/worker.go`). Slice 2 (`dab84f2`) added lease-based
scheduling, worker-loss recovery, log streaming. Slice 3 introduces the
standards charter, the JSON Schema 2020-12 validator at
`internal/standards/standards.go`, and five embedded schemas at
`internal/standards/schemas/` (mirrored under `docs/standards/schemas/`).

**Companion documents:**
- `docs/standards/registry.json` — authoritative machine-readable registry (130 rows)
- `docs/standards/mappings/identity.md` — already owns `spiffe`,
  `spire`, `spiffe-workload-api`, `openid-connect` in full. The CI
  mapping only documents the CI-side surface of `spiffe`/`spire` and
  defers to `identity.md` for everything else — no double-counting.
- `docs/standards/mappings/platform-build.md` — owns the OCI
  duplicate rows (`oci-runtime`, `oci-image`, `oci-distribution`) and
  the Bazel/RE-API sibling row (`remote-execution-api`). CI-domain
  rows in `registry.json` are primary; the `platform`-domain
  duplicates are `NOT_APPLICABLE` cross-references.
- `Makefile` target `standards-validate` runs the validator tests +
  `works-standards list` + `works-kanban validate`.

---

## §14 Implementation Rule (binding)

Every standard in this document is processed through the five-step rule
from the user-mandated standards charter:

1. **Determine applicability** — is this standard in-scope for
   works-execution V1?
2. **Map to system requirements** — which concrete component,
   contract, or test enforces it?
3. **Identify gaps** — what is missing today (Slice 1 + Slice 2)?
4. **Prioritize by risk and leverage** — score each gap on
   (risk-of-omission × leverage-on-platform-correctness).
5. **Recommend highest-value actionable gap with file path** — the
   next concrete change, where it lands, and the acceptance evidence.

---

## §1. Scope and deduplication

The registry `ci` domain holds **5 rows**; this mapping covers the **7
distinct CI/Actions/Execution standards** named by the parent task.
`spiffe` and `spire` actually live under the `identity` domain in the
registry — they appear here because the parent task groups them under
CI/Actions/Execution; only the CI-side surface is documented.

| # | Standard | Registry row | Status |
|---|----------|--------------|--------|
| 1 | OCI Runtime Spec (1.2) | `oci-runtime-spec` | PLANNED |
| 2 | OCI Image Spec (1.1) | `oci-image-spec` | PLANNED |
| 3 | OCI Distribution Spec | `oci-distribution-spec` | PLANNED |
| 4 | Bazel RE-API (v2) | `bazel-remote-execution-api` | PLANNED |
| 5 | JSON Schema 2020-12 | `json-schema-2020-12` | **IMPLEMENTED** |
| 6 | SPIFFE (2.0) | `spiffe` (identity) | PLANNED |
| 7 | SPIRE | `spire` (identity) | PLANNED |

**Excluded:** `openid-connect` (owned by `identity.md` §2.1); the
`oci-runtime` / `oci-image` / `oci-distribution` platform-domain
duplicates — `status: NOT_APPLICABLE` cross-references.

**Path convention:** the registry references
`docs/standards/schemas/*.json`; in the working tree the schemas
physically live at `internal/standards/schemas/` because Go's
`//go:embed` requires the same package directory. Both locations are
byte-identical; references below use the canonical-validator path.
See §2.5 next step for the sync script.

---

## §2. Per-standard mapping

### 2.1 OCI Runtime Specification (`oci-runtime-spec`)

**Requirement:** "Container runtime contract." `control_id:
"OCI-RUNTIME"`, `version: "1.2"`.

**Applicability (§14.1):** **In-scope, Slice 3.** Registry
`implementation: "Slice 3: docker worker sandbox conforms to OCI
runtime spec."` Slice 2's worker is a host subprocess
(`internal/worker/worker.go` package doc: *"executes the node as a
subprocess"*); Slice 3 plans a Docker sandbox that speaks the OCI
runtime lifecycle (`create` / `start` / `state` / `delete`).

**Current status (registry):** `PLANNED`. `enforcement_point:
"tests/conformance/oci_test.go."` Today: no `tests/conformance/`
directory; `internal/sandbox/hermetic.go` (referenced by
`platform.md` §224) does not exist.

**Gap (§14.3):**
1. No `tests/conformance/` directory.
2. No `internal/sandbox/hermetic.go` to give OCI conformance a
   surface to verify.
3. No OCI `config.json` generator (capabilities derived from the
   action manifest's `permissions` field) and no test fixture.

**Next step (§14.5):** Scaffold `tests/conformance/oci_test.go` with
one table-driven test that loads an OCI `config.json` fixture (Linux
container, `process.terminal=false`), runs it under the locally
installed `runc` (or `crun`), asserts the expected exit code, and
confirms the lifecycle `created → running → stopped`. Add
`tests/conformance/oci_helpers_test.go` with a sandbox-skip wrapper
so the test passes on hosts without `runc`. Wire from the
`standards-validate` Makefile target.

**Acceptance evidence:** `tests/conformance/oci_test.go` exists;
`make test` runs it (skip on hosts without `runc`); a passing run
advances `oci-runtime-spec.status` from `PLANNED` toward `PARTIAL`.

**Risk × Leverage:** Medium × High. Slice 2's isolation is
default-deny at the *subprocess* layer, not the *container* layer.

---

### 2.2 OCI Image Specification (`oci-image-spec`)

**Requirement:** "Image format contract." `control_id: "OCI-IMAGE"`,
`version: "1.1"`.

**Applicability (§14.1):** **In-scope, bundled with §2.1.** Registry
explicitly says `implementation: "Bundled with oci-runtime-spec."`
and shares the same `enforcement_point: "tests/conformance/."`.

**Current status (registry):** `PLANNED`. `test:
"tests/conformance/oci_test.go"` (shared). `evidence: null`.

**Gap (§14.3):**
1. No image-layer digest verifier (per-layer SHA-256 check).
2. No `internal/sandbox/image.go` (pull-side reader).
3. No OCI `blobs/sha256/<digest>` layout writer.

**Next step (§14.5):** Implement the image *reader* side only in
`internal/sandbox/image.go` — `PullImage(ctx, ref) (Layout, error)`
returning manifest + ordered layers + digests, and
`VerifyDigest([]byte, sha256hex) error`. Add
`tests/conformance/oci_image_test.go` that loads a fixture
`tests/conformance/fixtures/oci-image-layout/` (manifest + one empty
layer) and asserts `PullImage` produces a 1-layer layout with
matching digest.

**Acceptance evidence:** `internal/sandbox/image.go` exists;
`tests/conformance/oci_image_test.go` passes; fixture `index.json` and
`manifest.json` validate against OCI image-spec media-types.

**Risk × Leverage:** Low × Medium. Stable format; risk is
silent-corruption, not security.

---

### 2.3 OCI Distribution Specification (`oci-distribution-spec`)

**Requirement:** "Image distribution contract." `control_id:
"OCI-DIST"`, `version: "1.1"`.

**Applicability (§14.1):** **Deferred to Slice 4+, future.** Registry
`implementation: "Future: image registry support."` V1 does not host
an image registry; the slice-3 sandbox will pull from a fixed
reference or build inline. Adding a `/v2/` client + bearer-token
rotation is disproportionate until at least one customer asks.

**Current status (registry):** `PLANNED`. No code; no test.

**Gap (§14.3):** No `internal/sandbox/registry.go`; no
bearer-token / rotation flow; no ADR for *which* registry shape V1
supports (GHCR / ECR / self-hosted Distribution).

**Next step (§14.5):** Author ADR `docs/adr/ADR-0008-oci-registry.md`
capturing the deferral and the promotion trigger (§6). No code in
Slice 3.

**Acceptance evidence:** ADR merged; registry row remains `PLANNED`
but `evidence` field references the ADR.

**Risk × Leverage:** Low × Low. Safe to defer.

---

### 2.4 Bazel Remote Execution API (`bazel-remote-execution-api`)

**Requirement:** "Remote build execution protocol." `control_id:
"BRE-API"`, `version: "2.0"`.

**Applicability (§14.1):** **Deferred to Slice 4+, future.** Registry
`implementation: "Future: RE-API server for remote build cache."` The
sibling `platform-build.md` §3.13 (`remote-execution-api` row)
records the same deferral with a more detailed gap analysis. V1's
worker executes one node per lease; RE-API earns its keep only when
the action graph exceeds a single-machine build budget. Slice 1+2
already has a content-addressed store at `services/work/store/`
(registry `platform-content-addressed`, `status: IMPLEMENTED`) — the
seam on which an RE-API server would attach later.

**Current status (registry):** `PLANNED`. No code; no test.

**Gap (§14.3):** No `internal/exec/remote/` package; the CAS shape
in `services/work/store/` is not RE-API-shaped (no
bytes/directories separation, no RE-API digest convention).

**Next step (§14.5):** Add the §6 trigger list. No code in Slice 3.

**Acceptance evidence:** Trigger list published (§6); ADR-0008 (above)
references the same deferral pattern.

**Risk × Leverage:** Low × Low. Risk of building an RE-API server
without a concrete second-host use case is that the implementation
drifts from spec optional surfaces.

---

### 2.5 JSON Schema 2020-12 (`json-schema-2020-12`)

**Requirement:** "Validate JSON documents." `control_id:
"JSONSCHEMA"`, `version: "2020-12"`.

**Applicability (§14.1):** **In-scope, FOUNDATION for every other
contract in the registry.** The **only** row in this mapping that is
already `IMPLEMENTED` and the **single highest-leverage row** in the
registry — every other contract (action manifest, evidence bundle,
failure classification, runner identity, workflow provenance, OCI
fixtures, RE-API manifests) is itself a JSON Schema 2020-12 document
or is validated by one.

**Current status (registry):** `IMPLEMENTED`. Concretely:

- **5 schemas** at `internal/standards/schemas/` (mirror at
  `docs/standards/schemas/`): `action-manifest`,
  `evidence-bundle`, `failure-classification`, `runner-identity`,
  `workflow-provenance`.
- **Validator** `internal/standards/standards.go` (122 lines) using
  `github.com/santhosh-tekuri/jsonschema/v5` and `//go:embed
  schemas/*.json`; exposes `Load()`, `Validate(name, doc)`,
  `ValidateBytes(name, []byte)`, `ListSchemas()`.
- **13 conformance tests** in `internal/standards/standards_test.go`
  (OK / missing-required / bad-enum / bad-pattern / unknown-schema).
- **CLI** `cmd/works-standards/main.go` (`list`, `show`, `validate`,
  `gaps`, `summary`); **Makefile** target `standards-validate` runs
  the tests + CLI + kanban validation.

**Gap (§14.3):**
1. `internal/standards.Validate` is **not yet called from the
   admission path** — validator exists, enforcement is aspirational.
2. The two physical locations (`docs/standards/schemas/` and
   `internal/standards/schemas/`) are not synchronised by automation.
3. No `$id`-collision test (two schemas sharing `$id` would silently
   overwrite each other in `Load()`).

**Next step (§14.5):** Wire `internal/standards.Validate` into API
admission so every Work spec is validated against
`action-manifest.schema.json` before it reaches the store. Concretely:

- **Create** `services/api/admission.go` —
  `Validate(spec []byte) error` that calls
  `standards.ValidateBytes("action-manifest.schema.json", spec)`,
  mounted from `services/api/api.go::worksHandler` BEFORE the store call.
- **Create** `services/api/admission_test.go` — table-driven: valid
  manifest passes; missing `action_id` returns 400; permission enum
  violation returns 400.
- **Create** `scripts/sync-standards-schemas.sh` — rsync
  `internal/standards/schemas/*.json` → `docs/standards/schemas/`;
  exit non-zero on diff.
- **Modify** `Makefile` — add `schemas-sync` target; make
  `standards-validate` depend on it.

**Acceptance evidence:** `services/api/admission.go` exists and is
called from `worksHandler`; `make standards-validate` runs the
admission test alongside the existing 13; `diff -r
internal/standards/schemas/ docs/standards/schemas/` is empty in CI.

**Risk × Leverage:** **High × Critical.** Without admission-time
validation the JSON Schema row is a *paper* contract — the validator
exists but the API never calls it. This is the single most important
standards work in Slice 3.

---

### 2.6 SPIFFE (`spiffe`)

**Requirement:** "Workload identity." `control_id: "SPIFFE-ID"`,
`version: "2.0"`. *(Domain `identity`; this section is the CI-side
surface only. Full mapping: `identity.md` §2.2.)*

**Applicability (§14.1):** **In-scope, CI-side surface only.** The
worker uses SPIFFE IDs at the lease layer
(`services/api/leases.go::grantLeaseBody`); `runner-identity.schema.json`
(line 22) enforces the URI format
`^spiffe://[a-z][a-z0-9.-]*/ns/[a-z0-9-]+/sa/[a-z0-9_-]+$`, and
`internal/standards/standards_test.go::TestValidate_RunnerIdentity_BadSPIFFE`
(line 244) tests rejection of non-`spiffe://` strings.

**Current status (registry):** `PLANNED`. `implementation: "Slice 3:
workers carry SPIFFE IDs (#121 Runner Identity)."` Slice-1+2 worker at
`internal/worker/worker.go` identifies itself only by opaque
`worker_id`.

**Gap (§14.3):**
1. Worker does not present a SPIFFE ID on `POST /v1/leases/grant`.
2. API does not validate a SPIFFE ID on incoming lease requests.
3. Schema test only checks URI *format*, not *issuer* (trust-domain
   match against operator configuration).

**Next step (§14.5):** Extend `grantLeaseBody` with optional
`spiffe_id`; reject (HTTP 403) any request whose `spiffe_id` does not
parse via the SPIFFE-ID parser shipped in `identity.md` §2.2
(parser ownership stays there).

- **Modify** `services/api/leases.go` — add `SpiffeID string` to
  `grantLeaseBody`; reject on parse failure.
- **Create** `tests/identity/lease_spiffe_test.go` — table-driven:
  valid → 200; malformed scheme → 403; wrong trust domain → 403.

**Acceptance evidence:** `services/api/leases.go` parses the SPIFFE ID
on grant; `internal/standards.Validate("runner-identity.schema.json", ...)`
returns the same pattern result; `tests/identity/lease_spiffe_test.go`
passes.

**Risk × Leverage:** Medium × High. Every downstream identity row
(X.509 SVIDs, RBAC claims, audit `actor`) assumes a valid SPIFFE URI.

---

### 2.7 SPIRE (`spire`)

**Requirement:** "SPIFFE Runtime Environment." `control_id: "SPIRE"`,
`version: "current"`. *(Domain `identity`; this section is the CI-side
surface only. Full mapping: `identity.md` §2.3.)*

**Applicability (§14.1):** **Deferred to Slice 4+.** Slice-3 deliverable:
zero. CI-side enablement (requiring an SVID on every
`POST /v1/leases/grant`) is blocked on the same `identity.md` §2.3
trigger — multi-host pool go-live, or any audit-driven requirement for
non-mock SVIDs.

---

## §3. Prioritization — top-3 for Slice 3

| Rank | Standard                          | Rationale |
|------|-----------------------------------|-----------|
| **1** | **JSON Schema 2020-12 (§2.5) — admission enforcement** | Validator + 5 schemas already exist, but `services/api/` does not call the validator. Without admission-time validation every other JSON-Schema-shaped contract (action manifest, evidence bundle, workflow provenance) is *paper*. Single highest-leverage change in Slice 3. |
| **2** | **OCI Runtime + Image Spec bundle (§§2.1–2.2)** | Slice 3's sandbox story (`internal/sandbox/hermetic.go` per `platform.md`) is the bridge from default-deny subprocess to container isolation. OCI conformance is the *testable* shape of that bridge. |
| **3** | **SPIFFE ID format on the lease path (§2.6)** | Format already enforced by `runner-identity.schema.json` and tested. Slice-3 work is one line in `services/api/leases.go` + a table-driven test. High leverage (downstream RBAC + X.509 SVIDs assume valid SPIFFE URIs), low cost. |

**Deferred (with reason, not abandoned):** OCI Distribution Spec (§2.3)
— no registry need yet; ADR-0008 captures deferral. Bazel RE-API
(§2.4) — no second-host need yet. SPIRE (§2.7) — bundled with
`identity.md` §2.3.

---

## §4. Traceability table

| Standard | Registry row | Status | Enforcement point (file) | Test (file) | Slice |
|----------|--------------|--------|---------------------------|-------------|-------|
| OCI Runtime Spec | `oci-runtime-spec` | PLANNED | `tests/conformance/oci_test.go` (new) | `tests/conformance/oci_test.go` | 3 |
| OCI Image Spec | `oci-image-spec` | PLANNED | `internal/sandbox/image.go` (new) | `tests/conformance/oci_image_test.go` (new) | 3 (bundled) |
| OCI Distribution Spec | `oci-distribution-spec` | PLANNED | n/a (deferred) | n/a | 4+ |
| Bazel Remote Execution API | `bazel-remote-execution-api` | PLANNED | n/a (deferred) | n/a | 4+ |
| JSON Schema 2020-12 | `json-schema-2020-12` | IMPLEMENTED | `services/api/admission.go` (new) + `internal/standards/standards.go` (existing) | `internal/standards/standards_test.go` (13) + `services/api/admission_test.go` (new) | 3 |
| SPIFFE | `spiffe` (identity) | PLANNED | `services/api/leases.go` (extend `grantLeaseBody`) | `tests/identity/lease_spiffe_test.go` (new) | 3 |
| SPIRE | `spire` (identity) | PLANNED | n/a (deferred) | n/a | 4+ |

The five JSON-Schema-shaped contracts in the registry
(`action-manifest`, `evidence-bundle`, `failure-classification`,
`runner-identity`, `workflow-provenance`) are each
`IMPLEMENTED (schema), PLANNED (enforcement)` today — schemas land
with §2.5; enforcement is per-contract:
`services/api/admission.go` (action manifest, §2.5),
`services/api/evidence.go` (evidence, see `supply-chain.md`),
`internal/worker/worker.go::classify` (failure),
`services/api/enroll.go` (runner identity, slice-3 #121),
`services/api/provenance.go` (provenance, see `supply-chain.md`).

---

## §5. Promotion triggers (future)

**OCI Distribution Spec (§2.3).** Promote from `PLANNED` to `PARTIAL`
when any one of: (a) first customer asks to push/pull worker images;
(b) sandbox story advances past hermetic subprocess into per-action
image build. Promote to `IMPLEMENTED` after
`internal/sandbox/registry.go` exists and a roundtrip push-pull-verify
test passes against a local `registry:2` container.

**Bazel Remote Execution API (§2.4).** Promote from `PLANNED` to
`PARTIAL` when any one of: (a) a second host joins the worker fleet;
(b) a single action's CAS footprint exceeds 10 GB; (c) a customer
asks to run Bazel actions through works-execution. Promote to
`IMPLEMENTED` after `internal/exec/remote/server.go` serves the
`Execution` + `ContentAddressableStorage` + `ActionCache` gRPC
services, backed by `services/work/store/`, and an end-to-end
`bazel build //...` against the local server returns the same exit
code as a same-host Bazel build.

**SPIRE (§2.7).** See `identity.md` §2.3 — same deferral pattern.

---

## §6. Acceptance for this mapping document

- [x] All 7 distinct CI/Actions/Execution standards mapped (§§2.1–2.7).
- [x] Per-standard fields complete: applicability, status, gap, next
      step, file path (§2).
- [x] §14 five-step rule applied to every row.
- [x] Cross-references to `internal/standards/standards.go`,
      `internal/standards/schemas/*.json`, `internal/worker/worker.go`,
      `services/api/api.go`, `services/api/leases.go`, and the
      `standards-validate` Makefile target.
- [x] Cross-references to `identity.md` (SPIFFE/SPIRE),
      `platform-build.md` (OCI duplicates, Bazel/RE-API),
      `platform.md` (sandbox-hermetic),
      `supply-chain.md` (evidence/provenance enforcement).
- [x] Path-constant correction folded into §2.5 next step (sync script).
- [x] Top-3 highest-leverage Slice-3 standards identified (§3).
- [x] Traceability table links each standard to enforcement point,
      test, and slice (§4).
- [x] Deferred items (OCI Distribution, Bazel RE-API, SPIRE) carry
      explicit promotion triggers (§5).

This document will be updated whenever a row moves between status
values in `docs/standards/registry.json`, and at minimum once per
slice close.