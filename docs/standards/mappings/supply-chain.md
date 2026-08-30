# Software Supply-Chain Standards — Per-Standard Mapping

> **Purpose.** Per-standard mapping for the **9 software supply-chain standards**
> declared in [`docs/standards/registry.json`](../registry.json) where
> `domain == "supply-chain"`. For each standard we record: applicability, current
> status (sourced verbatim from `registry.json`), gap, the highest-value
> concrete next step with an explicit file path, and a traceability table back
> to the registry row plus the slice deliverable that owns the work.
>
> **Method.** The §14 implementation rule from the user-mandated standards
> charter, applied uniformly:
> 1. determine applicability,
> 2. map to system requirements,
> 3. identify gaps,
> 4. prioritize by risk and leverage,
> 5. recommend the highest-value actionable gap with a concrete file path.
>
> **Authoritative sources for status.**
> `docs/standards/registry.json` (machine-readable, the only source of truth
> for `status`), the existing schema files in `docs/standards/schemas/`
> (notably `workflow-provenance.schema.json` and `evidence-bundle.schema.json`
> which already encode SLSA v1.0 + in-toto v0.1 envelope shapes), the
> starter-pack threat model (`docs/works-venture-starter-pack/04_SECURITY/THREAT_MODEL.md`
> item #8 "Supply-chain compromise of worker binary") and evidence model
> (`02_ARCHITECTURE/EVIDENCE_MODEL.md`).
>
> **Ground truth for current state.** Slice 1 (commit `d3db1d1`) shipped the
> Work primitive, SQLite store, HTTP API (`cmd/works-api`), CLI (`cmd/works`),
> and polling subprocess worker (`internal/worker/worker.go`). Slice 2
> (commit `dab84f2`) added lease-based scheduling, worker-loss recovery
> (`services/work/store/leases.go` + `services/api/leases.go`), and log
> streaming. **No supply-chain standard has implementation yet** — every row
> in the registry carries `status: PLANNED`. The schema files for provenance
> are designed but no `services/attestation/` package exists yet; that is the
> slice-3 surface the registry points at.

---

## Table of contents

| § | Standard | Status | Risk / Leverage | Top next step |
|---|----------|--------|-----------------|---------------|
| 1 | SLSA v1.2 — Supply-chain Levels for Software Artifacts | PLANNED | **High** (umbrella for §§4, 6, 7, 9) | Stand up `ci/local-runner/slsa.sh` + `services/attestation/` |
| 2 | in-toto v1.0 — Link/Step attestation | PLANNED | High (envelope for §§1, 3, 9) | Wire `services/attestation/link.go` + `intoto_test.go` |
| 3 | in-toto Attestation Framework (predicates v0.1+) | PLANNED | High (predicate vocabulary) | Land `workflow-provenance` + `runner-identity` predicate emitters |
| 4 | SPDX 3.0 — Software Package Data Exchange | PLANNED | Medium-high (EU CRA Art. 13) | Stand up `ci/local-runner/sbom.sh` emitting SPDX 3.0 |
| 5 | ISO/IEC 5962:2021 — SPDX standardization | PLANNED | Low (bundled with §4) | Re-use SPDX 3.0 generator; add ISO reference label |
| 6 | CycloneDX 1.6 — Bill of Materials | PLANNED | Medium (dual-format optionality) | Add `sbom_cyclonedx.json` emission alongside §4 |
| 7 | Sigstore (cosign + Rekor + Fulcio) | PLANNED | **High** (integrity + transparency) | Sign attestation bundles with cosign in `ci/local-runner/sign.sh` |
| 8 | TUF — The Update Framework | PLANNED | Medium (slice 4+ worker update) | Author `docs/supply-chain/TUF_DESIGN.md` for slice 4 |
| 9 | SLSA Provenance Attestations v1.2 | PLANNED | High (build-time integrity) | Land `services/attestation/provenance.go` per `workflow-provenance.schema.json` |

A consolidated **traceability table** at the bottom maps every
`standard_id` to its section, registry line, and the slice that owns the
work.

---

## 1. SLSA v1.2 — Supply-chain Levels for Software Artifacts

**Applicability.** **In scope.** works-execution is a control plane + worker
system that produces and consumes **artifacts** (worker binaries, lease
results, evidence bundles, container images once slice 3 lands) and accepts
**Work submissions** whose source revisions are upstream artifacts
(repositories, commit SHAs). The starter-pack threat model names supply-chain
compromise of the worker binary as threat #8
(`docs/works-venture-starter-pack/04_SECURITY/THREAT_MODEL.md:22`), so
SLSA's provenance + build-integrity guarantees directly mitigate that threat.

**System requirements it maps to.**

- **Build provenance** for every released artifact (worker binary, CLI,
  future Docker images): non-forgeable record of *who built what, with what
  inputs, on what platform*.
- **Build isolation**: builds must not have unilateral influence from the
  developer workspace. Slice-3 builds run in `ci/local-runner/`, which is
  already isolated from the developer's `cmd/works-worker/main.go` checkout.
- **Source provenance**: the Git revision being built is recorded in the
  attestation (`predicate.predicate.invocation.source.revision`).
- **Common dependency enumeration**: every build emits an SBOM (ties to §4
  and §6 below).
- **Levels L1 → L3**: L1 = signed provenance exists; L2 = signed by a
  trusted build platform; L3 = provenance is non-forgeable (keyless
  Sigstore / Rekor). The registry's `control_id = "SLSA-L3"` and
  `implementation = "Slice 3 targets SLSA L3 for releases."` make the
  target explicit.

**Current status — PLANNED.** `registry.json` line 866 carries `status:
PLANNED`, `implementation: "Slice 3 targets SLSA L3 for releases."`,
`enforcement_point: "ci/local-runner/slsa.sh."`, `test:
"tests/supply_chain/slsa_test.go"`, `evidence: "PLANNED"`. **None of the
referenced files exist yet** (`ci/local-runner/` is absent; `tests/supply_chain/`
is absent). The provenance **envelope schema is designed** —
`docs/standards/schemas/workflow-provenance.schema.json` already enumerates
`https://slsa.dev/provenance/v1` as a valid `predicateType` and validates
the SLSA v1 subject/digest shape — but no Go code emits against it.

**Gap.** Four coupled gaps:

1. **No attestation package.** `services/attestation/` does not exist; the
   only existing producer of "execution evidence" is `internal/worker/worker.go`,
   which writes an artifact file path (`LogRef`) and an `Evidence` slice
   (`packages/workgraph/workgraph.go:156`) but never signs or wraps them.
2. **No local CI runner.** `ci/local-runner/run-local-ci.sh` is referenced
   in `AGENTS.md` as the authoritative CI surface but only contains
   placeholder paths in this slice; `ci/local-runner/slsa.sh` does not
   exist.
3. **No supply-chain test package.** `tests/supply_chain/` is absent; the
   registry points to `tests/supply_chain/slsa_test.go` (line 874).
4. **Registry `enforcement_point` is missing the trailing slash inside the
   command.** Line 873 ends with `"ci/local-runner/slsa.sh."` — a typo
   where a sentence-period sits inside the file path. This will need to be
   corrected when the file is created.

**Highest-leverage next step.** Land the **SLSA scaffold** in this order so
each step is independently testable:

1. Create `services/attestation/` Go package with `services/attestation/doc.go`
   declaring the package's purpose ("Emit and verify SLSA v1.2 and in-toto
   v0.1 attestations for works-execution artifacts"), then `services/attestation/provenance.go`
   implementing `BuildProvenance(workID, artifactDigest, invocation []In) (*Provenance, error)`
   that emits a value validating against
   `docs/standards/schemas/workflow-provenance.schema.json` with
   `predicateType = "https://slsa.dev/provenance/v1"`.
2. Create `ci/local-runner/slsa.sh` that runs `go build` for all three
   binaries (`bin/works`, `bin/works-api`, `bin/works-worker`), hashes each
   output, calls `services/attestation` to produce a SLSA v1.2 envelope,
   and writes it to `dist/slsa/<binary>-<sha256>.intoto.jsonl`.
3. Create `tests/supply_chain/slsa_test.go` asserting: (a) provenance
   validates against `workflow-provenance.schema.json`, (b) subject digest
   matches the on-disk binary, (c) `predicate.runDetails.builder` is
   `"ci/local-runner/slsa.sh"`.

- **File to create:** `services/attestation/provenance.go` (new package,
  first attestation emitter).
- **File to create:** `ci/local-runner/slsa.sh` (new, executable).
- **File to create:** `tests/supply_chain/slsa_test.go` (new test file).
- **Linked registry rows:** §1, §2, §3, §7, §9 — all five advance from a
  single attestation scaffold.
- **Risk / leverage.** **High leverage.** A working `services/attestation/`
  package is the keystone for SLSA, in-toto, Sigstore, and SLSA Provenance
  simultaneously. Defer it and the slice-3 plan in `registry.json` cannot
  start.

---

## 2. in-toto v1.0 — Link/Step attestation layout

**Applicability.** **In scope.** in-toto is the **envelope** format that
SLSA v1.2 re-uses (`https://in-toto.io/attestation/v0.1` is the second valid
`predicateType` in `workflow-provenance.schema.json`), so any SLSA
provenance emitted by works-execution is simultaneously in-toto-shaped.
Works are multi-step DAGs (`packages/workgraph/workgraph.go:244 Graph.Nodes`),
and in-toto's Step/By-product model maps 1:1 to a node-attempt-artifact
chain.

**System requirements it maps to.**

- **Statement envelope** (`_type: https://in-toto.io/Statement/v0.1`) with
  `predicateType`, `subject[]`, `predicate`.
- **Link metadata** for each step (who ran it, materials consumed,
  products produced, environment).
- **Layout**: a root `Layout` describing the DAG (`Work.Graph`) and per-step
  Links recording which worker, which lease, which revision produced which
  artifact.

**Current status — PLANNED.** `registry.json` line 881: `status: PLANNED`,
`implementation: "Slice 3: emit in-toto attestations per work."`,
`enforcement_point: "services/attestation/."`, `test:
"tests/supply_chain/intoto_test.go"`. As with §1, **no files exist**.

**Gap.** Same package-absence gap as §1 — `services/attestation/` does
not exist, so there is no Statement emitter and no Link emitter. The
schema in `docs/standards/schemas/workflow-provenance.schema.json` is
already correct for in-toto's envelope shape (subject/predicate/predicateType
triad), so the design is half-done; the code is zero.

