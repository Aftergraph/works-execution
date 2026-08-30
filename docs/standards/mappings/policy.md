# Policy & Governance-as-Code — Standard Mapping

**Scope:** the five policy-domain standards declared in
[`docs/standards/registry.json`](../registry.json): **OPA**, **Rego**, **Kyverno**,
**Cedar**, **OSCAL**.

**Method (§14 implementation rule):** for every standard this document
records (1) applicability to works-execution, (2) current status and
implementation, (3) gap against system requirements, (4) traceability to
control surfaces, and (5) a concrete next step with an exact file path.
It closes with the recommended first-language standardisation decision.

**Authority hierarchy:** ADRs in `docs/adr/` > this document > `registry.json`
status field. Where this document and `registry.json` disagree, this document
governs the *per-standard* picture; the registry remains the single
machine-readable source of `status`.

**Audience:** founders, review agents, and the slice-3 implementer
who will wire the first policy engine.

---

## 0. Executive decision (read first)

> **Standardise first on Open Policy Agent + Rego (control_id `OPA` /
> `REGO`) as the single policy decision point at the control plane, with
> OPA invoked as a sidecar / library from `services/api`.**
>
> - **Why first:** OPA+Rego is the only policy stack in this list that
>   already fits the control-plane model (in-process, language-agnostic,
>   no Kubernetes required, MIT/Apache-2.0, mature Go SDK, single
>   binary). The works-execution platform is *not* a Kubernetes-native
>   product — Kyverno's value proposition does not apply.
> - **Why not Cedar first:** Cedar is excellent for AWS-side
>   authorisation but adds a vendor-flavoured authorisation model
>   (`Action / Resource / Principal` schemas) that is harder to generalise
>   across the heterogeneous decision points we have
>   (lease grant, capability admission, retention, fork policy,
>   redaction). It is the right answer for *one* of those decisions
>   later; it is not the right first language.
> - **Why not OSCAL first:** OSCAL is a *catalogue/component/profile*
>   format for compliance posture, not a decision-time language. It is
>   complementary and should be layered on **after** a decision-time
>   language exists, so we have something to certify.
> - **Why not Rego alone (without OPA):** Rego without OPA is a spec, not
>   a deployable. Pair them.

The remainder of this document justifies that decision per the §14
implementation rule and the per-standard traceability tables.

---

## 1. How to read this document

For each standard the structure is identical:

| Section | What it answers |
|---|---|
| **Applicability** | Does this standard apply to works-execution? On what surface? |
| **Current status** | What the registry says + what is actually true on the slice-1/2 main branch. |
| **Gap** | The specific decision points in the system that the standard does *not* yet cover, named concretely. |
| **Concrete next step** | One file path, one acceptance criterion, one status transition. |
| **Traceability table** | Row-per-system-requirement → row-per-standard-control → row-per-evidence-pointer. |

The "concrete next step" section is the only one that mutates state. The
others are documentation only and therefore Fast-track.

---

## 2. OPA — Open Policy Agent

**Standard IDs (registry):** `opa` / control_id `OPA`. See
`docs/standards/registry.json` lines 1465–1479.

### 2.1 Applicability

**Yes — central to slice 3+ control-plane design.** OPA is the
*decision-time* policy engine for the works-execution control plane.
The intended decision points are:

| Decision point | Hook in code (today) | What OPA will decide |
|---|---|---|
| Lease grant | `services/api/leases.go:grantLease` (slice 2) | "Is this `(work_id, node_id, worker_id)` triple allowed to bind a lease right now?" (capability admission, fork policy, tenant scope, rate limits) |
| Work creation | `services/api/api.go:worksHandler` POST | "Is the submitted Work allowed? (size, secret patterns, source allow-list, PII redaction)" |
| Work state transition | `packages/workgraph/workgraph.go` `validTransitions` | "Is this transition allowed by policy, or only by the state machine?" (slice 3 retry policy, max-attempts) |
| Retention / redaction | new `services/retention` (PLANNED) | "Can this artifact be evicted? Is this log line redacted before persistence?" |
| Fork policy | `docs/agents/worker.md` §Fork | "Is the lease being granted to a forked-from worker allowed under the trust class of the parent?" |
| Reaper policy | `services/api` lease-reaper goroutine | "Should this expired lease be auto-revoked, escalated, or quarantined?" |

