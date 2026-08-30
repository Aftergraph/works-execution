# Secure Software Development (SSD) Domain — Per-Standard Mapping

> **Scope.** This document maps the 5 user-mandated secure software
> development standards declared in `docs/standards/registry.json`
> (domain = `ssd`) to the works-execution system. For each standard we
> record: applicability, current status (sourced from `registry.json`), gap,
> concrete next step with file path, and a traceability table back to the
> registry row, slice deliverables, and the quality-domain mapping where
> they overlap.
>
> **Method.** The §14 implementation rule from the user-mandated standards
> charter is applied uniformly:
> 1. determine applicability,
> 2. map to system requirements,
> 3. identify gaps,
> 4. prioritize by risk and leverage,
> 5. recommend the highest-value actionable gap with a concrete file path.
>
> **Authoritative sources for status.**
> `docs/standards/registry.json`, `docs/rfcs/RFC-0001-slice-2-leases-and-recovery.md`
> (threat model), `services/api/api.go` (HTTP API), `services/work/store/`
> (SQLite + leases), `internal/worker/worker.go` (subprocess executor),
> `docs/works-venture-starter-pack/04_SECURITY/` (THREAT_MODEL,
> SECURITY_BASELINE, SECRETS_AND_IDENTITY), and
> `docs/standards/mappings/quality.md` (overlap on 25010 security +
> 31000 risk register).

---

## Summary table

| # | Standard | Status | Risk/Leverage | Top next step |
|---|---|---|---|---|
| 1 | NIST SP 800-218 SSDF v1.1 | PARTIAL | High leverage (SSD framework) | Add `tests/ssd/` + PR template SSD checklist |
| 2 | OWASP ASVS v5.0 | PARTIAL | High leverage (verification) | Stand up `tests/security/asvs_test.go` |
| 3 | OWASP SAMM v2.0 | PARTIAL | Medium leverage (maturity) | Self-assessment scoring sheet in `docs/standards/ssd-samm.md` |
| 4 | OWASP API Top 10 (2023) | PARTIAL | High leverage (most attack surface) | Author `services/api/auth.go` rate-limit middleware |
| 5 | OpenSSF Scorecard | PLANNED | Medium leverage (OSS health) | Author `ci/local-runner/scorecard.sh` |

---

## 1. NIST SP 800-218 SSDF v1.1 — Secure Software Development Framework

- **Standard.** `nist-sp-800-218-ssdf` — PO.1-5, PS.1-3, PW.1-7, RV.1-3
  practice groups
- **Registry row.** `standard_id == "nist-sp-800-218-ssdf"`,
  `control_id == "SSDF-PRACTICES"`, `status == "PARTIAL"`,
  `evidence == "docs/standards/mappings/ssd.md"`.
- **Applicability.** **In scope.** SSDF is the parent SSD framework; every
  practice group maps to something already in flight on works-execution
  (threat modeling = `RFC-0001`, code review = PR template, testing =
  `e2e/` + chaos, vulnerability management = pinned `go.mod`).
- **Current status.** PARTIAL. Registry: "Practices covered: threat modeling
  (RFC-0001), code review (PR template), testing (e2e + chaos), vulnerability
  management (deps in go.mod)."
- **Gap.** No `tests/ssd/` package exists; no per-practice evidence
  checklist; PR template is implicit (no `.github/PULL_REQUEST_TEMPLATE.md`
  in this repo).
- **Concrete next step.** Stand up `tests/ssd/ssdf_practices_test.go`
  (new) that asserts each SSDF practice group has at least one
  executable check or a documented evidence pointer, and add
  `.github/PULL_REQUEST_TEMPLATE.md` (new) with a "SSD checklist" section
  (PW.4 review, PW.5 testing, PW.7 vulnerability scan). File path:
  `tests/ssd/ssdf_practices_test.go` (new) and
  `.github/PULL_REQUEST_TEMPLATE.md` (new).
- **Risk / leverage.** High leverage — SSDF is the parent framework; one
  test package + PR template lifts visibility across every other SSD
  mapping.

