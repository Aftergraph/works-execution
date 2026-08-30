# AI & Agentic — Per-Standard Mapping

**Document ID:** `works-standards-ai-mapping`
**Venture:** works-execution (`github.com/JonasAbde/works-execution`)
**Generated:** 2026-08-31
**Slice context:** Slice 1 (`d3db1d1`) shipped the `Work` primitive, SQLite store, HTTP API, CLI, and polling subprocess worker. Slice 2 (`dab84f2`) added lease-based scheduling, worker-loss recovery, and log streaming. The Slice 2 worker is the only AI-relevant artifact in the platform today; it is a **deterministic subprocess executor, not an ML model** (see `docs/agents/worker.md` §Model). AI/ML components are deferred to slice 6+ (`platform-ai-failure-intel` in the registry).
**Companion documents:**
- `docs/standards/registry.json` — authoritative machine-readable registry (130 rows)
- `docs/standards/mappings/ai.md` — this document
- `docs/agents/worker.md` — Slice 2 worker capability declaration (governance anchor for the agentic rows below)
- `docs/kanban/board.json` — Slice 3+ work tracker

---

## §14 Implementation Rule (binding)

Every standard in this document is processed through the five-step rule from the user-mandated standards charter:

1. **Determine applicability** — is this standard in-scope for works-execution V1?
2. **Map to system requirements** — which concrete component, contract, or test enforces it?
3. **Identify gaps** — what is missing today (Slice 1 + Slice 2)?
4. **Prioritize by risk and leverage** — score each gap on (risk-of-omission × leverage-on-platform-correctness).
5. **Recommend highest-value actionable gap with file path** — the next concrete change, where it lands, and the acceptance evidence.

---

## §1. Scope

The registry contains **16 rows** in the `ai` domain. All 16 are tracked below. After deduplication and not-applicable filtering, the **active standards** are:

| #  | Standard                       | Registry row                  | Status (today)         |
|----|--------------------------------|-------------------------------|------------------------|
| 1  | ISO/IEC 42001:2023 (AIMS)      | `iso-iec-42001-2023`          | PARTIAL                |
| 2  | ISO/IEC 23894:2023 (AI Risk)   | `iso-iec-23894-2023`          | PARTIAL                |
| 3  | ISO/IEC 22989:2022 (Vocabulary)| `iso-iec-22989-2022`          | PLANNED                |
| 4  | NIST AI RMF 1.0                | `nist-ai-rmf-1.0`             | PARTIAL                |
| 5  | MITRE ATLAS                    | `mitre-atlas`                 | PARTIAL                |
| 6  | EU AI Act                      | `eu-ai-act`                   | **BLOCKED**            |
| 7  | ISO/IEC 23053:2022 (ML frame)  | `iso-iec-23053-2022`          | NOT_APPLICABLE         |
| 8  | ISO/IEC 23053 GenAI Amendment  | `iso-iec-23053-genai-amendment` | NOT_APPLICABLE       |
| 9  | ISO/IEC 5259-1 (DQ overview)   | `iso-iec-5259-1`              | NOT_APPLICABLE         |
| 10 | ISO/IEC 5259-2 (DQ methods)    | `iso-iec-5259-2`              | NOT_APPLICABLE         |
| 11 | ISO/IEC 5259-3 (DQ framework)  | `iso-iec-5259-3`              | NOT_APPLICABLE         |
| 12 | ISO/IEC 5259-4 (DQ process)    | `iso-iec-5259-4`              | NOT_APPLICABLE         |
| 13 | ISO/IEC 5259-5 (DQ taxonomy)   | `iso-iec-5259-5`              | NOT_APPLICABLE         |
| 14 | ISO/IEC TR 5259-6:2026 (guide) | `iso-iec-tr-5259-6-2026`      | NOT_APPLICABLE         |
| 15 | NIST AI 600-1 (GenAI profile)  | `nist-ai-600-1`               | NOT_APPLICABLE         |
| 16 | OWASP Top 10 for LLM Apps 2026 | `owasp-genai-llm-top10-2026`  | NOT_APPLICABLE         |