**Highest-leverage next step.** Re-use the §1 scaffold: extend
`services/attestation/` with `link.go` that emits
`https://in-toto.io/attestation/v0.1` Statements with predicate `Link`
(materials = lease inputs, products = artifact hashes, environment =
`internal/worker/worker.go` build env). Add
`tests/supply_chain/intoto_test.go` that round-trips a real work through
the lease-complete flow and asserts every node-attempt emits a Link
Statement.

- **File to create:** `services/attestation/link.go` (Link emitter).
- **File to create:** `tests/supply_chain/intoto_test.go`.
- **Linked registry rows:** §1, §3, §9 — same attestation scaffold; the
  Link emitter is the per-step complement to §1's per-work provenance.
- **Risk / leverage.** **High.** Without Links, every other attestation
  becomes a disconnected statement; Links are the only artifact that
  *binds* a worker to a binary it produced, which is the literal definition
  of supply-chain integrity.

---

## 3. in-toto Attestation Framework (predicates v0.1+)

**Applicability.** **In scope.** The predicate vocabulary is what makes
attestations **machine-verifiable**: SLSA Provenance (`slsa.dev/provenance/v1`),
Link (`in-toto.io/attestation/link/v0.1`), Runner Identity
(`in-toto.io/attestation/runner-identity/v0.1`), Material Provenance, etc.
works-execution's evidence model
(`docs/works-venture-starter-pack/02_ARCHITECTURE/EVIDENCE_MODEL.md`) is
itself a predicate family — `Evidence.Type` (`build | test | typecheck |
lint | security_scan | artifact | policy`) at
`packages/workgraph/workgraph.go:161` is a small set of predicate types
that should map 1:1 onto in-toto predicate URIs.

