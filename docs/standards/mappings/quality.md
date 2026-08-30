# Quality Domain — Per-Standard Mapping

> **Scope.** This document maps the 9 user-mandated software-engineering / quality
> standards declared in `docs/standards/registry.json` (domain = `quality`) to the
> works-execution system. For each standard we record: applicability, current
> status (sourced from `registry.json`), gap, concrete next step with file
> path, and a traceability table back to the registry row and slice deliverables.
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
> `docs/standards/registry.json` (machine-readable), `Makefile` (gate targets),
> `e2e/` (e2e + chaos suites), `internal/worker/` (slice-2 lease/recovery),
> `docs/works-venture-starter-pack/05_OPERATIONS/SLOS_AND_SRE.md` (SLOs),
> `docs/works-venture-starter-pack/07_BUSINESS/RISK_REGISTER.md` (risk basis).

---

## Summary table

| # | Standard | Status | Risk/Leverage | Top next step |
|---|---|---|---|---|
| 1 | ISO/IEC/IEEE 12207:2026 | PARTIAL | High leverage (governance) | Add lifecycle conformance check to `Makefile` |
| 2 | ISO/IEC 25010:2023 | PARTIAL | High leverage (quality model) | Add `tests/quality/iso25010_smoke_test.go` |
| 3 | ISO/IEC/IEEE 29119 | IMPLEMENTED | Medium leverage | Promote to VERIFIED with test evidence pointer |
| 4 | ISO/IEC 20000-1 | PARTIAL | Medium leverage (SMS) | Author `docs/operations/SLOS_AND_SRE.md` from starter pack |
| 5 | ISO 22301 | PLANNED | High risk (BC) | Document BC plan in `docs/operations/BC_PLAN.md` |
| 6 | ISO 31000 | PLANNED | High leverage (risk) | Author `docs/standards/RISK_REGISTER.md` |
| 7 | ISO 9241-210 | PLANNED | Low leverage (V1) | Defer until web UI slice 6; track in kanban |
| 8 | WCAG 2.2 | PLANNED | Low leverage (V1) | Defer until web UI slice 6; track in kanban |
| 9 | WAI-ARIA 1.2 | PLANNED | Low leverage (V1) | Bundle with WCAG 2.2 in web UI slice 6 |

---

## 1. ISO/IEC/IEEE 12207:2026 — Systems and Software Engineering

- **Standard.** `iso-iec-ieee-12207-2026` — Software life cycle processes
- **Registry row.** `standards[].standard_id == "iso-iec-ieee-12207-2026"`,
  `control_id == "LIFECYCLE"`, `status == "PARTIAL"`, `evidence == "docs/standards/mappings/quality.md"`.
- **Applicability.** **In scope.** works-execution follows an iterative slice
  lifecycle (slice 1 → slice 2 → slice 3 …) with explicit RFCs, ADRs, and a
  kanban board — a direct mapping to the 12207 iterative process model.
- **Current status.** PARTIAL. Slice-based lifecycle is operating (slice 1
  d3db1d1, slice 2 dab84f2), but no formal conformance artefacts exist.
- **Gap.** No lifecycle-process evidence bundle; `Makefile` does not assert
  slice deliverables; the RFC/ADR/kanban chain is not linked from one place.
- **Concrete next step.** Extend `Makefile` with a `lifecycle` target that
  asserts (a) `docs/adr/ADR-INDEX.md` exists and references all committed ADRs,
  (b) `docs/rfcs/` contains at least one RFC whose status is `ACCEPTED`, and
  (c) the current slice has a kanban card in the `done` lane. File path:
  `Makefile` (add target), `docs/standards/mappings/lifecycle-evidence.json`
  (new artefact listing the last 3 verified slices).
- **Risk / leverage.** High leverage — 12207 is the parent governance process
  and already partially satisfied; one well-placed Makefile target turns PARTIAL
  into VERIFIED with low effort.

### Traceability — 12207

| Requirement | System element | File | Owner | Status |
|---|---|---|---|---|
| Iterative process | Slice-based delivery | `docs/rfcs/RFC-0001-slice-2-leases-and-recovery.md` | founder | PARTIAL |
| Decision management | ADR chain | `docs/adr/ADR-INDEX.md` | founder | PARTIAL |
| Work product control | Kanban + Makefile | `docs/kanban/board.json`, `Makefile` | founder | PARTIAL |
| Review / validation | e2e + chaos tests | `e2e/e2e_test.go`, `e2e/chaos_test.go` | founder | PARTIAL |
| Lifecycle evidence | Lifecycle bundle (missing) | `docs/standards/mappings/lifecycle-evidence.json` | founder | PLANNED |