**Key point:** the `works-worker` is non-LLM autonomous software (a subprocess executor that polls a control plane, requests leases, runs commands, reports results). It is governed by AI/agentic standards even though it has no model — those standards cover autonomous decision-makers, not only ML.

---

## §2. Active standards (PARTIAL / PLANNED / BLOCKED)

### 2.1 ISO/IEC 42001:2023 — AI Management System (`iso-iec-42001-2023`)

**Registry row:** `control_id=AIMS-POLICY`, `status=PARTIAL`, `owner=founder`.

**Applicability.** In-scope. ISO/IEC 42001 covers the management system for any AI capability that the venture deploys. The Slice 2 worker is the first such capability, so the AIMS pilot is the worker declaration itself. Scope expands when `platform-ai-failure-intel` ships (slice 6+).

**Current implementation (Slice 1+2).** `docs/agents/worker.md` is the policy-level registration of the capability: identity, purpose, owner, capabilities, permissions, authority, risk classification, allowed/prohibited actions, escalation, evidence, evaluation criteria, termination. `make vet` checks the file is present and unmodified; deviations require an ADR. This is the minimum-viable AIMS for one autonomous agent.

**Concrete gap.** The worker declaration exists but there is no **AIMS governing body** above it — no role assignments for who approves changes to the declaration, no annual internal audit, no management-review meeting cadence, no continual-improvement log. A single capability declaration is not an AIMS; it is one capability registered against an AIMS that has yet to be built.

**Next step.** Add `docs/governance/aims/` with: (a) `policy.md` — the works-execution AI policy statement (signed by the founder), (b) `roles.md` — who approves capability additions/removals, (c) `review-cadence.md` — quarterly internal review and the change log. File path: `docs/governance/aims/policy.md`. Acceptance evidence: file is committed, references `docs/agents/worker.md`, and is linked from `docs/governance/aims/README.md`.

---

### 2.2 ISO/IEC 23894:2023 — AI Risk Management Guidance (`iso-iec-23894-2023`)

**Registry row:** `control_id=RISK-ASSESS`, `status=PARTIAL`, `owner=founder`.

**Applicability.** In-scope. Risk assessment is required for every AI capability the venture ships. The worker is the pilot capability.

**Current implementation (Slice 1+2).** `docs/agents/worker.md` §Risk classification declares:
- Function = `MANAGE` (autonomous action on leases).
- Risk level = `LIMITED` (provisional; EU AI Act tier is pending legal review — see row 2.6).
- Impact profile (subprocess blast radius bounded by lease scope, hermetic default, timeout).
- Failure modes (subprocess crash, lease loss, API unreachable) and escalation rules.

**Concrete gap.** Risk is classified but **not treated**. There is no risk-treatment register: no documented mitigations, no residual-risk acceptance, no monitoring, no review trigger. The declaration lists failure modes but does not say what control mitigates each one or how often it is re-evaluated.

**Next step.** Add a per-failure-mode treatment table at `docs/agents/worker.md` §Risk classification: each row = (failure mode, existing mitigation, residual risk, owner, next review date). File path: `docs/agents/worker.md` (extend in place; update the "Last reviewed" date). Acceptance evidence: section enumerates the four failure modes from §Risk classification with concrete mitigations tied to code paths in `internal/worker/worker.go`.

---

### 2.3 ISO/IEC 22989:2022 — AI Concepts and Terminology (`iso-iec-22989-2022`)

**Registry row:** `control_id=VOCAB`, `status=PLANNED`, `owner=founder`.

**Applicability.** In-scope as a foundation. All other AI standards assume a shared vocabulary. Without it, every AI capability declared later will re-define core terms.