**System requirements it maps to.**

- **Predicate registry**: a documented list of which predicate types
  works-execution emits and consumes.
- **Predicate conformance**: each emitted predicate validates against its
  published JSON Schema.
- **Predicate composition**: a workflow-provenance Statement
  (`workflow-provenance.schema.json`) can reference downstream evidence
  via additional `predicateType`s on the same subject.

**Current status — PLANNED.** `registry.json` line 896: `status: PLANNED`,
`implementation: "Bundled with in-toto-v1.0."`, all
`enforcement_point / test / evidence = null`. Treated by the registry as
a sub-row of §2.

**Gap.** Predicate **enumeration** is implicit but not documented.
`docs/standards/schemas/` already defines two predicate-typed schemas:

- `workflow-provenance.schema.json` — for SLSA v1 / in-toto v0.1 envelopes
  (used by §1, §2, §9).
- `runner-identity.schema.json` — for runner enrollment (worker →
  trust-class advertisement).

Neither is wired to code; `internal/worker/worker.go` does not emit a
runner-identity record, it only logs `workerID` strings.

**Highest-leverage next step.** Document and emit the **runner-identity
predicate** first, because every other predicate needs to bind to a
runner. Extend `internal/worker/worker.go`'s `Client` with a `Register()`
call that POSTs a runner-identity Statement to a new
`POST /v1/workers/register` endpoint, validating the payload against
`runner-identity.schema.json`. This single step unblocks §1, §2, §9 and
seeds the predicate registry.