### Traceability — SSDF

| Practice group | System element | File | Owner | Status |
|---|---|---|---|---|
| PO.1-5 (Organizational) | Slice governance | `docs/rfcs/RFC-0001-slice-2-leases-and-recovery.md`, `docs/kanban/board.json` | founder | PARTIAL |
| PS.1-3 (Protect) | Secrets baseline | `docs/works-venture-starter-pack/04_SECURITY/SECRETS_AND_IDENTITY.md` | founder | PARTIAL |
| PW.1 (Design) | Threat model | `docs/works-venture-starter-pack/04_SECURITY/THREAT_MODEL.md`, `docs/rfcs/RFC-0001` | founder | PARTIAL |
| PW.2-3 (Implementation) | Lint, types, code review | `Makefile` (`vet`), PR template (new) | founder | PARTIAL |
| PW.4-6 (Test) | Unit / e2e / chaos | `e2e/e2e_test.go`, `e2e/chaos_test.go` | founder | PARTIAL |
| PW.7 (Vulnerability mgmt) | Dep tracking | `go.mod`, `go.sum` | founder | PARTIAL |
| RV.1-3 (Vulnerability response) | Runbook (cross-link) | `docs/operations/INCIDENT_RESPONSE.md` (new, see quality §4) | founder | PLANNED |
| SSDF evidence test | New | `tests/ssd/ssdf_practices_test.go` (new) | founder | PLANNED |

---

## 2. OWASP ASVS v5.0 — Application Security Verification Standard

- **Standard.** `owasp-asvs` — L1/L2/L3 security verification
- **Registry row.** `standard_id == "owasp-asvs"`, `control_id == "ASVS"`,
  `status == "PARTIAL"`, `test == "tests/security/asvs_test.go"`.
- **Applicability.** **In scope.** works-execution is a JSON/HTTP API with
  SQL store + subprocess executor — exactly the surface ASVS covers.
  Registry: "Level 1 verification in tests/security/."
- **Current status.** PARTIAL. The `test` field points at
  `tests/security/asvs_test.go`, but no `tests/security/` directory exists
  yet — i.e., the test name is asserted by the registry before the test is
  written.
- **Gap.** No `tests/security/` package; no ASVS chapter mapping;
  no L1 control evidence.
- **Concrete next step.** Create `tests/security/asvs_test.go` (new) with
  a table-driven test enumerating ASVS v5.0 L1 chapters
  (V1 Architecture, V2 Authentication, V3 Session Management, V4 Access
  Control, V5 Validation, V6 Cryptography, V7 Error Handling, V8 Data
  Protection, V9 Communications, V10 Malicious Code, V11 Business Logic,
  V12 Files and Resources, V13 API and Web Service, V14 Configuration)
  with the works-execution control that satisfies each. Add a sibling
  `docs/standards/mappings/asvs-chapter-coverage.md` (new) recording
  per-chapter evidence pointers. File path: `tests/security/asvs_test.go`
  (new) and `docs/standards/mappings/asvs-chapter-coverage.md` (new).
- **Risk / leverage.** High leverage — ASVS is the most actionable SSD
  standard for V1; filling `tests/security/` makes multiple other gaps
  (auth, validation, BOLA, rate limit) testable.

### Traceability — ASVS v5.0