**Current implementation (Slice 1+2).** None. The registry points at `docs/agents/glossary.md` but the file does not yet exist. Terminology is currently inline in `docs/agents/worker.md` (Agent ID, Trust class, Capability, Authority, Risk classification, Evidence).

**Concrete gap.** No central glossary; vocabulary drifts across documents; PR review has no single checklist to compare against.

**Next step.** Create `docs/agents/glossary.md` with the AI/agentic terms used across `docs/agents/worker.md` and this document: agent, capability, authority, risk level, lease, evidence, evaluation criterion. Add a one-line bullet to `.github/PULL_REQUEST_TEMPLATE.md` (or its works-execution equivalent) that flags any PR that introduces a new AI/agentic term. Acceptance evidence: `docs/agents/glossary.md` exists, ≥10 terms defined, referenced from `docs/agents/worker.md` §Identity.

---

### 2.4 NIST AI RMF 1.0 — Govern/Map/Measure/Manage (`nist-ai-rmf-1.0`)

**Registry row:** `control_id=AI-RMF-FOUR-FUNCTIONS`, `status=PARTIAL`, `owner=founder`.

**Applicability.** In-scope. RMF covers any AI system, including the autonomous (non-ML) worker. Its four functions are the natural structure for the worker declaration.

**Current implementation (Slice 1+2).** `docs/agents/worker.md` exercises all four functions:
- **GOVERN** → §Owner, §Authority, §Human approval requirements.
- **MAP** → §Identity, §Capabilities, §Tools, §Permissions.
- **MEASURE** → §Risk classification, §Evaluation criteria, §Evidence requirements.
- **MANAGE** → §Allowed actions, §Prohibited actions, §Escalation rules, §Termination conditions.

**Concrete gap.** Measurement is qualitative only. §Evaluation criteria lists four criteria (functional, reliability, safety, throughput) but none are run automatically today; `tests/agents/worker_test.go` is referenced in the registry as the test pointer but is not yet wired into CI as a gate. RMF Measure requires ongoing, repeatable measurement.

**Next step.** Add `tests/agents/worker_test.go` cases that assert each evaluation criterion: (1) functional — one-node work end-to-end → `SUCCEEDED`, (2) reliability — `kill -9` worker → reaper recovers within ≤30s (already exercised by `e2e_chaos`), (3) safety — subprocess filesystem scope test (slice 3 hermetic), (4) throughput — soak test gated on CI. File path: `tests/agents/worker_test.go`. Acceptance evidence: tests run under `make test`; reliability + functional cases are green on Slice 2 code.

---

### 2.5 MITRE ATLAS — Adversarial Threat Landscape for AI Systems (`mitre-atlas`)

**Registry row:** `control_id=ATLAS-THREAT`, `status=PARTIAL`, `owner=founder`.

**Applicability.** In-scope. ATLAS is LLM-centric, but its tactics extend to any autonomous agent: resource abuse, reconnaissance via legitimate capabilities, exfiltration via allowed tools, manipulation of the control plane. The Slice 2 worker is a legitimate-but-broad tool executor, which is the exact pattern ATLAS catalogs.

**Current implementation (Slice 1+2).** `docs/agents/worker.md` §Tools and §Permissions establish the baseline: outbound network is API-URL-only, filesystem is `ArtifactsDir`-only, no spawn-other-workers, no self-modification. These are the mitigation controls for the relevant ATLAS techniques.

**Concrete gap.** No per-tactic threat analysis. The mitigations exist in code but are not mapped to ATLAS technique IDs in a document the `judge` profile can review each slice. The registry note says `agents/worker.md reviewed by judge profile before each slice` — but there is no checklist to review against.

**Next step.** Add `docs/agents/atlas-threat-model.md`: a table mapping the worker's tools and permissions to relevant ATLAS techniques (e.g. AML.T0046 *Exfiltration via C2*, AML.T0052 *Screen Capture* → N/A, AML.T0009 *Harvest Information from Repositories* → mitigated by tenant-scoped API), with a residual-risk column. File path: `docs/agents/atlas-threat-model.md`. Acceptance evidence: ≥5 techniques mapped to a concrete code-path mitigation in `internal/worker/worker.go`.