- **File to create:** `services/api/runner_register.go` (new endpoint).
- **File to create:** `internal/worker/worker.go` extension
  `Client.Register(r runnerIdentity) error` (existing file modified).
- **File to create:** `tests/supply_chain/runner_identity_test.go`.
- **Linked registry rows:** §1, §2, §7, §9 — runner identity is what
  Sigstore (§7) signs over; SLSA Provenance (§9) includes
  `runDetails.builder` which is runner-identity-derived.
- **Risk / leverage.** **High** (same keystone reasoning as §1).

---

## 4. SPDX 3.0 — Software Package Data Exchange

**Applicability.** **In scope.** Every released artifact (worker binary,
CLI, API server, future Docker image) carries dependencies that must be
documented for **EU CRA Art. 13** (the registry lists `eu-cra` with
`enforcement_point: "SBOM generation (CycloneDX, PLANNED), vuln scanning
in CI."` at line 483). SPDX is the ISO/IEC 5962:2021 §5 canonical format
and is mandated by an increasing number of US Federal and EU procurement
contexts.

**System requirements it maps to.**

- **SBOM per release**: machine-readable, validated, attached to the
  artifact digest.
- **Package identity**: each Go module + transitive dep named with PURL,
  supplier, license.
- **Provenance linking**: SPDX 3.0 supports a `Relationship: generatedFrom`
  to a SLSA provenance envelope, so §4 + §1 must round-trip.

**Current status — PLANNED.** `registry.json` line 911: `status: PLANNED`,
`implementation: "Slice 3: emit SPDX SBOM per release."`,
`enforcement_point: "ci/local-runner/sbom.sh."`, `test:
"tests/supply_chain/spdx_test.go"`.

**Gap.** No SBOM producer; no test; no script. The Go module graph is
known (`go.mod`/`go.sum` present at the repo root) but not exported.
The full dep tree is `modernc.org/sqlite` + transitive (registry lines
48-60), which is ~50 packages in total — small enough to enumerate by
hand but the right answer is to call `go list -m -json all` and pipe to
an SPDX serializer.

**Highest-leverage next step.** Stand up the SBOM CI gate as a **pure Go**
producer (no JS toolchain) so it lives inside `make build` cleanly. Use
`go list -m -json all` for the dependency graph, emit SPDX 3.0 JSON
(construct `SpdxDocument` with `packages[]`, `relationships[]`,
`creationInfo.createdBy = "ci/local-runner/sbom.sh"`), validate with
`gojsonschema` against a vendored SPDX 3.0 schema, and write to
`dist/sbom/<binary>-<sha256>.spdx.json`.

- **File to create:** `services/sbom/spdx.go` (Go emitter).
- **File to create:** `ci/local-runner/sbom.sh` (orchestrator).
- **File to create:** `tests/supply_chain/spdx_test.go` (validation +
  relationships round-trip).
- **Linked registry rows:** §4 itself, §5 (same format), §6 (parallel
  CycloneDX emitter), and `eu-cra` in the security mapping
  (`docs/standards/mappings/security.md`).
- **Risk / leverage.** **Medium-high** — driven by EU CRA exposure; not
  blocking slice 1-2 but blocking any EU customer rollout.

---

## 5. ISO/IEC 5962:2021 — SPDX standardization

**Applicability.** **In scope, bundled.** ISO/IEC 5962:2021 is the
ISO-issued standardization of SPDX 2.x; it adds nothing technically new
beyond the standard-identifier citation. works-execution emits SPDX 3.0
under §4; that same emission should additionally carry
`creationInfo.standard = "ISO/IEC 5962:2021"` so the output satisfies
both the de-facto standard (SPDX) and the de-jure one (ISO).

**System requirements it maps to.**