---

## 2. ISO/IEC 25010:2023 — Quality Model

- **Standard.** `iso-iec-25010-2023` — 8 quality characteristics
- **Registry row.** `standard_id == "iso-iec-25010-2023"`,
  `control_id == "25010-CHARACTERISTICS"`, `status == "PARTIAL"`.
- **Applicability.** **In scope.** The eight characteristics (functional
  suitability, performance, compatibility, usability, reliability, security,
  maintainability, portability) are explicitly listed in the implementation
  note. Slice 1 + slice 2 already exercise functional suitability (e2e),
  reliability (chaos), performance (lost-worker SLO), and maintainability
  (`go vet`, `gofmt`). Compatibility and usability are deferred.
- **Current status.** PARTIAL. Registry exceptions: "Compatibility and
  usability metrics TBD."
- **Gap.** No `tests/quality/` package exists; quality characteristics are
  asserted by ad-hoc tests but not named under the 25010 vocabulary.
- **Concrete next step.** Create `tests/quality/iso25010_smoke_test.go` that
  (a) declares which 25010 characteristic each existing test suite covers
  (`functional_suitability` ← `e2e/`, `reliability` ← `e2e/chaos_test.go`,
  `performance` ← lease SLO test, `maintainability` ← `go vet`), and (b)
  fails CI if any previously covered characteristic drops out. File path:
  `tests/quality/iso25010_smoke_test.go` (new). Companion documentation in
  `docs/operations/QUALITY_CHARACTERISTICS.md` (new) listing how each of the
  8 characteristics is (or is not) addressed in V1.
- **Risk / leverage.** High leverage — 25010 is the canonical quality
  vocabulary; a single smoke test + documentation file lifts visibility
  across the full model without code changes.

### Traceability — 25010

| Characteristic | System element | File | Owner | Status |
|---|---|---|---|---|
| Functional suitability | e2e suite | `e2e/e2e_test.go` | founder | PARTIAL |
| Performance | Lost-worker SLO | `internal/worker/worker.go` (lease TTL 25s, reaper 5s) | founder | PARTIAL |
| Compatibility | Multi-OS worker target | `cmd/works-worker/` (build matrix) | founder | PLANNED |
| Usability | CLI UX | `cmd/works/` (slice 1) | founder | PLANNED |
| Reliability | Chaos test | `e2e/chaos_test.go` | founder | PARTIAL |
| Security | (separate SSD mapping) | `docs/standards/mappings/ssd.md` | founder | PARTIAL |
| Maintainability | `go vet`, gofmt | `Makefile` (`vet` target) | founder | PARTIAL |
| Portability | SQLite-on-disk + cross-OS worker | `services/work/store/store.go` | founder | PARTIAL |
| Coverage smoke test | New | `tests/quality/iso25010_smoke_test.go` | founder | PLANNED |

---

## 3. ISO/IEC/IEEE 29119 — Software Testing

- **Standard.** `iso-iec-ieee-29119` (2013-2024)
- **Registry row.** `standard_id == "iso-iec-ieee-29119"`,
  `control_id == "TEST-PROCESS"`, `status == "IMPLEMENTED"`,
  `evidence == "tests/"`, `test == "all tests pass"`.
- **Applicability.** **In scope.** The test pyramid (unit + integration + e2e
  + chaos + conformance) is operational across the repo.
- **Current status.** IMPLEMENTED. Registry indicates `IMPLEMENTED` rather than
  `PARTIAL` — the only quality standard currently at this level.
- **Gap.** Status is `IMPLEMENTED` but not `VERIFIED`; the registry's
  `test` field is `"all tests pass"` — a soft assertion that should be
  promoted to a recorded gate evidence pointer (e.g., a CI status badge or
  the SHA of the last green test run).
- **Concrete next step.** Promote `IMPLEMENTED → VERIFIED` by recording the
  evidence pointer. Add a JSON line to `docs/standards/mappings/quality.md`'s
  registry mirror, or update the canonical registry to `VERIFIED` with
  `evidence = "ci/local-runner/last-green-<sha>"` once a CI runner exists.
  For now (no CI runner directory yet), add `tests/TEST_EVIDENCE.md` (new)
  capturing the last 3 `go test ./...` + `make e2e` green SHAs and the
  command(s) used to reproduce.
- **Risk / leverage.** Medium leverage — 29119 is already in good shape; the
  gap is documentation rather than implementation.