| ASVS chapter | System element | File | Owner | Status |
|---|---|---|---|---|
| V1 Architecture | Slice design + threat model | `docs/rfcs/RFC-0001` | founder | PARTIAL |
| V2 Authentication | (slice 3 OIDC/SPIFFE) | `services/api/api.go`, slice 3 plans | founder | PLANNED |
| V3 Session Management | Token-based (slice 3) | (slice 3) | founder | PLANNED |
| V4 Access Control | Work-id scoping | `services/api/api.go` (BOLA prevention) | founder | PARTIAL |
| V5 Validation | Work schema validation | `services/work/store/store.go` | founder | PARTIAL |
| V6 Cryptography | TLS, hashing | (slice 3 OIDC + secrets) | founder | PLANNED |
| V7 Error Handling | HTTP error mapping | `services/api/api.go` | founder | PARTIAL |
| V8 Data Protection | No PII by design | `services/work/store/store.go` (schema rejects PII) | founder | PARTIAL |
| V9 Communications | (slice 3 TLS) | (slice 3) | founder | PLANNED |
| V10 Malicious Code | Subprocess hermeticity | `internal/worker/worker.go`, `THREAT_MODEL.md` | founder | PARTIAL |
| V11 Business Logic | Lease reaper | `services/work/store/leases.go` | founder | PARTIAL |
| V12 Files and Resources | Workdir scoping | `internal/worker/worker.go` | founder | PARTIAL |
| V13 API and Web Service | Same as OWASP API Top 10 | `services/api/api.go` (see §4) | founder | PARTIAL |
| V14 Configuration | Build-time defaults | `cmd/works-api/`, `cmd/works-worker/` | founder | PARTIAL |
| ASVS L1 smoke | New | `tests/security/asvs_test.go` (new) | founder | PLANNED |

---

## 3. OWASP SAMM v2.0 — Software Assurance Maturity Model

- **Standard.** `owasp-samm` — 5 business functions (Governance, Design,
  Implementation, Verification, Operations)
- **Registry row.** `standard_id == "owasp-samm"`, `control_id == "SAMM"`,
  `status == "PARTIAL"`, `test == null`.
- **Applicability.** **In scope.** SAMM is a maturity model; works-execution
  is at the equivalent of level 1 across all 5 functions per the registry
  ("Self-assessment at level 1 across all 5 functions").
- **Current status.** PARTIAL. Self-assessment is asserted but not recorded.
- **Gap.** No artefact records the SAMM self-assessment; no per-function
  maturity score; no roadmap to level 2.
- **Concrete next step.** Author `docs/standards/mappings/ssd-samm.md`
  (new) with a maturity scorecard across the 5 SAMM functions × 15
  practices (Governance: Strategy & Metrics, Policy & Compliance,
  Education & Guidance; Design: Threat Assessment, Security Requirements,
  Secure Architecture; Implementation: Secure Build, Secure Deployment,
  Defect Management; Verification: Architecture Assessment, Requirements
  Testing, Security Testing; Operations: Incident Management, Environment
  Management, Operational Management). For each cell, record current
  maturity (0-3), evidence pointer, and target maturity. File path:
  `docs/standards/mappings/ssd-samm.md` (new). Cross-link from
  `docs/standards/mappings/quality.md` (quality §6 risk register, §4
  incident response).
- **Risk / leverage.** Medium leverage — SAMM is a model, not a gate;
  recording the scorecard turns the implicit "level 1" claim into a
  defensible audit artefact.

### Traceability — SAMM v2.0

| SAMM function | Practice | System element | File | Owner | Status (L0-3) |
|---|---|---|---|---|---|
| Governance | Strategy & Metrics | Slice KPIs + kanban | `docs/kanban/board.json` | founder | L1 |
| Governance | Policy & Compliance | Standards registry | `docs/standards/registry.json` | founder | L1 |
| Governance | Education & Guidance | Threat model doc | `docs/works-venture-starter-pack/04_SECURITY/THREAT_MODEL.md` | founder | L1 |
| Design | Threat Assessment | RFC-0001 | `docs/rfcs/RFC-0001-slice-2-leases-and-recovery.md` | founder | L1 |
| Design | Security Requirements | (cross-link) | `docs/standards/mappings/security.md` | founder | L1 |
| Design | Secure Architecture | Slice-based isolation | `internal/worker/worker.go` | founder | L1 |
| Implementation | Secure Build | `go vet`, reproducible build | `Makefile` | founder | L1 |
| Implementation | Secure Deployment | (slice 3 OCI) | (slice 3) | founder | L0 |
| Implementation | Defect Management | Issue tracker (TBD) | (TBD) | founder | L0 |
| Verification | Architecture Assessment | Slice RFC review | `docs/rfcs/` | founder | L1 |
| Verification | Requirements Testing | ASVS L1 | `tests/security/asvs_test.go` (new, see §2) | founder | L0 |
| Verification | Security Testing | e2e + chaos | `e2e/` | founder | L1 |
| Operations | Incident Management | Runbook (cross-link) | `docs/operations/INCIDENT_RESPONSE.md` (new, quality §4) | founder | L0 |
| Operations | Environment Management | (slice 3) | (slice 3) | founder | L0 |
| Operations | Operational Management | SLO doc | `docs/operations/SLOS_AND_SRE.md` (new, quality §4) | founder | L0 |
| Scorecard | New | `docs/standards/mappings/ssd-samm.md` (new) | founder | PLANNED |