- **Standard identifier** in `creationInfo` referencing ISO/IEC 5962:2021.
- **Conformance to ISO/IEC 5962:2021 §5** (which is the SPDX 2.1
  specification; SPDX 3.0 supersedes but ISO/IEC 5962:2021 remains the
  cited reference).
- **License-list version** pinned to a known SPDX license list release.

**Current status — PLANNED.** `registry.json` line 926: `status: PLANNED`,
`implementation: "Bundled with spdx-3.0."`, all
`enforcement_point / test / evidence = null`.

**Gap.** None actionable that §4 does not already cover. The only
additions vs §4 are (a) a `standard` field in `creationInfo` and (b) a
pin of the SPDX license-list version in the SBOM.

**Highest-leverage next step.** When §4 lands, extend
`services/sbom/spdx.go` with two lines: set
`SpdxDocument.CreationInfo.Standard = "ISO/IEC 5962:2021"` and
`SpdxDocument.CreationInfo.LicenseListVersion = "3.20"` (pinned).
Document the bundling in `docs/standards/mappings/supply-chain.md` §5.

- **File to modify:** `services/sbom/spdx.go` (two fields added when
  §4 lands).
- **Linked registry rows:** §4 (the actual emitter).
- **Risk / leverage.** **Low** — the ISO citation is a label, not a
  new technical control. Bundled with §4 means **no separate deliverable**
  is needed.

---

## 6. CycloneDX 1.6 — Bill of Materials

**Applicability.** **In scope, optional secondary format.** CycloneDX is
the OWASP-led SBOM standard, widely supported by vulnerability scanners
(Dependency-Track, Trivy, Grype). It is **not** ISO-standardized. Its
value to works-execution is *tooling compatibility*: customers may
already have a CycloneDX ingestion pipeline, and emitting both SPDX and
CycloneDX doubles the chance the SBOM is actually consumed.

**System requirements it maps to.**

- **CycloneDX 1.6 BOM** (`bomFormat: "CycloneDX"`, `specVersion: "1.6"`,
  `serialNumber: urn:uuid:<per-build>`) for each released binary.
- **Components**: every Go module + transitive dep as a CycloneDX
  component with `purl` + `licenses`.
- **Vulnerabilities field** (optional, populated by later dep-vuln scan).

**Current status — PLANNED.** `registry.json` line 941: `status: PLANNED`,
`implementation: "Slice 3: emit CycloneDX SBOM alongside SPDX."`,
`enforcement_point: null`, `test: null`, `evidence: null`.

**Gap.** Same producer-absence gap as §4, plus a *deliberate dual-emission*
design choice: should SPDX and CycloneDX be one build pipeline or two?
The right answer is "one Go pipeline that emits both formats from the
same `go list -m -json all` snapshot", because divergence between the
two SBOMs is a worse failure mode than either one missing.

**Highest-leverage next step.** Re-use the §4 dependency graph and add a
CycloneDX emitter in the same package. Land
`services/sbom/cyclonedx.go` that takes the same
`[]go.Package` snapshot as `spdx.go` and writes
`dist/sbom/<binary>-<sha256>.cdx.json`.

- **File to create:** `services/sbom/cyclonedx.go` (new, sibling to
  `spdx.go`).
- **File to create:** `tests/supply_chain/cyclonedx_test.go`.
- **Linked registry rows:** §4 (shared `go list` snapshot),
  `eu-cra` (security mapping).
- **Risk / leverage.** **Medium** — secondary format; valuable but not
  blocking. Highest leverage is shipping it together with §4 so the
  dual-emission is verified by one CI run.

---

## 7. Sigstore — signing, transparency, verification (cosign + Rekor + Fulcio)

**Applicability.** **In scope.** Every attestation produced by §§1-3 and
9 needs a **signature** to be non-forgeable, and every SBOM from §§4, 6
needs the same. Sigstore provides **keyless** signing (Fulcio-issued
ephemeral X.509 cert bound to OIDC identity, Rekor transparency-log
entry, cosign signature in OCI registry or local bundle) which is the
SLSA L3 mechanism.

**System requirements it maps to.**

- **cosign sign-blob** on every SLSA provenance envelope and every
  in-toto Link statement.
- **Rekor log inclusion**: each signature is accompanied by a
  `logIndex` + `logID` so a verifier can prove the signature was
  *timestamped* by an append-only log.
- **Fulcio keyless identity**: the signer's OIDC subject becomes the
  `predicate.runDetails.builder` value, binding the build to the
  CI-runner identity.