### Traceability — 29119

| Test process area | System element | File | Owner | Status |
|---|---|---|---|---|
| Test planning | Slice RFC + test plan | `docs/rfcs/RFC-0001-slice-2-leases-and-recovery.md` | founder | IMPLEMENTED |
| Test design | `TestE2E_*` cases | `e2e/e2e_test.go` | founder | IMPLEMENTED |
| Test execution | `go test ./...`, `make e2e` | `Makefile` | founder | IMPLEMENTED |
| Test reporting | Test output + (missing) evidence pointer | `tests/TEST_EVIDENCE.md` (new) | founder | PLANNED |
| Chaos / recovery | `TestChaos_KillWorkerMidNode` | `e2e/chaos_test.go` | founder | IMPLEMENTED |

---

## 4. ISO/IEC 20000-1:2018 — Service Management

- **Standard.** `iso-iec-20000-1` — IT service management system (SMS)
- **Registry row.** `standard_id == "iso-iec-20000-1"`, `control_id == "SMS"`,
  `status == "PARTIAL"`, `evidence == "docs/operations/SLOS_AND_SRE.md"`.
- **Applicability.** **In scope.** works-execution is an internal platform
  with SLOs and an incident-response orientation; a full SMS certification
  is out of scope for V1, but SMS-style artefacts (SLOs, incident response,
  change management) are operating.
- **Current status.** PARTIAL. Registry: "Incident response + SLO definitions
  in place; full SMS deferred." Exception: "Full SMS certification requires
  external audit."
- **Gap.** The registry's `evidence` pointer `docs/operations/SLOS_AND_SRE.md`
  does not exist at that path — the canonical SLO doc lives at
  `docs/works-venture-starter-pack/05_OPERATIONS/SLOS_AND_SRE.md`. There is
  no `docs/operations/` directory yet.
- **Concrete next step.** Create the operations directory and an
  authoritative SLO doc that downstream mappings (this one, plus 25010 and
  22301) can cite. File path: `docs/operations/SLOS_AND_SRE.md` (new,
  content copied/extracted from the starter pack) plus
  `docs/operations/INCIDENT_RESPONSE.md` (new) listing the on-call flow,
  severity definitions, and the `RFC-0001` worker-loss recovery procedure
  as the canonical incident playbook. Once both exist, update
  `registry.json`'s `evidence` field to point at the new authoritative path.
- **Risk / leverage.** Medium leverage — the doc move unblocks 25010 and
  22301 traceability and removes the broken evidence pointer.

### Traceability — 20000-1

| SMS requirement | System element | File | Owner | Status |
|---|---|---|---|---|
| Service requirements | SLO targets | `docs/operations/SLOS_AND_SRE.md` (new) | founder | PLANNED |
| Service delivery | Slice e2e + chaos | `e2e/` | founder | PARTIAL |
| Incident response | Runbook (missing) | `docs/operations/INCIDENT_RESPONSE.md` (new) | founder | PLANNED |
| Change management | Slice RFC + ADR chain | `docs/rfcs/`, `docs/adr/` | founder | PARTIAL |
| Continual improvement | Kanban review | `docs/kanban/board.json` | founder | PARTIAL |

---

## 5. ISO 22301:2019 — Business Continuity

- **Standard.** `iso-22301` — Business continuity management system (BCMS)
- **Registry row.** `standard_id == "iso-22301"`, `control_id == "BCMS"`,
  `status == "PLANNED"`, evidence = `null`.
- **Applicability.** **In scope (severity-dependent).** works-execution holds
  the authoritative work state (SQLite + WAL) and lease state. Loss of the
  authoritative state store is named as the only `fail closed` condition in
  `SLOS_AND_SRE.md`. BC is therefore on the critical path.
- **Current status.** PLANNED. Registry: "Disaster recovery = SQLite WAL
  replay + S3 replica (PLANNED)."
- **Gap.** No BC plan, no recovery-time objective (RTO) for the store, no
  documented backup strategy, no S3 replica configuration. The risk register
  flags `Cache correctness` and `Secret exfiltration` as Critical but no BC
  controls exist yet.
- **Concrete next step.** Author `docs/operations/BC_PLAN.md` (new) that
  defines RTO/RPO targets, the SQLite WAL replay procedure, the S3 replica
  cadence, and the recovery runbook for the authoritative state store. Add
  the S3 replica to `services/work/store/store.go` as an opt-in backup hook
  guarded by `WORKS_BACKUP_S3_BUCKET`. File path: `docs/operations/BC_PLAN.md`
  (new) and `services/work/store/store.go` (extend).
