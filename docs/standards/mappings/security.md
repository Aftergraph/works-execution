# Security, Privacy & Governance Standards — Per-Standard Mapping

> **Purpose.** Per-standard mapping for the 16 security/privacy/governance standards in
> [`docs/standards/registry.json`](../registry.json) where `domain == "security"`.
>
> **Method.** The §14 implementation rule from the user-mandated standards charter:
> (1) determine applicability, (2) map to system requirements, (3) identify gaps,
> (4) prioritize by risk and leverage, (5) recommend the highest-value actionable gap
> with an explicit file path. BLOCKED standards call out the required external
> dependency and the unblock trigger.
>
> **Ground truth for current state.** `docs/standards/registry.json` rows (status,
> enforcement_point, evidence). Slice-1 (commit `d3db1d1`) and slice-2 (commit
> `dab84f2`) shipped: Work primitive, SQLite store, HTTP API (`cmd/works-api`),
> CLI (`cmd/works`), polling subprocess worker (`internal/worker/worker.go`),
> lease-based scheduling, worker-loss recovery, log streaming.

---

## Table of contents

| § | Standard | Status |
|---|----------|--------|
| 1 | ISO/IEC 27001:2022 ISMS | PARTIAL → BLOCKED for certification |
| 2 | ISO/IEC 27002:2022 Code of Practice | PARTIAL |
| 3 | ISO/IEC 27017:2015 Cloud Services | PARTIAL |
| 4 | ISO/IEC 27018:2019 PII in Public Clouds | PARTIAL |
| 5 | ISO/IEC 27701:2019 Privacy Extension | PARTIAL (bundled with §4) |
| 6 | SOC 2 Trust Services Criteria | BLOCKED |
| 7 | NIST CSF 2.0 | PARTIAL |
| 8 | NIST SP 800-53 Rev. 5 | PARTIAL (~30% of in-scope controls) |
| 9 | NIST SP 800-207 Zero Trust Architecture | PARTIAL |
| 10 | CIS Critical Security Controls v8 | PARTIAL |
| 11 | CIS Benchmarks | PLANNED |
| 12 | GDPR (Reg. 2016/679) | PARTIAL |
| 13 | EU NIS2 (Dir. 2022/2555) | PLANNED |
| 14 | EU Cyber Resilience Act (CRA, 2024) | PLANNED |
| 15 | EU Data Act | PARTIAL |
| 16 | EU Data Governance Act (Reg. 2022/868) | NOT_APPLICABLE |

**Final traceability table** at the bottom maps every `standard_id` to its section
and to the row in `registry.json`.

---

## 1. ISO/IEC 27001:2022 — Information Security Management System (ISMS)

**Applicability.** Applicable. The platform handles operator credentials, audit
logs, and lease state; any outage or breach has direct customer impact. Even
without certification, the ISMS shape (policy → risk → statement of
applicability → control → evidence → review) is the right backbone for all
other security standards in this document.

**System requirements it maps to.**
- **A.5 Organizational controls** — ISMS scope statement, roles, and review
  cadence.
- **A.6 People controls** — onboarding/offboarding, training records.
- **A.7 Physical & environmental** — N/A (SaaS only).
- **A.8 Technological controls** — RBAC, audit logging, secrets management,
  vulnerability management, change management.

**Current status — PARTIAL.** `registry.json` reports PARTIAL with
`implementation: "Partial via ADRs + RFC-0001 + capability manifests."`
Concretely landed in code: capability manifest enforcement, hermetic
default-deny execution (`internal/worker/worker.go` subprocess scoping),
audit-event emission, lease-based access control. Missing: the unifying
policy file, risk register, statement of applicability (SoA), and the
management-review cadence.

**Gap.** No `docs/security/policy.md`, no `docs/security/RISK_REGISTER.md`,
no SoA. Each downstream standard (27002, 27017, CSF 2.0, 800-53) inherits
this gap because the ISMS is the spine they hang from.

**Highest-leverage next step.** Author the ISMS policy + risk register
before adding any more 27002/800-53 controls — otherwise controls accumulate
without a unified risk-treatment story and audit (when it eventually
happens) becomes an archaeology exercise.

- **File to create:** `docs/security/policy.md` (scope, roles, SoA pointer,
  review cadence, control owners).