- **Verification path**: `cosign verify-blob` (or equivalent) is
  invokable by any downstream consumer from the public bundle.

**Current status — PLANNED.** `registry.json` line 956: `status: PLANNED`,
`implementation: "Slice 3: sign attestation bundles with cosign."`,
`enforcement_point: null`, `test: null`, `evidence: null`.

**Gap.** No signature at all today; the slice-1 and slice-2 builds
(`make build` line 11-14) produce unsigned binaries; the slice-2 lease
completion (`services/api/leases.go:97`) accepts arbitrary
`evidence[]` blobs with no signature requirement.

**Highest-leverage next step.** Add **signed-bundle verification** to
the lease-complete endpoint first (this is the most consequential
untrusted-input surface today), then extend `ci/local-runner/` to sign
every attestation it emits. Add `services/attestation/verify.go` with
`func VerifyBundle(bundle []byte, publicKey []byte) error` and call it
from `services/api/leases.go:completeLease` on every incoming
`evidence[]` entry. Add `ci/local-runner/sign.sh` that calls
`cosign sign-blob --output-signature` on every `.intoto.jsonl` from §1
and writes `.sig` alongside.

- **File to create:** `services/attestation/verify.go` (cosign verifier
  wrapper).
- **File to modify:** `services/api/leases.go:completeLease` (reject
  unsigned evidence).
- **File to create:** `ci/local-runner/sign.sh` (sign in CI).
- **File to create:** `tests/supply_chain/sigstore_test.go`.
- **Linked registry rows:** §1, §2, §3, §9 (every attestation is signed
  by this layer).
- **Risk / leverage.** **High** — signing is the difference between
  "evidence we wrote" and "evidence we can prove we wrote". Without it,
  §1's provenance is a TODO comment.

---

## 8. TUF — The Update Framework (secure software updates)

**Applicability.** **In scope, slice 4+.** TUF protects the
**consumer side** of the supply chain: when a worker downloads a new
worker binary, TUF-signed metadata (`root.json`, `targets.json`,
`snapshot.json`, `timestamp.json`) ensures the download is the
*intended* version, even in the face of a compromised mirror or a
stale cached version. works-execution's worker
(`cmd/works-worker/main.go`) is a Go binary that will eventually be
auto-updated; that update path is the TUF surface.

**System requirements it maps to.**

- **Repository metadata**: `root.json` (signing keys, threshold),
  `targets.json` (per-target hashes + length), `snapshot.json` (which
  targets are current), `timestamp.json` (snapshot freshness).
- **Key rotation**: TUF requires multi-signature thresholds with
  rotation; works-execution can use the existing founder-owner key
  initially but must architect for a second signer.
- **Consistent snapshot**: every client query is keyed off the same
  snapshot to prevent mixed-version attacks.
- **Worker's update path**: `internal/worker/worker.go` polls the API
  on startup; the response can include a TUF metadata pointer.

**Current status — PLANNED.** `registry.json` line 971: `status: PLANNED`,
`implementation: "Slice 4+: TUF-style metadata for worker binary
distribution."`, `enforcement_point: null`, `test: null`,
`evidence: null`. **Note: TUF is deferred to slice 4+, not slice 3** —
this is the only supply-chain standard not in the slice-3 batch.

**Gap.** The worker today does not auto-update at all; it is deployed
by rebuilding `bin/works-worker` and restarting the process
(`cmd/works-worker/main.go` package docstring). There is no TUF
metadata producer, no TUF client in the worker, no key-rotation
process. None of this is blocking slice 1-3.

**Highest-leverage next step.** **Author the TUF design document
first**, before any code. TUF is easy to do badly (rolled-your-own
metadata, no key rotation, no snapshot consistency) and the design
choices (where to host metadata, who is the root signer, threshold
scheme) are not the kind that should be made under code pressure.

- **File to create:** `docs/supply-chain/TUF_DESIGN.md` (design doc:
  roles, keys, thresholds, rotation cadence, repo hosting, worker
  client library choice — recommend `theupdateframework/go-tuf`).
- **Linked registry rows:** §8 only.
- **Risk / leverage.** **Medium** — not slice-3-blocking, but a
  *known* design debt. The doc is cheap (one file) and prevents the
  wrong choice from being baked into code later.

---

## 9. SLSA Provenance Attestations v1.2