- **Risk / leverage.** High leverage / high risk — 22301 is the only quality
  standard that gates customer trust in the platform's durability story, and
  it is currently PLANNED with no artefacts.

### Traceability — 22301

| BCMS requirement | System element | File | Owner | Status |
|---|---|---|---|---|
| BC policy | Authoritative-state durability | `docs/operations/BC_PLAN.md` (new) | founder | PLANNED |
| Risk assessment | (cross-link) | `docs/standards/RISK_REGISTER.md` (see §6) | founder | PLANNED |
| Recovery strategy | SQLite WAL + S3 replica | `services/work/store/store.go` | founder | PLANNED |
| Exercise & test | Backup replay test (missing) | `tests/quality/bc_replay_test.go` (new) | founder | PLANNED |
| Continual improvement | Post-incident review | `docs/operations/INCIDENT_RESPONSE.md` (cross-link) | founder | PLANNED |

---

## 6. ISO 31000:2018 — Risk Management

- **Standard.** `iso-31000` — Risk management principles & framework
- **Registry row.** `standard_id == "iso-31000"`, `control_id == "RISK-MGMT"`,
  `status == "PLANNED"`, evidence = `null`.
- **Applicability.** **In scope.** works-execution is a single-tenant
  execution platform where risk-aware prioritization (cache correctness,
  secret exfiltration, worker compromise) is a product feature. The starter
  pack already supplies a 10-row risk register
  (`docs/works-venture-starter-pack/07_BUSINESS/RISK_REGISTER.md`) with
  Critical and High severities.
- **Current status.** PLANNED. The starter pack has the seed; the
  authoritative risk register at the standards-mapping layer does not exist.
- **Gap.** No canonical `docs/standards/RISK_REGISTER.md`; no risk-review
  cadence linked from the kanban; the starter-pack risk register is not
  referenced from any standard mapping.
- **Concrete next step.** Promote the starter-pack risk register into an
  authoritative artefact at `docs/standards/RISK_REGISTER.md` (new). Add a
  review cadence field to `docs/kanban/board.json` (or a sibling
  `docs/kanban/risk-review.json`) that triggers a re-review every slice.
  File path: `docs/standards/RISK_REGISTER.md` (new), with cross-links
  from this mapping, from the 22301 BC plan, and from
  `docs/standards/mappings/ssd.md`.
- **Risk / leverage.** High leverage / high risk — 31000 is the parent of
  every other risk-aware decision in this mapping (22301, 20000-1, SSD).
  Authoring the register is a one-PR effort that unblocks all downstream
  risk evidence.

### Traceability — 31000

| 31000 step | System element | File | Owner | Status |
|---|---|---|---|---|
| Establish context | Venture scope | `README.md`, `docs/standards/registry.json` | founder | PARTIAL |
| Risk identification | Risk register seed | `docs/standards/RISK_REGISTER.md` (new) | founder | PLANNED |
| Risk analysis | Severity × Likelihood | (in register) | founder | PLANNED |
| Risk evaluation | Critical/High subset | (in register) | founder | PLANNED |
| Risk treatment | Mitigations tied to slices | (in register + ADRs) | founder | PLANNED |
| Monitoring & review | Per-slice re-review | `docs/kanban/board.json` (new card type) | founder | PLANNED |

---

## 7. ISO 9241-210:2019 — Human-Centred Design

- **Standard.** `iso-9241-210` — User-centred design process
- **Registry row.** `standard_id == "iso-9241-210"`, `control_id == "HCD"`,
  `status == "PLANNED"`, evidence = `null`.
- **Applicability.** **Deferred (low V1 leverage).** V1 ships CLI and HTTP
  API; no web UI. Registry: "Web UI deferred to slice 6; CLI is the primary
  UX in V1. HCD will apply when web UI lands."
- **Current status.** PLANNED.
- **Gap.** None actionable today. The CLI (`cmd/works/`) is small enough
  that ad-hoc usability is acceptable; the web UI does not exist.
- **Concrete next step.** No code change now. Track in
  `docs/kanban/board.json` (new `hcd-web-ui` card, lane `implementation`,
  slice 6). When the web UI slice starts, follow the §14 rule from this
  mapping: (a) define user-research artefacts at
  `docs/agents/hcd-personas.md` (new), (b) design tasks with the HCD
  iterative cycle (specify context of use → specify requirements →
  produce designs → evaluate), (c) record evidence in
  `docs/agents/hcd-evidence.md` (new).