- **File to create:** `docs/security/RISK_REGISTER.md` (asset → threat →
  likelihood × impact → treatment → owner).
- **Linked registry rows:** cross-references in this same mapping for §§2, 7,
  8, 10.

**⚠ BLOCKED for formal certification.** `registry.json` lists
`blocked_reason: "Formal ISO 27001 certification requires external audit
body."` and `unblock_check: "Engagement of an ISO 27001 certification body."`
The internal ISMS can reach IMPLEMENTED on its own; transitioning to
VERIFIED requires an accredited certification body (national accreditation
body, e.g. UKAS / ANAB / DAkkS) conducting Stage 1 (documentation) and
Stage 2 (operational) audits, then issuing a 3-year certificate with
annual surveillance audits. **No in-repo action unblocks certification.**

---

## 2. ISO/IEC 27002:2022 — Code of Practice for Information Security Controls

**Applicability.** Applicable as the operational control catalog that
implements the ISMS (§1).

**System requirements it maps to.** 93 controls across four themes:
- **Organizational** (37): policies, supplier relationships, incident
  management.
- **People** (8): screening, awareness.
- **Physical** (14): N/A for SaaS.
- **Technological** (34): RBAC, audit, secrets, vulnerability management.

**Current status — PARTIAL.** The registry notes
`implementation: "Mapped in docs/standards/mappings/security.md; specific
controls enforced in code (RBAC, secrets, audit)."` Test surface is
`tests/security/` (planned).

**Gap.** Specific controls known-implemented vs not-implemented are not
enumerated anywhere; no `tests/security/` package exists; no SoA-level
mapping from 27001 §1 to 27002 controls.

**Highest-leverage next step.** Enumerate the 27002 Annex A controls that
*are* implemented (RBAC = A.5.15/A.8.2; capability manifest = A.8.2;
audit events = A.8.15; secrets from env, never source = A.8.24; input
validation = A.8.28; lease-based execution isolation = A.8.31) and the
top five that are not yet (A.5.7 threat intelligence, A.5.23–A.5.30
supplier relationships, A.5.24 incident management plan, A.8.8 vuln
management, A.8.16 monitoring activities). This becomes the §1 SoA.

- **File to create:** `docs/security/soa-27002.md` (control ID →
  implemented in / owner / evidence / next step).
- **File to create:** `internal/security/audit/audit.go` skeleton with
  A.8.15-shaped event types and tests in `tests/security/audit_test.go`.

---

## 3. ISO/IEC 27017:2015 — Code of Practice for Cloud Services

**Applicability.** Conditionally applicable. `registry.json` notes
`"Apply when hosted on cloud; for V1 mostly on-prem / BYOC."` Today the
default deployment is BYOC (bring-your-own-cluster) so the platform's
cloud-customer responsibilities are limited; the cloud-*provider*
responsibilities are the customer's.

**System requirements.** Cloud-specific extensions to 27002:
- Shared responsibilities between cloud customer and provider (Cl. 5.1.1).
- Customer's responsibility for in-VM OS hardening, identity, data.
- Removal/return of assets at contract end (Cl. 8.1.4 / A.5.23).
- Virtualization/network isolation (Cl. 8.16 / A.8.22).

**Current status — PARTIAL.** No explicit cloud-deployment guidance;
presumed responsibility split follows the BYOC default.

**Gap.** No `docs/deployment/SHARED_RESPONSIBILITY.md`; no documented
cloud-tenant isolation posture for when the platform itself is hosted
on a managed cloud (e.g. ECS/EKS).

**Highest-leverage next step.** Publish the shared-responsibility matrix
even before a managed-cloud deployment exists — every customer onboarding
will ask for it, and writing it forces clarity on what runs where.

- **File to create:** `docs/deployment/SHARED_RESPONSIBILITY.md` (table:
  control | customer | works-execution | provider | reference).

---

## 4. ISO/IEC 27018:2019 — PII Protector in Public Clouds Acting as PII Processor

**Applicability.** Low. `registry.json` notes
`"No PII processed in V1; Work objects carry no customer personal data by
design."` Still applicable because the policy must hold even when a
future customer tries to put PII into a Work.

**System requirements.**
- Consent and purpose limitation for any PII that lands in source/objective/
  policy fields.