None of these are enforced today. The pack's design leaves them as
*future* policy decisions in `registry.json`. This is the single largest
uncovered surface in the control plane.

### 2.2 Current status

- **Registry:** `PLANNED`, owner = `founder`, no implementation, no test,
  no evidence. (`registry.json` lines 1476–1478.)
- **Code (slice 1+2 main):** zero OPA calls. The lease-grant path in
  `services/api/leases.go` makes no policy call beyond "does an active
  lease already exist?" — that is a uniqueness constraint, not a
  policy decision.
- **Truth of record:** PLANNED is honest.

### 2.3 Gap

The §14 gap analysis against the pack's own requirements
(`docs/works-venture-starter-pack/04_SECURITY/SECURITY_BASELINE.md`,
`03_ENGINEERING/IMPLEMENTATION_PLAN.md`, `02_ARCHITECTURE/SYSTEM_ARCHITECTURE.md`):

1. **Capability admission is unstated.** The agent declaration
   (`docs/agents/worker.md` §Authority) lists what the worker can do
   ("Run `Node.Run` as `sh -c <command>`") but no code consults a
   capability manifest before granting a lease.
2. **Fork policy is unstated.** The pack's threat model item #7
   (`THREAT_MODEL.md`) requires "Fork-aware secret policies" but no
   code or config implements it.
3. **Retry / max-attempts is unstated.** RFC-0001 (slice 2) explicitly
   defers "policy says max-attempts-exceeded" to slice 3.
4. **Redaction at log-write time is unstated.** `worker.md`
   §Prohibited actions lists "no secret patterns in subprocess args"
   but no redaction is enforced at the artifact boundary.
5. **Retention is unstated.** No policy decides when an artifact is
   evicted.

OPA closes gaps 1–4 in one engine. Gap 5 (retention) is operational
and benefits from OSCAL overlap; it is **not** the first OPA job.

### 2.4 Concrete next step

**File path:** `docs/standards/mappings/policy/opa-bundle/lease_grant.rego`
(new file, directory to be created), and a new integration test at
`services/api/leases_policy_test.go`.

**Acceptance criterion:** running `go test ./services/api/...` with
the new file in place causes the test to call
`POST /v1/leases/grant` and assert that a Rego policy returning
`{"allow": false, "reason": "..."}` is honoured by the API (i.e. the
API returns 403 with the reason in the body, and no lease row is
written).

**Status transition:** PLANNED → PARTIAL on the registry row for
`opa` once the file lands and the test passes; PARTIAL → IMPLEMENTED
once §2.5 below is fully traced in `docs/standards/mappings/policy.md`.

**Velocity track:** Normal (the new test is runtime behaviour; the new
`.rego` file is data, but it ships with the runtime change that
consults it). The new directory `docs/standards/mappings/policy/opa-bundle/`
contains only data files and is Fast-trackable in isolation.

### 2.5 Traceability table