**Applicability.** **In scope, bundled with §1.** SLSA Provenance is the
specific predicate type (`https://slsa.dev/provenance/v1`) that sits
inside an in-toto Statement and describes *one build*. It is the most
machine-checked artifact in the supply-chain stack: a verifier can
demand `predicate.runDetails.builder` matches a known builder, and
`predicate.buildConfig.steps[]` matches a known recipe, with no human
intervention.

**System requirements it maps to.**

- **Predicate conformance** with the SLSA v1.0 spec (predicate schema
  `slsa.dev/provenance/v1`).
- **Subject = artifact digest**: `subject[].digest.sha256 = <bin-sha256>`.
- **Build type**: `predicate.buildType = "https://works-execution.dev/BuildType/v1"`
  (custom but allowed by SLSA v1.0; mapped to works-execution's
  `ci/local-runner/slsa.sh`).
- **Run details**: `predicate.runDetails.builder.id`,
  `predicate.runDetails.builder.version`, `predicate.runDetails.metadata.invocationID`
  = the lease ID (`workgraph.Lease.ID`) when the build is a
  works-execution-driven re-build.

**Current status — PLANNED.** `registry.json` line 986: `status:
PLANNED`, `implementation: "Bundled with slsa-1.2."`, all
`enforcement_point / test / evidence = null`.

**Gap.** The predicate **schema is designed** in
`docs/standards/schemas/workflow-provenance.schema.json` (which
explicitly enumerates `https://slsa.dev/provenance/v1` as a valid
`predicateType`), but no producer exists.

**Highest-leverage next step.** Implement `services/attestation/provenance.go`
together with §1's scaffold (same file), because the SLSA Provenance
predicate is the *specific* payload that §1 emits and §7 signs.
`workflow-provenance.schema.json` already encodes the SLSA v1 shape;
the missing piece is the producer function that fills it.

- **File to create:** `services/attestation/provenance.go` (same as
  §1; this section makes the predicate-type choice explicit).
- **File to create:** `tests/supply_chain/provenance_test.go`
  (validate that an emitted envelope validates against
  `workflow-provenance.schema.json` with `predicateType =
  "https://slsa.dev/provenance/v1"` and matches the build's
  artifact digest).
- **Linked registry rows:** §1, §2, §3, §7 (one envelope, four
  standards satisfied).
- **Risk / leverage.** **High** — same as §1; this row is the
  predicate side of the same scaffold.

---

## Cross-cutting summary — priority-ordered concrete next steps

In order of leverage (each step delivers value for multiple standards):

| # | File to create/modify | Standards it advances |
|---|------------------------|-----------------------|
| 1 | `services/attestation/` (new package: `doc.go`, `provenance.go`, `link.go`, `verify.go`) | §1 SLSA, §2 in-toto, §3 predicates, §7 Sigstore, §9 SLSA Provenance |
| 2 | `docs/standards/schemas/workflow-provenance.schema.json` — **no edit needed** (already correct; reuse) | §1, §2, §9 |
| 3 | `ci/local-runner/slsa.sh` + `ci/local-runner/sign.sh` + `ci/local-runner/sbom.sh` (three scripts) | §1, §4, §6, §7 |
| 4 | `services/sbom/` (new package: `spdx.go`, `cyclonedx.go`) | §4 SPDX, §5 ISO-SPDX, §6 CycloneDX |
| 5 | `services/api/runner_register.go` + `internal/worker/worker.go` `Client.Register` extension | §3 predicate (runner-identity), §1, §2 |
| 6 | `services/api/leases.go:completeLease` — reject unsigned `evidence[]` | §7 Sigstore |
| 7 | `tests/supply_chain/` (new test package; one `*_test.go` per §) | §1, §2, §3, §4, §6, §7, §9 |
| 8 | `docs/supply-chain/TUF_DESIGN.md` (design doc only, slice 4+) | §8 TUF |
| 9 | `ci/local-runner/run-local-ci.sh` — add a `supply-chain` gate target | §1, §4, §6, §7, §9 |

**Single highest-value actionable gap** (one recommendation):

> **Stand up `services/attestation/` as a Go package with three files
> (`doc.go`, `provenance.go`, `link.go`) and a matching
> `tests/supply_chain/` package, all in one PR.** This single PR advances
> §§1, 2, 3, 7, 9 from PLANNED to IMPLEMENTED because they all share the
> same envelope and producer; Sigstore signing (§7) and SLSA-Provenance
> predicate (§9) are added in a follow-up PR, but the keystone is the
> package scaffold.