- PII disclosure notification and onward-transfer controls (Cl. 5.4 / 5.5).
- Data minimization, retention, secure deletion (Cl. 5.6 / 5.7 / 6.5).
- Subcontractor transparency (Cl. 7.1).

**Current status — PARTIAL.** `enforcement_point: "Source/objective/
policy fields in Work schema reject PII patterns (PLANNED)."`

**Gap.** No PII-pattern validator on the Work schema; no redaction in
audit logs (`services/api/leases.go` records raw data); no retention rule.

**Highest-leverage next step.** Add a PII-pattern guard to the Work
schema validator so the "no PII by design" promise becomes an enforced
invariant rather than a documented intent. This single validator delivers
half the standard's value at minimal code cost.

- **File to create:** `packages/workgraph/pii_guard.go` (regex/length/
  field-type checks; reject on match).
- **File to modify:** `services/api/api.go` (call guard before store).
- **Test:** `packages/workgraph/pii_guard_test.go`.

---

## 5. ISO/IEC 27701:2019 — Privacy Extension to ISO/IEC 27001

**Applicability.** Bundled with §4. `registry.json` says
`"Bundled with iso-iec-27018."`

**System requirements.** PIMS (Privacy Information Management System)
controls layered on the ISMS: PII controller vs processor roles,
data-subject rights handling, privacy-by-design.

**Current status — PARTIAL.** Same posture as §4; no customer PII is
processed, so the privacy-rights machinery (DSAR endpoint, deletion
endpoints) is not implemented.

**Gap.** No DSAR/deletion endpoints; no privacy notice text.

**Highest-leverage next step.** Ship the §4 PII guard first (it has
the highest leverage and is a prerequisite for any PIMS work to be
meaningful). Then add DSAR-equivalent: a `DELETE /v1/works/{id}` that
guarantees purge of all related audit events within a published SLA.

- **File to create:** `docs/privacy/PRIVACY_NOTICE.md`.
- **File to create:** `services/api/privacy.go` (`DELETE /v1/works/{id}`
  handler; cascading audit-event purge).

---

## 6. SOC 2 Trust Services Criteria (2017 TSC)

**Applicability.** Applicable as a *target* attestation but not yet
implemented. Will be required by enterprise customers before any
production rollout.

**System requirements.** Five Trust Services Criteria:
- **CC** (Common Criteria) — control environment, communication, risk
  assessment, monitoring.
- **A** (Availability) — capacity, recovery.
- **PI** (Processing Integrity) — accurate, complete, timely processing.
- **C** (Confidentiality)** — confidential info protection.
- **P** (Privacy)** — personal info handling (overlap with GDPR).

**Current status — BLOCKED.** `registry.json`:
```
status: BLOCKED
implementation: "Deferred — no SOC 2 Type II audit yet."
blocked_reason: "Requires SOC 2 Type II audit by licensed CPA firm."
unblock_check: "Engagement of SOC 2 auditor; readiness assessment first."
```

**⚠ BLOCKED — external dependency.** A SOC 2 Type II report is only
issuable by a licensed CPA firm that is registered with the AICPA and
has undergone a peer review. Specifically required:

1. **Engagement of a licensed CPA firm** with a SOC practice (typically
   one of the Big 4, national mid-tier, or specialized SOC firms —
   Vanta/Drata/Secureframe do *not* themselves issue the report; they
   are readiness/automation platforms).
2. **A SOC 2 readiness assessment** (typically 4–8 weeks) to gap-analyse
   controls against TSC.
3. **Observation window** for Type II (minimum 3 months, commonly 6 or
   12 months) during which all controls must operate and evidence is
   continuously collected.
4. **Audit fieldwork** by the CPA firm and issuance of the Type II
   report.

**In-repo work that *can* happen now** (does not unblock the audit, but
shrinks the readiness gap): implement every CC criterion (CC1.1–CC9.2),
ship the incident-response runbook (CC7.3/4), implement access reviews
(CC6.2/3), background-check policy (CC1.4), and vendor-risk register
(CC9.2). These are tracked in the kanban board's `governance` lane.

**Highest-leverage next step (repo-internal).** Run a documented TSC
readiness self-assessment and capture the result as the basis for the
eventual CPA engagement.

- **File to create:** `docs/compliance/SOC2_READINESS.md` (TSC criterion →
  status → evidence pointer → gap → owner).

---