---

## 4. OWASP API Security Top 10 (2023)

- **Standard.** `owasp-api-top10` — API1:2023 through API10:2023
- **Registry row.** `standard_id == "owasp-api-top10"`,
  `control_id == "API-TOP10"`, `status == "PARTIAL"`,
  `test == "tests/api/security_test.go"`.
- **Applicability.** **In scope (highest-attack-surface surface).**
  works-execution's only public surface is the JSON HTTP API
  (`services/api/api.go`); the registry names BOLA prevention, rate
  limiting, and input validation as the live controls.
- **Current status.** PARTIAL. Registry: "BOLA prevention (work_id
  scoping), rate limiting (PLANNED), input validation (validate Work on
  POST)."
- **Gap.** No `tests/api/security_test.go`; rate limiting is PLANNED; no
  explicit OWASP API Top 10 control list in code or docs.
- **Concrete next step.** Two changes:
  1. Create `tests/api/security_test.go` (new) with a table-driven test
     covering each OWASP API 2023 risk (API1 BOLA, API2 Broken Auth,
     API3 BOPLA, API4 Resource Consumption, API5 Function-Level Auth,
     API6 Unrestricted Access, API7 SSRF, API8 Misconfig, API9 Inventory,
     API10 Unsafe Consumption) with the works-execution control asserted
     per risk.
  2. Add a token-bucket middleware to `services/api/api.go` (new file
     `services/api/ratelimit.go`) implementing API4 (resource
     consumption) at the only public surface.
  File path: `tests/api/security_test.go` (new) and
  `services/api/ratelimit.go` (new), with `services/api/api.go` (extend
  middleware chain).
- **Risk / leverage.** High leverage — the API is the platform's only
  attack surface; one new test file + one middleware lifts the mapping
  from PARTIAL toward VERIFIED.

### Traceability — OWASP API Top 10 (2023)

| API risk | System element | File | Owner | Status |
|---|---|---|---|---|
| API1 BOLA | work_id scoping | `services/api/api.go` | founder | PARTIAL |
| API2 Broken Auth | (slice 3 OIDC) | (slice 3, see quality §6 SPIFFE) | founder | PLANNED |
| API3 BOPLA | Schema-validated POST | `services/work/store/store.go` | founder | PARTIAL |
| API4 Resource Consumption | Rate limit middleware | `services/api/ratelimit.go` (new) | founder | PLANNED |
| API5 Function-Level Auth | (slice 3 OIDC scopes) | (slice 3) | founder | PLANNED |
| API6 Unrestricted Access | work_id scoping | `services/api/api.go` | founder | PARTIAL |
| API7 SSRF | (N/A — no outbound HTTP from worker) | `internal/worker/worker.go` | founder | PARTIAL |
| API8 Misconfig | Defaults | `cmd/works-api/` | founder | PARTIAL |
| API9 Inventory | OpenAPI (PLANNED) | (slice 3, `docs/api/openapi.yaml`) | founder | PLANNED |
| API10 Unsafe Consumption | JSON schema validation | `docs/standards/schemas/` | founder | PARTIAL |
| API Top 10 smoke | New | `tests/api/security_test.go` (new) | founder | PLANNED |