---

### 2.6 EU AI Act (`eu-ai-act`)

**Registry row:** `control_id=EU-AI-ACT`, `status=BLOCKED`, `owner=founder`, `blocked_reason="Legal review required to classify works-execution under EU AI Act risk tiers."`, `unblock_check="Receipt of formal counsel opinion classifying the worker under the Act."`.

**Applicability.** In-scope by default (any autonomous AI system placed on the EU market), but the **risk-tier classification is unsettled** without external counsel.

**Current implementation (Slice 1+2).** The worker declaration is drafted assuming the worker is **not** a general-purpose AI system — it is a deterministic subprocess executor under a control-plane policy gate. `docs/agents/worker.md` §Risk classification states the EU AI Act tier is "likely out of scope or 'limited risk', pending legal review." This is a **provisional classification**; it is not authoritative.

**Concrete gap.** No formal classification. Until counsel opines, every other AI standard in this document rests on a tier assumption that has not been validated. If counsel classifies the worker as `HIGH RISK` or `LIMITED RISK`, the gap set changes materially (e.g. high-risk triggers Annex IV technical documentation, post-market monitoring, incident reporting, EU database registration).

**Next step.** **External dependency (required):** engage qualified legal counsel with EU AI Act expertise and obtain a written opinion classifying `works-worker` under the Act's risk tiers, plus any required downstream actions (database registration, conformity assessment, technical documentation per Annex IV if high-risk). **Internal reversible preparation:** open a kanban card at `docs/kanban/board.json` titled "EU AI Act classification — counsel engagement" with owner=founder, dependencies=[counsel opinion], and acceptance criteria = "written opinion received and filed at `docs/governance/eu-ai-act/opinion.pdf`; tier recorded in this row." Until the opinion is received, do **not** promote any PARTIAL row in this document to IMPLEMENTED on EU-AI-Act grounds. File path: `docs/governance/eu-ai-act/opinion.pdf` (to be created on receipt).

---

## §3. Deferred standards (NOT_APPLICABLE)

The following 10 rows are `NOT_APPLICABLE` for works-execution today because there are **no ML models and no LLM calls** in the V1 platform path (Slice 1–3). They are kept in the registry so that when `platform-ai-failure-intel` becomes production (planned slice 6+), the standards are not re-discovered from scratch.

Each deferred standard gets the same minimum entry: applicability (why it does not apply), trigger (what would change the status), next step (concrete file to create when triggered). Status in the registry is preserved; this document is the traceability anchor.

### 3.1 ISO/IEC 23053:2022 — Framework for AI Systems Using ML (`iso-iec-23053-2022`)

**Registry row:** `status=NOT_APPLICABLE`, `not_applicable_reason="No machine-learning models in the platform slice 1-3."`, `exceptions=["No ML models in slice 1-3. Will revisit at slice 6+ when AI-Assisted Failure Intelligence becomes production."]`.

- **Why not applicable:** the platform has no ML models. The worker is deterministic.
- **Trigger:** first ML model in the platform path (`platform-ai-failure-intel`, slice 6+).
- **Next step:** create `docs/agents/ml-system-lifecycle.md` when triggered, mapping the framework's components (data, model, deployment, operation, retirement) to the new model. File path: `docs/agents/ml-system-lifecycle.md`.

### 3.2 ISO/IEC 23053 Generative AI Amendment (`iso-iec-23053-genai-amendment`)

**Registry row:** `status=NOT_APPLICABLE`, `exceptions=["Bundled with iso-iec-23053-2022."]`.