## 7. NIST Cybersecurity Framework 2.0

**Applicability.** Applicable. CSF 2.0's six functions (Govern, Identify,
Protect, Detect, Respond, Recover) are the most natural skeleton for
this mapping document and for the platform's incident-response design.

**System requirements.**
- **GV** (Govern — new in 2.0) — organizational context, risk management
  strategy, roles.
- **ID** (Identify) — asset inventory, risk assessment.
- **PR** (Protect) — identity mgmt, awareness, data security, platform
  security, technology infra resilience.
- **DE** (Detect) — anomalies, continuous monitoring, detection processes.
- **RS** (Respond) — incident management, analysis, mitigation, reporting.
- **RC** (Recover) — recovery planning, improvements, communications.

**Current status — PARTIAL.** `registry.json`:
- Identify: capability manifest ✅
- Protect: hermetic default ✅
- Detect: OTel + audit ✅
- Respond: incident runbook PLANNED
- Recover: lost-worker recovery ✅ (slice 2)

**Gap.** Govern (new in 2.0) is not addressed; incident-response runbook
not authored; detection coverage is OTel-shaped but not mapped to
specific threats.

**Highest-leverage next step.** Author the incident-response runbook —
it closes the largest *and* oldest CSF gap, and the runbook doubles as
evidence for SOC 2 CC7.3 and ISO 27001 A.5.24.

- **File to create:** `docs/runbooks/INCIDENT_RESPONSE.md` (triage →
  severity → on-call → comms → post-mortem).
- **File to create:** `docs/security/CONTACT_ROTATION.md` (on-call
  schedule + escalation matrix).

---

## 8. NIST SP 800-53 Rev. 5 — Security and Privacy Controls

**Applicability.** Applicable. `registry.json` estimates ~30% of relevant
controls implemented in V1 (AC, AU, IA, SI families). The full catalog
is 1000+ controls; the in-scope subset is the ~120 that map to a
multi-tenant job-scheduler SaaS.

**System requirements (in-scope families).**
- **AC** (Access Control) — AC-2 account mgmt, AC-3 enforcement, AC-6
  least privilege, AC-16 security attributes.
- **AU** (Audit and Accountability) — AU-2 event logging, AU-3 content,
  AU-4 storage capacity, AU-6 review.
- **IA** (Identification and Authentication) — IA-2 user identification,
  IA-5 authenticator mgmt.
- **SI** (System and Information Integrity) — SI-2 flaw remediation,
  SI-7 software/firmware integrity, SI-10 input validation.

**Current status — PARTIAL.** Implemented in V1: capability manifests
(AC-6), audit-event shape (AU-2/3), input validation on Work (SI-10).
**Exception in registry:** `"Full 800-53 implementation is multi-quarter."`

**Gap.** No SI-2 flaw-remediation SLA; no AU-6 review tooling; no IA-5
authenticator-policy document; no baseline-config artifact for AC,
CM families.

**Highest-leverage next step.** Define and publish a *vulnerability
remediation SLA matrix* (CRITICAL = 24 h, HIGH = 7 d, MEDIUM = 30 d,
LOW = 90 d) and wire it into `ci/local-runner/`. SI-2 is the single
control most often cited as the difference between "secure dev" and
"compliant dev".

- **File to create:** `docs/security/VUL_SLA.md` (severity → SLA →
  owner → escalation).
- **File to create:** `ci/local-runner/vuln_gate.sh` (gate that fails
  when any known vuln exceeds its SLA — uses deps.dev / OSV data).

---

## 9. NIST SP 800-207 — Zero Trust Architecture

**Applicability.** Applicable. The worker → API → store chain is a
canonical zero-trust surface: no implicit network trust, every call
authenticated and authorized.

**System requirements.**
- Logical identity component (workload identity) — SPIFFE/SPIRE-style.
- Policy enforcement point (PEP) — capability admission in the API.
- Policy administrator / engine — capability manifests stored in API.
- Continuous diagnostics — OTel-driven posture signals.