---

## Traceability table (registry `standard_id` → this document)

Every supply-chain row in `docs/standards/registry.json` maps to a section
above. `registry.json` lines are 1-indexed from the file as of
`2026-08-31`.

| standard_id (registry)  | registry §line | This doc § | registry status |
|-------------------------|---------------:|-----------:|-----------------|
| `slsa-1.2`              | 866            | §1         | PLANNED |
| `in-toto-v1.0`          | 881            | §2         | PLANNED |
| `in-toto-attestation`   | 896            | §3         | PLANNED |
| `spdx-3.0`              | 911            | §4         | PLANNED |
| `iso-iec-5962-2021`     | 926            | §5         | PLANNED (bundled with §4) |
| `cyclonedx`             | 941            | §6         | PLANNED |
| `sigstore`              | 956            | §7         | PLANNED |
| `tuf`                   | 971            | §8         | PLANNED (slice 4+) |
| `slsa-provenance`       | 986            | §9         | PLANNED (bundled with §1) |

### Slice-3 ownership map (which slice delivers each §)

| § | Standard | Slice that owns delivery | Evidence target |
|---|----------|--------------------------|-----------------|
| §1 | SLSA 1.2 | Slice 3 | `tests/supply_chain/slsa_test.go` |
| §2 | in-toto v1.0 | Slice 3 | `tests/supply_chain/intoto_test.go` |
| §3 | in-toto predicates | Slice 3 (bundled) | `tests/supply_chain/runner_identity_test.go` |
| §4 | SPDX 3.0 | Slice 3 | `tests/supply_chain/spdx_test.go` |
| §5 | ISO/IEC 5962:2021 | Slice 3 (bundled with §4) | (no separate test) |
| §6 | CycloneDX 1.6 | Slice 3 | `tests/supply_chain/cyclonedx_test.go` |
| §7 | Sigstore | Slice 3 | `tests/supply_chain/sigstore_test.go` |
| §8 | TUF | **Slice 4+** | `docs/supply-chain/TUF_DESIGN.md` (design only, no test yet) |
| §9 | SLSA Provenance | Slice 3 (bundled with §1) | `tests/supply_chain/provenance_test.go` |

### Cross-references to other mapping documents

| Other mapping | Standards that touch the supply-chain domain | Link |
|---------------|----------------------------------------------|------|
| `security.md` | EU CRA (`eu-cra`, line 476) requires SBOM (§4, §6) and vuln scanning in CI (ties to §7); EU NIS2 (`eu-nis2`, line 461) requires incident response that references compromised binary provenance (ties to §1, §9). | `docs/standards/mappings/security.md` |
| `quality.md` | ISO/IEC 25010 §6.1 maintainability depends on reproducibility, which §1 + §9 partially deliver; ISO 31000 risk register (§6) should list supply-chain compromise of worker binary as a treated risk. | `docs/standards/mappings/quality.md` |
| `ssd.md` | Secure development practices include dependency pinning + SBOM (ties to §4, §6); SSD-30 mandates signed releases (ties to §7). | `docs/standards/mappings/ssd.md` |

---

## Known follow-ups for `registry.json` itself

These are *not* supply-chain deliverables but were spotted while
authoring this mapping and should be filed as separate cleanup items:

1. **Line 873** — `enforcement_point` for `slsa-1.2` reads
   `"ci/local-runner/slsa.sh."` with the trailing period *inside* the
   quoted file path. Likely a typo (intended `"ci/local-runner/slsa.sh"`)
   but, if literal, would create a path that does not exist. Confirm
   with the registry owner and remove the stray period.
2. **Line 888** — `enforcement_point` for `in-toto-v1.0` reads
   `"services/attestation/."` — same trailing-period issue inside the
   quoted path; the directory should be `services/attestation/` without
   the trailing period.
3. **Lines 903-907, 934-937, 950-953, 966-968, 980-983, 994-997** — the
   bundled rows (`in-toto-attestation`, `iso-iec-5962-2021`, `cyclonedx`,
   `sigstore`, `tuf`, `slsa-provenance`) all have
   `enforcement_point / test / evidence = null`. This is fine when the
   row genuinely bundles, but for non-bundled rows (§6, §7) the `null`
   test pointer hides the fact that `tests/supply_chain/<x>_test.go`
   does not exist. Recommend populating `test` for §6 and §7 even if the
   file does not exist yet, so the gap is explicit rather than implicit.