- **Why not applicable:** no generative AI in the platform; the amendment only adds GenAI-specific clauses on top of 3.1.
- **Trigger:** any LLM/genAI call inside the platform boundary (today: none; planned: AI-Assisted Failure Intelligence).
- **Next step:** fold into `docs/agents/ml-system-lifecycle.md` (created per §3.1) at trigger time. File path: same — no separate file.

### 3.3 ISO/IEC 5259-1 — Data Quality for ML: Overview (`iso-iec-5259-1`)

**Registry row:** `status=NOT_APPLICABLE`, `exceptions=["Bundled with iso-iec-23053-2022."]`.

- **Why not applicable:** no ML → no data-quality-for-ML discipline needed.
- **Trigger:** first ML training dataset enters the platform.
- **Next step:** add a `data-quality.md` section to `docs/agents/ml-system-lifecycle.md` at trigger time. File path: `docs/agents/ml-system-lifecycle.md` (single document).

### 3.4 ISO/IEC 5259-2 — Data Quality for ML: Methods (`iso-iec-5259-2`)

**Registry row:** `status=NOT_APPLICABLE`.

- **Why not applicable:** same as §3.3.
- **Trigger:** first ML model.
- **Next step:** add a `data-quality-methods.md` appendix to `docs/agents/ml-system-lifecycle.md`. File path: `docs/agents/ml-system-lifecycle.md`.

### 3.5 ISO/IEC 5259-3 — Data Quality for ML: Framework (`iso-iec-5259-3`)

**Registry row:** `status=NOT_APPLICABLE`.

- **Why not applicable:** same as §3.3.
- **Trigger:** first ML model.
- **Next step:** add a `data-quality-framework.md` appendix to `docs/agents/ml-system-lifecycle.md`. File path: `docs/agents/ml-system-lifecycle.md`.

### 3.6 ISO/IEC 5259-4 — Data Quality for ML: Process (`iso-iec-5259-4`)

**Registry row:** `status=NOT_APPLICABLE`.

- **Why not applicable:** same as §3.3.
- **Trigger:** first ML model.
- **Next step:** add a `data-quality-process.md` appendix to `docs/agents/ml-system-lifecycle.md`. File path: `docs/agents/ml-system-lifecycle.md`.

### 3.7 ISO/IEC 5259-5 — Data Quality for ML: Attribute Taxonomy (`iso-iec-5259-5`)

**Registry row:** `status=NOT_APPLICABLE`.

- **Why not applicable:** same as §3.3.
- **Trigger:** first ML model with a labeled dataset.
- **Next step:** add an `attribute-taxonomy.md` appendix to `docs/agents/ml-system-lifecycle.md`. File path: `docs/agents/ml-system-lifecycle.md`.

### 3.8 ISO/IEC TR 5259-6:2026 — Data Quality for ML: Guidance (`iso-iec-tr-5259-6-2026`)

**Registry row:** `status=NOT_APPLICABLE`.

- **Why not applicable:** technical report on guidance for §3.3–§3.7. None of the underlying parts apply, so the guidance does not apply either.
- **Trigger:** any of §3.3–§3.7 triggering.
- **Next step:** cross-reference only — link TR 5259-6 from `docs/agents/ml-system-lifecycle.md` once created. File path: `docs/agents/ml-system-lifecycle.md`.

### 3.9 NIST AI 600-1 — Generative AI Profile (`nist-ai-600-1`)

**Registry row:** `status=NOT_APPLICABLE`.

- **Why not applicable:** the profile is the AI RMF 1.0 applied specifically to generative AI. There is no generative AI in the platform. Note that this does **not** affect the underlying `nist-ai-rmf-1.0` row (§2.4), which applies to the worker regardless.
- **Trigger:** first generative AI component.
- **Next step:** create `docs/agents/genai-profile.md` at trigger time, mapping AI 600-1's GenAI-specific Govern/Map/Measure/Manage actions to the platform. File path: `docs/agents/genai-profile.md`.

### 3.10 OWASP Top 10 for LLM Applications 2026 (`owasp-genai-llm-top10-2026`)