**Current status — PARTIAL.** `registry.json` ties this to three
open/closed items:
- **Workload identity (SPIFFE) — PLANNED** (kanban #89).
- **Short-lived credentials — Zero-Secret** (kanban #114).
- **Least privilege — capability manifests** (kanban #110).

**Gap.** No SPIFFE identity issuance yet; static bearer tokens in the
worker; capability admission not yet enforced at the API edge.

**Highest-leverage next step.** Enforce capability admission at the API
edge *before* layering SPIFFE on top — without admission enforcement,
SPIFFE only proves "who", not "what they're allowed to do". This is
also the highest leverage single ticket for §1, §2, §10, and §15.

- **File to create:** `services/api/admission.go` (capability manifest
  validator called on every authenticated request).
- **Test:** `services/api/admission_test.go`.

---

## 10. CIS Critical Security Controls v8

**Applicability.** Applicable. 18 controls ordered by effectiveness.
`registry.json` notes partial coverage across Inventory, Secure Config,
Access Control, Audit, and Vulnerability Mgmt.

**System requirements.** 18 controls (IG1 = 56 safeguards, IG2 = 74,
IG3 = 23); the in-scope subset for this platform is the IG1 essentials
plus IG2 selectives for the audit/log and vuln-mgmt controls.

**Current status — PARTIAL.** Implemented: works registry = CIS-1
(inventory), default-deny = CIS-4 (secure config), RBAC = CIS-5/6
(access control), OTel = CIS-8 (audit log management), dep tracking in
`go.mod` = CIS-7 (continuous vuln mgmt).
PLANNED: CI gates for secrets, license, vuln scanning.

**Gap.** CIS-2 (software inventory) for third-party deps is implicit in
`go.sum` but no SBOM is generated; CIS-3 (data protection) overlaps
with §4 above; CIS-14 (security awareness) is not formalized.

**Highest-leverage next step.** Add the CI secrets-scanning gate — CIS-3
*and* CIS-14 overlap on it, and a single pre-commit + CI hook prevents
the most common incident class (a leaked API key) before it ships.

- **File to create:** `ci/local-runner/secrets_gate.sh` (gitleaks /
  trufflehog scan; non-zero exit on match).
- **File to create:** `.gitleaks.toml` (allowlist for test fixtures).
- **File to modify:** `Makefile` (wire `secrets_gate` into `make vet`).

---

## 11. CIS Benchmarks

**Applicability.** Applicable *conditionally* — depends on the actual
deployment surface (Docker image, VM image, k8s). `registry.json`
status: PLANNED, `"implementation": "Deferred — depends on deployment
surface (Docker, VM image)."`

**System requirements.** OS-level hardening (kernel modules disabled,
sysctl values, file permissions, package allowlist, auditd rules).
For Docker: CIS Docker Benchmark §1–§6.

**Current status — PLANNED.** No Docker image exists in V1 yet; the
worker is run as a host process today.

**Gap.** No published hardened image; no Dockerfile in repo.

**Highest-leverage next step.** Produce a Dockerfile for the worker +
API *and* run the CIS Docker Benchmark scan in CI as soon as the
Dockerfile lands. Adopting the benchmark before the first public image
is far cheaper than retrofitting.

- **File to create (deferred until first container image):**
  `Dockerfile`, `Dockerfile.api`, `.dockerignore`.
- **File to create:** `ci/local-runner/cis_docker.sh` (docker-bench-run
  wrapper; fail on `WARN` count > threshold).

---

## 12. GDPR (Regulation 2016/679)

**Applicability.** Applicable to the data the platform *touches*, not
to the platform itself per `registry.json` exception
`"Applies to the data we touch, not to the platform itself."` The
platform processes Work metadata (source, objective, policy) which by
V1 design contains no PII; logs are scoped to operational events.

**System requirements.**
- **Art. 5** — lawfulness, fairness, transparency, purpose limitation,
  data minimization, accuracy, storage limitation, integrity,
  confidentiality, accountability.
- **Art. 15–22** — data-subject rights (access, rectification, erasure,
  restriction, portability, objection).
- **Art. 25** — privacy by design and by default.
- **Art. 30** — records of processing activities (RoPA).
- **Art. 32** — security of processing.
- **Art. 33–34** — breach notification (72 h to supervisory authority).

**Current status — PARTIAL.** By-design posture documented; source
fields limited; log redaction PLANNED; RoPA not yet authored.

**Gap.** No RoPA artifact; no documented breach-notification procedure
for the 72-h clock; no DSAR endpoint (overlaps with §5).

**Highest-leverage next step.** Author the RoPA — it is the single GDPR
document most often requested by EU enterprise customers, and writing
it forces explicit decisions on retention, lawful basis, and recipients.

- **File to create:** `docs/privacy/ROPA.md` (controller/processor,
  lawful basis per processing purpose, recipients, retention,
  safeguards).
- **File to create:** `docs/runbooks/BREACH_NOTIFICATION.md` (72-h
  clock procedure, supervisory-authority contact tree).

---

## 13. EU NIS2 (Directive 2022/2555)

**Applicability.** Applicable if/when an EU enterprise customer is
served. `registry.json` notes
`"Incident response runbook required before any EU customer rollout."`

**System requirements.**
- **Art. 21** — risk-management measures (10 baseline measures: incident
  handling, supply-chain security, BC, cryptography, access control,
  etc.).
- **Art. 23** — incident notification: early warning within 24 h,
  notification within 72 h, final report within one month.
- **Art. 21(2)(f)** — encryption and cryptography policies.

**Current status — PLANNED.** No incident response runbook yet; no
24/72-h notification clock documented.

**Gap.** All of Art. 21 in documented form; Art. 23 notification clock.

**Highest-leverage next step.** Reuse the §7 incident-response runbook
and the §12 breach-notification runbook — NIS2's 24/72-h clocks are
close to GDPR's 72-h clock, so a single runbook with two clock panels
covers both. Avoiding duplicate runbooks is the leverage.

- **File to modify:** `docs/runbooks/INCIDENT_RESPONSE.md` (add §NIS2
  with 24-h early warning + 72-h notification panels).

---

## 14. EU Cyber Resilience Act (CRA, 2024)

**Applicability.** Applicable — works-execution ships "products with
digital elements" (the worker binary, the API). CRA applies to the
manufacturer placing the product on the EU market.

**System requirements.**
- **Annex I §1** — secure-by-default configuration.
- **Annex I §2** — attack-surface minimization.
- **Annex I §3** — vulnerability handling (disclosure policy, SBOM).
- **Annex I §6** — mechanisms to keep software up to date.
- **Art. 13** — technical documentation.
- **Art. 14** — conformity assessment.

**Current status — PLANNED.** `registry.json`:
`"Vulnerability handling process + SBOM required before public release."`
`"SBOM generation (CycloneDX, PLANNED), vuln scanning in CI."`

**Gap.** No vulnerability-disclosure policy (`SECURITY.md`); no SBOM;
no CRA conformity-assessment posture document.

**Highest-leverage next step.** Publish a SECURITY.md with a
vulnerability-disclosure policy — it is the cheapest single deliverable
in the CRA set, and researchers are blocked from sending reports without
it. A `security@…` alias and 90-day coordinated-disclosure commitment is
sufficient as a starter policy.

- **File to create:** `SECURITY.md` (supported versions table, reporting
  channel, response timeline, coordinated-disclosure commitment).
- **File to create:** `ci/local-runner/sbom.sh` (CycloneDX generation;
  diff-checked in PR).
- **Test:** `ci/local-runner/sbom_test.go`.

---

## 15. EU Data Act (Reg. 2024/...)

**Applicability.** Partially applicable — applies to connected-product
data and to cloud-switching interoperability. The most relevant
sub-articles for this platform are the data-portability and
interoperability ones.

**System requirements.**
- **Art. 5–6** — user access to and portability of data generated by
  use of a connected product / service.
- **Art. 7** — near-real-time data access.
- **Art. 23–31** — cloud-switching interoperability (no lock-in).

**Current status — PARTIAL.** `registry.json`:
`"Worker protocol is open; not a lock-in. Users can extract all their
data via /v1/works and /v1/audit-events."` — i.e. portability is
substantively addressed through the open API; cloud-switching is N/A
(BYOC default).

**Gap.** No documented "data export" recipe; no `portable.json`
manifest; the API contract is open but not formally asserted as
Data-Act-compliant.

**Highest-leverage next step.** Author a portable-export runbook so a
customer (or auditor) can verify Data Act portability claims by
following the recipe in 5 minutes.

- **File to create:** `docs/operations/DATA_EXPORT.md` (cURL recipe:
  list all works, list all audit events, list all leases; output
  formats; retention; re-import path).

---

## 16. EU Data Governance Act (Reg. 2022/868)

**Applicability.** NOT_APPLICABLE. `registry.json`:
`"Platform is not a public-sector data intermediary."`
`exceptions: ["Platform is not a public-sector data intermediary."]`

The Data Governance Act applies to public-sector bodies re-using
certain categories of public data, and to private "data intermediation
service providers" that facilitate data sharing between data holders
and data users. works-execution is neither.

**No gap, no next step.** Re-evaluate only if the venture expands into
becoming a data-intermediation service (e.g. a marketplace for Work
results between enterprises), in which case this row flips to
APPLICABLE and a dedicated mapping would be authored.

---

## Cross-cutting summary — priority-ordered concrete next steps

In order of leverage (each step delivers value for multiple standards):

| # | File to create/modify | Standards it advances |
|---|-----------------------|-----------------------|
| 1 | `docs/security/policy.md` + `docs/security/RISK_REGISTER.md` | §1 ISMS, §2 SoA, §7 GV |
| 2 | `docs/runbooks/INCIDENT_RESPONSE.md` + `docs/runbooks/BREACH_NOTIFICATION.md` | §7 RS, §12, §13 |
| 3 | `services/api/admission.go` + test | §9 zero-trust, §1, §2, §10, §15 |
| 4 | `packages/workgraph/pii_guard.go` + test | §4, §5, §12 |
| 5 | `ci/local-runner/secrets_gate.sh` + `.gitleaks.toml` + `Makefile` | §10, §14 |
| 6 | `docs/security/soa-27002.md` | §1, §2 |
| 7 | `ci/local-runner/sbom.sh` + `SECURITY.md` | §14, §10 |
| 8 | `docs/deployment/SHARED_RESPONSIBILITY.md` | §3, §15 |
| 9 | `docs/compliance/SOC2_READINESS.md` | §6 readiness (does not unblock §6) |
| 10 | `docs/privacy/ROPA.md` | §12, §5 |

## Traceability table (registry `standard_id` → this document)

Every security/privacy/governance row in `docs/standards/registry.json`
maps to a section above. `registry.json` lines are 1-indexed from the
file.

| standard_id (registry) | registry §line | This doc § | registry status |
|------------------------|---------------:|-----------:|-----------------|
| `iso-iec-27001`        | 277            | §1         | PARTIAL |
| `iso-iec-27002`        | 294            | §2         | PARTIAL |
| `iso-iec-27017`        | 309            | §3         | PARTIAL |
| `iso-iec-27018`        | 324            | §4         | PARTIAL |
| `iso-iec-27701`        | 339            | §5         | PARTIAL |
| `soc2-tsc`             | 354            | §6         | **BLOCKED** |
| `nist-csf-2.0`         | 371            | §7         | PARTIAL |
| `nist-sp-800-53`       | 386            | §8         | PARTIAL |
| `nist-sp-800-207`      | 401            | §9         | PARTIAL |
| `cis-controls-v8`      | 416            | §10        | PARTIAL |
| `cis-benchmarks`       | 431            | §11        | PLANNED |
| `gdpr`                 | 446            | §12        | PARTIAL |
| `eu-nis2`              | 461            | §13        | PLANNED |
| `eu-cra`               | 476            | §14        | PLANNED |
| `eu-data-act`          | 491            | §15        | PARTIAL |
| `eu-data-governance-act` | 506          | §16        | NOT_APPLICABLE |

---

## Blocked-standards unblock summary

| standard_id | External dependency required | In-repo work that helps readiness (does not unblock) |
|-------------|------------------------------|------------------------------------------------------|
| `soc2-tsc` | Engagement of a licensed CPA firm with an active AICPA SOC practice; readiness assessment; observation window (Type II ≥ 3 months); audit fieldwork. | `docs/compliance/SOC2_READINESS.md`; CC-series controls; incident-response runbook; access-review cadence. |
| `iso-iec-27001` (certification) | Engagement of an accredited certification body (national accreditation body e.g. UKAS/ANAB/DAkkS); Stage 1 + Stage 2 audits; annual surveillance audits. | `docs/security/policy.md` + SoA + RISK_REGISTER.md; completed 27002 Annex A mapping. |

> **No in-repo action moves either of these to VERIFIED.** Status will
> stay at PARTIAL with `blocked_reason` populated until the external
> dependency is engaged. Recheck trigger = `unblock_check` field in the
> registry row.