| System requirement (source) | OPA control (Rego bundle) | Enforcement point (code) | Test pointer | Evidence pointer |
|---|---|---|---|---|
| Lease grant requires capability match (§RFC-0001 §Design, slice 2) | `policy/lease_grant.rego#allow` | `services/api/leases.go:grantLease` (slice 3 inserts `policy.Eval` call) | `services/api/leases_policy_test.go` (NEW, slice 3) | `docs/standards/mappings/policy/opa-bundle/lease_grant.rego` |
| Fork policy (Threat Model item #7) | `policy/lease_grant.rego#fork_ok` | same as above | same | same bundle |
| Max-attempts (IMPLEMENTATION_PLAN §slice-3) | `policy/transition.rego#allow` | `services/work/store/store.go:UpdateState` | `services/work/store/transition_policy_test.go` (NEW) | `docs/standards/mappings/policy/opa-bundle/transition.rego` |
| Secret-pattern redaction in logs | `policy/redact.rego#patterns` | new `services/redact` middleware (slice 3) | `services/redact/redact_test.go` (NEW) | `docs/standards/mappings/policy/opa-bundle/redact.rego` |
| Tenant scope on lease (NIST 800-207, Zero-Trust row in registry) | `policy/lease_grant.rego#tenant_ok` | `services/api/leases.go:grantLease` | same as row 1 | same bundle |

**Bundle layout (slice 3 first PR):**

```
docs/standards/mappings/policy/opa-bundle/
  lease_grant.rego        # rows 1, 2, 5
  transition.rego         # row 3
  redact.rego             # row 4
  data/workers.json       # capability manifests, fed at OPA load time
  README.md               # bundle load order, version pin, rego fmt
```

The bundle is loaded by OPA either as a Go library
(`github.com/open-policy-agent/opa/pkg/rego`) or as a sidecar
(`openpolicyagent/opa` image) — the decision is deferred to the slice-3
RFC. Both are MIT/Apache-2.0 and Go-callable. The mapping
**deliberately does not** pick a deployment topology in this document;
that is a slice-3 ADR.

---

## 3. Rego

**Standard IDs (registry):** `rego` / control_id `REGO`. See
`registry.json` lines 1480–1494.

### 3.1 Applicability

**Yes — but only as the language of OPA.** Rego is the *language*
inside OPA. The registry currently treats `rego` as a "bundled with
opa" standard, which is correct in spirit but understates the
operational surface: a Rego-specific test framework (`opa test`),
formatter (`opa fmt`), and bundle format are independent moving parts.

### 3.2 Current status

- **Registry:** `PLANNED`, marked as bundled with OPA. (lines 1491–1493.)
- **Code:** none. No `.rego` files exist in the repo
  (`find /root/works-venture -name "*.rego"` returns empty).
- **CI:** no `opa fmt` or `opa test` step.

### 3.3 Gap

| Gap | Why it matters |
|---|---|
| No `opa fmt` in CI | Rego policy drift is undetectable. |
| No `opa test` examples | The first Rego file will land with no unit-test convention. |
| No `Makefile` target to vendor OPA | Slice 3 implementer has to discover the dependency. |

### 3.4 Concrete next step

**File path:** new `Makefile` target `opa-test` and new
`docs/standards/mappings/policy/opa-bundle/README.md` that pins
OPA v0.6x (latest stable at the time slice 3 starts) and
documents the `opa fmt` + `opa test` workflow.

**Acceptance criterion:** `make opa-test` runs `opa test
docs/standards/mappings/policy/opa-bundle/` and exits 0 with zero
policies. CI gate (`ci/local-runner/`) invokes the same target.

**Status transition:** PLANNED → PARTIAL on the `rego` row in the
registry once the `Makefile` target and README exist.

**Velocity track:** Fast (Makefile + docs only).

### 3.5 Traceability table

| System requirement | Rego control | Enforcement point | Test pointer | Evidence pointer |
|---|---|---|---|---|
| Rego policies exist and are formatted (slice 3) | `.rego` files in `docs/standards/mappings/policy/opa-bundle/` | `Makefile#opa-fmt` | `make opa-test` | `docs/standards/mappings/policy/opa-bundle/README.md` |
| Rego policies have unit tests (slice 3) | `*_test.rego` files in same dir | `Makefile#opa-test` | `make opa-test` | same |

---

## 4. Kyverno

**Standard IDs (registry):** `kyverno` / control_id `KYVERNO`. See
`registry.json` lines 1495–1509.

### 4.1 Applicability

**Not applicable in V1–V2; revisit only if/when a managed-Kubernetes
control plane is added to the platform.** The registry entry already
says "Bundled with opa (deferred)" — that is the right answer.

Kyverno's value proposition is **Kubernetes admission control**: it
validates, mutates, and generates Kubernetes resources at the
API-server boundary via `ValidatingAdmissionPolicy` /
`MutatingAdmissionPolicy` and the older
`ValidatingWebhookConfiguration`. Works-execution V1 runs the API as a
plain `net/http` Go process with SQLite; the only "control plane" is
the API process itself. There is no Kubernetes API server, no CRDs,
no admission webhook, and no plan to add one — the entire point of
ADR-0002 is that the *works-execution* process owns state, not
Kubernetes.

### 4.2 Current status

- **Registry:** `PLANNED`, marked deferred. (lines 1506–1508.)
- **Code:** none. No Kyverno binary, no `ClusterPolicy`, no webhook.
- **Truth of record:** PLANNED is honest but **the registry status
  should be `NOT_APPLICABLE` with a `not_applicable_reason`** until a
  slice explicitly adds K8s.

### 4.3 Gap

The only real gap is **registrar hygiene**: the entry is `PLANNED`
when it should be `NOT_APPLICABLE` with a reason pointer. A second,
deferred gap exists if the platform ever grows a hosted-control-plane
tier: then Kyverno would apply to that tier's cluster. We do not
foresee this in the 90-day plan.

### 4.4 Concrete next step

**File path:** `docs/standards/registry.json` — flip the `status`
field on the `kyverno` row from `PLANNED` to `NOT_APPLICABLE` and
add a `not_applicable_reason` field pointing to this document.

**Acceptance criterion:** after the registry edit, the entry's
`status` equals `NOT_APPLICABLE`, the reason is non-empty, and a
JSON-schema validator (`tests/schemas/`) accepts the modified row.

**Status transition:** PLANNED → NOT_APPLICABLE.

**Velocity track:** Fast (one JSON edit, schema-validated).

**Re-open trigger:** if/when the venture adds a managed-control-plane
tier that deploys works-execution into a Kubernetes cluster it
operates, revert this row to PLANNED and write a slice-3+ RFC.

### 4.5 Traceability table

| System requirement | Kyverno control | Enforcement point | Test pointer | Evidence pointer |
|---|---|---|---|---|
| None (no K8s in V1) | n/a | n/a | `tests/schemas/registry_test.go` confirms `NOT_APPLICABLE` + reason | this document §4 |

---

## 5. Cedar

**Standard IDs (registry):** `cedar` / control_id `CEDAR`. See
`registry.json` lines 1510–1524.

### 5.1 Applicability

**Applicable as a *secondary*, narrow-purpose language, *not* as the
primary policy language.** Cedar is well-suited to authorisation
decisions over `(Principal, Action, Resource)` triples with
type-checked schemas — exactly the shape of "Can this worker
principal execute this node resource right now?" if and only if that
question is *purely* about identity and action. It is **less well
suited** to the broader policy surface in §2.1, which mixes
identity decisions with operational decisions (rate limits, retry
counts, retention windows, redaction patterns).

Cedar also has weaker out-of-the-box toolchain for non-AWS runtimes:
the canonical SDKs are Rust and Java; Go bindings are community
(`github.com/cedar-policy/cedar-go`) and tracked as v0.x. That is
acceptable for a narrow authorisation hot-path, but it is a reason
*not* to put all policy through Cedar in V1.

### 5.2 Current status

- **Registry:** `PLANNED`, "Future alternative" (lines 1521–1523).
- **Code:** none. No `cedar-go` import, no `.cedar` policy file.
- **Truth of record:** PLANNED is honest.

### 5.3 Gap

There is no current gap because no Cedar decision point exists yet.
The gap is *latent*: if the platform later needs cross-AWS-account
authorisation (e.g. multi-account BYOC, AWS Marketplace
entitlements), Cedar is the right tool for that surface and should
slot in beside OPA, not replace it.

### 5.4 Concrete next step

**File path:** `docs/standards/mappings/policy/cedar-bundle/README.md`
(new) — a **decision document** (no code, no policy file) that
records *under which trigger conditions* Cedar would be added to the
control plane and which decision point(s) it would own. It is not
a "do Cedar first" decision; it is a "Cedar comes in if X" decision.

**Trigger conditions (proposed in that document):**
1. Platform launches a multi-account BYOC tier that needs
   AWS-side authorisation delegation.
2. An OPA Rego policy grows past ~200 LOC for a single decision
   point and the author wants a typed schema.
3. A customer audit demands formal authorisation proofs (Cedar has
   a formal-verification track record OPA does not).

**Acceptance criterion:** the README exists, lists the three
triggers, and is linked from the registry row's `evidence` field.

**Status transition:** PLANNED remains PLANNED. The README is
documentation, not implementation; it does **not** move the row to
PARTIAL.

**Velocity track:** Fast (docs only).

### 5.5 Traceability table

| System requirement | Cedar control | Enforcement point | Test pointer | Evidence pointer |
|---|---|---|---|---|
| Deferred until a trigger fires (this doc) | n/a in V1 | n/a | n/a | `docs/standards/mappings/policy/cedar-bundle/README.md` (NEW, this slice) |

---

## 6. OSCAL

**Standard IDs (registry):** `oscal` / control_id `OSCAL`. See
`registry.json` lines 1525–1539.

### 6.1 Applicability

**Applicable, but only as a *posture* / *catalogue* layer on top of
OPA+Rego.** OSCAL is a set of JSON/YAML schemas
(`catalog`, `profile`, `component-definition`, `system-security-plan`,
`assessment-plan`, `assessment-results`, `plan-of-action-and-milestones`)
for expressing *which controls apply*, *how the system implements
them*, and *what an assessor measured*. It is not a decision-time
language — at runtime, OSCAL data is consumed by tools (e.g. OSCAL
CLI, compliance-trestle, fedramp automation) and translated into
either Rego, Cedar, or bespoke checks.

The works-execution platform has a rich set of declared controls
already (this `registry.json` plus the per-domain mapping docs
`sdd.md`, `quality.md`, `security.md`, `policy.md` are *de facto* an
OSCAL `profile`). The gap is **not** authoring control statements;
it is **emitting them in OSCAL JSON** so external assessors and
GRC tooling can consume them.

### 6.2 Current status

- **Registry:** `PLANNED`, "Future: OSCAL catalogs for compliance
  posture" (lines 1536–1538).
- **Code:** none. The `docs/standards/registry.json` is the closest
  existing artefact but is not in OSCAL shape.
- **Truth of record:** PLANNED is honest.

### 6.3 Gap

1. **No OSCAL profile in the repo.** An assessor asking "show me
   which NIST 800-53 controls you claim to implement" cannot be
   answered from the current registry.
2. **No OSCAL component-definition for works-execution itself.**
   The platform-as-a-component has no formal `component-definition`
   that maps its code/config artefacts to control implementations.
3. **No `make oscal-validate` target.** The OSCAL CLI
   (`compliance-trestle`) is the standard validator; we do not
   invoke it.
4. **No assessment-results emission.** When the local CI runs
   (`ci/local-runner/run-local-ci.sh`), the pass/fail is not
   written into an `assessment-results` OSCAL document.

Gaps 1 and 2 are *authoring*; gaps 3 and 4 are *automation*.
The highest-leverage first step is **gap 3** (validator wiring) so
gap 1 can land without an external human reviewer.

### 6.4 Concrete next step

**File path:**
- new `docs/standards/mappings/policy/oscal/profile.json` (an OSCAL
  `profile` document that imports the NIST 800-53 catalog and
  selects the controls already claimed in `docs/standards/mappings/security.md`
  and `sdd.md`),
- new `Makefile` target `oscal-validate` that runs
  `trestle validate` against `profile.json`,
- new `tests/oscal/validate_test.go` that invokes the same target
  in CI.

**Acceptance criterion:** `make oscal-validate` exits 0 against the
new `profile.json`; `go test ./tests/oscal/...` is green; the
profile's `imports` include the same control IDs the security and
SSD mappings already claim.

**Status transition:** PLANNED → PARTIAL on the `oscal` row once
the profile.json + Makefile target + test all land.

**Velocity track:** Fast (data file + Makefile + Go test, no
runtime behaviour change). The component-definition (gap 2) and
assessment-results emission (gap 4) are separate PRs and likely
Normal-track.

### 6.5 Traceability table

| System requirement | OSCAL control | Enforcement point | Test pointer | Evidence pointer |
|---|---|---|---|---|
| External assessors can consume our claimed controls (gap 1) | `profile.json#imports` | `Makefile#oscal-validate` | `tests/oscal/validate_test.go` (NEW) | `docs/standards/mappings/policy/oscal/profile.json` (NEW) |
| NIST 800-53 mapping traceability (security.md claim) | same profile, same catalog | same | same | `docs/standards/mappings/security.md` is the input |
| SSDF / PO.5 traceability (sdd.md claim) | same profile, NIST 800-53 control family PS/PW/PO/RV | same | same | `docs/standards/mappings/sdd.md` is the input |
| Component-definition for works-execution itself (gap 2) | `component-definition.json` (future) | `Makefile#oscal-validate` (extended) | `tests/oscal/component_test.go` (future) | this doc §6, gap 2 |
| CI pass/fail → assessment-results (gap 4) | `assessment-results.json` (future) | `ci/local-runner/run-local-ci.sh` (extended) | `tests/oscal/assessment_test.go` (future) | this doc §6, gap 4 |

---

## 7. Cross-standard summary table

| Standard | Control IDs | Registry status | Recommended first action | First PR's file path | Track |
|---|---|---|---|---|---|
| OPA | OPA, REGO | PLANNED / PLANNED | Author lease-grant Rego bundle + Go integration | `docs/standards/mappings/policy/opa-bundle/lease_grant.rego` (NEW) + `services/api/leases_policy_test.go` (NEW) | Normal |
| Rego | REGO | PLANNED | Add `Makefile#opa-test` + bundle README | `Makefile`, `docs/standards/mappings/policy/opa-bundle/README.md` | Fast |
| Kyverno | KYVERNO | PLANNED (should be `NOT_APPLICABLE`) | Flip status to `NOT_APPLICABLE` with reason | `docs/standards/registry.json` | Fast |
| Cedar | CEDAR | PLANNED | Write "Cedar comes in if X" decision doc | `docs/standards/mappings/policy/cedar-bundle/README.md` (NEW) | Fast |
| OSCAL | OSCAL | PLANNED | Author NIST 800-53 profile + validator | `docs/standards/mappings/policy/oscal/profile.json` (NEW), `Makefile#oscal-validate`, `tests/oscal/validate_test.go` (NEW) | Fast |

---

## 8. Decision record (one-paragraph)

Works-execution will **standardise first on Open Policy Agent +
Rego** as the *decision-time* policy language, **defer Kyverno
indefinitely** (revisiting only if/when the platform adds a managed
K8s control-plane tier), **defer Cedar** behind a written trigger
list (multi-account BYOC; OPA policy > ~200 LOC; customer-mandated
formal authorisation proofs), and **layer OSCAL on top of OPA+Rego**
to expose compliance posture. This sequencing matches the pack's
order-of-operations: ship a decision engine, ship a posture schema,
defer vendor-specific authorisation languages until a concrete
trigger exists.

---

## 9. Change log

| Date | Author | Change |
|---|---|---|
| 2026-08-31 | Hermes Agent (policy-mapping slice) | Initial document. Per §14 implementation rule. Five standards, five traceability tables, one decision. |