**Registry row:** `status=NOT_APPLICABLE`, `exceptions=["LLM top 10 becomes applicable only when AI-Assisted Failure Intelligence is wired in. Will revisit then."]`.

- **Why not applicable:** the platform makes no LLM calls. The LLM Top 10 (prompt injection, sensitive information disclosure, supply chain, data poisoning, etc.) is irrelevant until there is an LLM surface.
- **Trigger:** first LLM call in the platform path (`platform-ai-failure-intel`, slice 6+).
- **Next step:** create `docs/agents/llm-top10.md` at trigger time, with one row per Top 10 risk: (risk, exposure in our use case, mitigation, residual). File path: `docs/agents/llm-top10.md`.

---

## §4. Traceability — every standard_id back to the registry

This is the audit trail linking every `standard_id` in this document to its row in `docs/standards/registry.json` and to the evidence path the registry declares.

| #  | standard_id                       | Registry status   | Evidence path in registry  | This-document anchor |
|----|-----------------------------------|-------------------|------------------------------|----------------------|
| 1  | `iso-iec-42001-2023`              | PARTIAL           | `docs/agents/worker.md`       | §2.1                 |
| 2  | `iso-iec-23894-2023`              | PARTIAL           | `docs/agents/worker.md`       | §2.2                 |
| 3  | `iso-iec-22989-2022`              | PLANNED           | `docs/agents/glossary.md`     | §2.3                 |
| 4  | `iso-iec-23053-2022`              | NOT_APPLICABLE    | n/a (deferred)                | §3.1                 |
| 5  | `iso-iec-23053-genai-amendment`   | NOT_APPLICABLE    | n/a (bundled with 4)          | §3.2                 |
| 6  | `iso-iec-5259-1`                  | NOT_APPLICABLE    | n/a (bundled with 4)          | §3.3                 |
| 7  | `iso-iec-5259-2`                  | NOT_APPLICABLE    | n/a (deferred)                | §3.4                 |
| 8  | `iso-iec-5259-3`                  | NOT_APPLICABLE    | n/a (deferred)                | §3.5                 |
| 9  | `iso-iec-5259-4`                  | NOT_APPLICABLE    | n/a (deferred)                | §3.6                 |
| 10 | `iso-iec-5259-5`                  | NOT_APPLICABLE    | n/a (deferred)                | §3.7                 |
| 11 | `iso-iec-tr-5259-6-2026`          | NOT_APPLICABLE    | n/a (deferred)                | §3.8                 |
| 12 | `nist-ai-rmf-1.0`                 | PARTIAL           | `docs/agents/worker.md`       | §2.4                 |
| 13 | `nist-ai-600-1`                   | NOT_APPLICABLE    | n/a (deferred)                | §3.9                 |
| 14 | `owasp-genai-llm-top10-2026`      | NOT_APPLICABLE    | n/a (deferred)                | §3.10                |
| 15 | `mitre-atlas`                     | PARTIAL           | `docs/agents/worker.md`       | §2.5                 |
| 16 | `eu-ai-act`                       | BLOCKED           | n/a (awaiting counsel)        | §2.6                 |

**Status promotion rules.** A standard's row in the registry may only be promoted from `PARTIAL` → `IMPLEMENTED` → `VERIFIED` when:
1. The next-step file listed in this document is merged and committed.
2. The acceptance-evidence clause for that step is satisfied and reproducible from the committed state.
3. The `eu-ai-act` row (§2.6) remains `BLOCKED` until the external dependency (counsel opinion) is satisfied. No PARTIAL row in §2 may be promoted to IMPLEMENTED if doing so would implicitly assert EU AI Act compliance before counsel has classified the worker.

---

## §5. Change log

- 2026-08-31 — Initial mapping. Slice 2 worker declaration (`docs/agents/worker.md`) is the only governance anchor; ML/LLM standards are deferred per registry.