---

## 5. OpenSSF Scorecard

- **Standard.** `openssf-scorecard` — OSS project security health checks
- **Registry row.** `standard_id == "openssf-scorecard"`,
  `control_id == "SCORECARD"`, `status == "PLANNED"`,
  `test == null`.
- **Applicability.** **In scope once public.** Registry: "Will run on
  works-execution itself once public." The repo is not yet public, so
  Scorecard cannot be measured today.
- **Current status.** PLANNED.
- **Gap.** No `ci/local-runner/scorecard.sh`; no Scorecard run; no
  baseline target score; the registry references
  `ci/local-runner/scorecard.sh (PLANNED)` but no `ci/local-runner/`
  directory exists yet.
- **Concrete next step.** Prepare the runner script and a local CI stub
  so that when the repo becomes public (and Scorecard can run against
  it), the pipeline is ready. File path: `ci/local-runner/scorecard.sh`
  (new) wrapping `scorecard-action`, and
  `tests/supply_chain/scorecard_test.go` (new) asserting the script's
  presence + a target score (≥ 7.0) recorded in
  `docs/standards/mappings/scorecard-target.md` (new).
- **Risk / leverage.** Medium leverage — Scorecard itself is automated,
  but the existing gaps (no CI runner dir, no PR template, no
  `SECURITY.md` extras like a code review disclosure) all surface in
  Scorecard and need to be addressed first.

### Traceability — OpenSSF Scorecard

| Scorecard check | System element | File | Owner | Status |
|---|---|---|---|---|
| Code Review | PR + review | `.github/PULL_REQUEST_TEMPLATE.md` (new, see §1) | founder | PLANNED |
| Dangerous Workflow | (none) | `.github/workflows/` (none defined) | founder | PARTIAL |
| Dependency Update Tool | Dependabot/Renovate | (TBD) | founder | PLANNED |
| License | `LICENSE` | `LICENSE` (TBD) | founder | PLANNED |
| Pinned Dependencies | Action pinning | (N/A — no GHA workflows) | founder | N/A |
| SAST | `go vet` | `Makefile` | founder | PARTIAL |
| SBOM | CycloneDX/SPDX (slice 3) | (slice 3) | founder | PLANNED |
| Security Policy | `SECURITY.md` | `SECURITY.md` | founder | PARTIAL |
| Signed Releases | (slice 3 Sigstore) | (slice 3) | founder | PLANNED |
| Token Permissions | (N/A — no GHA) | — | founder | N/A |
| Runner script | New | `ci/local-runner/scorecard.sh` (new) | founder | PLANNED |

---

## Cross-cutting risks (rolled up from §1-§5)

1. **Missing test packages claimed by the registry.** `tests/security/`,
   `tests/api/`, `tests/ssd/`, and `tests/quality/` do not exist; the
   registry points to test files there before they are written. Highest
   single fix: create the four test packages as part of this mapping's
   next-step recommendations.
2. **No CI runner directory.** `ci/local-runner/` is referenced by
   Scorecard, SBOM, and SLSA rows but does not exist. Establish the
   directory alongside the Scorecard next step.
3. **No PR template.** Both SSDF (§1) and OpenSSF Scorecard (§5)
   point at PR-template discipline. Add `.github/PULL_REQUEST_TEMPLATE.md`.
4. **Risk register at the standards layer is missing.** Cross-link from
   the quality mapping's §6 — the same gap propagates here (threat
   model has no risk treatment owner).

## Highest-value actionable gap (single recommendation)

> **Stand up `tests/security/asvs_test.go`** as the anchor test package
> for the SSD domain. It will (a) make the registry's claimed
> `tests/security/asvs_test.go` test name real, (b) drive the
> `services/api/ratelimit.go` middleware (API4) and the
> `tests/api/security_test.go` test (OWASP API Top 10), and (c) give
> SSDF, SAMM, and OpenSSF Scorecard a concrete reference point for
> verification evidence. File path: `tests/security/asvs_test.go` (new).