- **Risk / leverage.** Low leverage in V1; will become Medium when the web
  UI lands.

### Traceability — 9241-210

| HCD activity | System element | File | Owner | Status |
|---|---|---|---|---|
| Context of use | CLI users (engineers) | `cmd/works/` | founder | PARTIAL |
| User requirements | (slice 6) | `docs/agents/hcd-personas.md` (new, slice 6) | founder | PLANNED |
| Design solutions | (slice 6) | (web UI, slice 6) | founder | PLANNED |
| Evaluation | (slice 6) | `docs/agents/hcd-evidence.md` (new, slice 6) | founder | PLANNED |

---

## 8. WCAG 2.2 — Web Content Accessibility Guidelines

- **Standard.** `wcag-2.2` — Web accessibility (A/AA/AAA)
- **Registry row.** `standard_id == "wcag-2.2"`, `control_id == "A11Y"`,
  `status == "PLANNED"`, evidence = `null`.
- **Applicability.** **Deferred (no web UI in V1).** Registry: "Web UI
  deferred; when built, will target AA."
- **Current status.** PLANNED.
- **Gap.** None actionable today. No web UI = no accessibility surface.
- **Concrete next step.** No code change now. When the web UI slice lands,
  enforce AA conformance by adding `axe-core` (or equivalent) to the
  Playwright/Cypress test runner used in slice 6, with assertions in
  `e2e/a11y/` (new). File path: `e2e/a11y/wcag_aa_test.ts` (new, slice 6).
- **Risk / leverage.** Low leverage in V1; mandatory before any web UI
  ships externally.

### Traceability — WCAG 2.2

| WCAG principle | System element | File | Owner | Status |
|---|---|---|---|---|
| Perceivable | (web UI, slice 6) | (slice 6) | founder | PLANNED |
| Operable | (web UI, slice 6) | (slice 6) | founder | PLANNED |
| Understandable | (web UI, slice 6) | (slice 6) | founder | PLANNED |
| Robust | (web UI, slice 6) | (slice 6) | founder | PLANNED |
| AA conformance test | axe-core runner | `e2e/a11y/wcag_aa_test.ts` (new, slice 6) | founder | PLANNED |

---

## 9. WAI-ARIA 1.2 — Accessible Rich Internet Applications

- **Standard.** `wai-aria` — ARIA roles, states, and properties
- **Registry row.** `standard_id == "wai-aria"`, `control_id == "ARIA"`,
  `status == "PLANNED"`, evidence = `null`.
- **Applicability.** **Bundled with WCAG 2.2.** Registry: "Bundled with
  wcag-2.2."
- **Current status.** PLANNED.
- **Gap.** None actionable today. No web UI = no ARIA surface.
- **Concrete next step.** Bundle with WCAG 2.2 implementation in slice 6.
  Add ARIA role coverage to the axe-core runner above by including
  `aria-*` rule tags. File path: `e2e/a11y/wcag_aa_test.ts` (same file as
  §8; new rule tags only).
- **Risk / leverage.** Low leverage in V1; rises with the web UI.

### Traceability — WAI-ARIA

| ARIA capability | System element | File | Owner | Status |
|---|---|---|---|---|
| Roles / states / properties | (web UI, slice 6) | (slice 6) | founder | PLANNED |
| Conformance test | axe-core ARIA rules | `e2e/a11y/wcag_aa_test.ts` (shared, slice 6) | founder | PLANNED |

---

## Cross-cutting risks (rolled up from §1-§9)

1. **Evidence pointer rot.** The registry's `evidence` field for
   `iso-iec-20000-1` points at a path that does not exist
   (`docs/operations/SLOS_AND_SRE.md`). Fix by creating the file
   (§4 next step).
2. **No quality test package.** `tests/quality/` does not exist. Several
   mappings (25010, 29119, 22301) want to land tests there. Establish the
   package as part of the §2 next step.
3. **Risk register at the standards layer is missing.** The starter-pack
   version exists; the authoritative standards-layer version does not
   (§6 next step). This single gap propagates into 22301, 20000-1, and the
   SSD mapping.

## Highest-value actionable gap (single recommendation)

> **Author `docs/standards/RISK_REGISTER.md`** by promoting the starter-pack
> risk register and cross-linking it from this document, the 22301 BC plan,
> and `docs/standards/mappings/ssd.md`. This unblocks evidence traceability
> for 31000, 22301, 20000-1, and every SSD mapping at one stroke, and is the
> minimum effort for maximum leverage across the quality + ssd domains.