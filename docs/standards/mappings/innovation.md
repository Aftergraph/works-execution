# Innovation Management — Per-Standard Mapping

> **Scope.** This document maps the 8 user-mandated innovation management
> standards declared in `docs/standards/registry.json` (domain = `innovation`)
> to the works-execution system. The 8 standards are the **ISO 56000 family**
> (ISO 56000:2020 through ISO 56008:2024) plus a ninth cross-cutting row,
> the **Continuous Innovation Feedback Standard** (`platform-continuous-innovation`,
> `control_id = INNOVATION-FEEDBACK`), which lives in the `platform` domain
> but defines the lifecycle every ISO 5600x requirement ultimately flows
> through. For each standard we record: applicability, current status
> (sourced from `registry.json`), gap, concrete next step with file path,
> and a traceability table back to the registry row, slice deliverables, and
> the platform-continuous-innovation lifecycle stage.

> **Method.** The §14 implementation rule from the user-mandated standards
> charter is applied uniformly:
> 1. determine applicability,
> 2. map to system requirements,
> 3. identify gaps,
> 4. prioritize by risk and leverage,
> 5. recommend the highest-value actionable gap with a concrete file path.

> **Authoritative sources for status.**
> `docs/standards/registry.json` (130 rows; 8 in the `innovation` domain, plus
> the `INNOVATION-FEEDBACK` row in `platform`),
> `docs/works-venture-starter-pack/` (venture plan),
> `services/api/api.go` (HTTP API), `services/work/store/store.go`
> (SQLite), `internal/worker/worker.go` (subprocess executor),
> `docs/kanban/board.json` (Slice 3 tracker).

> **Vocabulary binding (§14, ISO 56008:2024).** This document uses the
> ISO 56008:2024 vocabulary throughout: *innovation* is the production of
> a new or significantly changed product, process, marketing method, or
> organisational method; *innovation unit* is any artefact that can carry
> innovation state (a work item, a hypothesis, an experiment, a decision);
> *innovation portfolio* is the set of all live innovation units tracked
> by the platform. The Continuous Innovation Feedback Standard
> (`INNOVATION-FEEDBACK`) defines 9 lifecycle stages that every innovation
> unit traverses: **Signal → Opportunity → Hypothesis → Experiment →
> Evidence → Decision → Implementation → Measurement → Learning**.

---

## Summary table

| # | Standard | Registry row | Status (today) | Risk/Leverage | Top next step |
|---|---|---|---|---|---|
| 1 | ISO 56000:2020 — Overview & vocabulary | `iso-56000` | PLANNED | High leverage (parent framework) | Author `docs/standards/innovation/lifecycle.md` cross-linking to `INNOVATION-FEEDBACK` |
| 2 | ISO 56001:2024 — Innovation Management System | `iso-56001-2024` | PLANNED | Medium leverage (IMS normative) | Bundle evidence pointer into §1 lifecycle doc |
| 3 | ISO 56002:2019 — IMS guidance | `iso-56002-2019` | PLANNED | Medium leverage (implementation playbook) | Bundle guidance into §1 lifecycle doc |
| 4 | ISO 56003:2019 — Innovation tools | `iso-56003-2019` | PLANNED | High leverage (portfolio + opportunity) | Add `internal/innovation/opportunity.go` (opportunity state machine) |
| 5 | ISO 56005:2020 — Tools & methods (IP) | `iso-56005-2020` | PLANNED | Low leverage (IP not in V1 scope) | NOT_APPLICABLE — see §5 |
| 6 | ISO 56006:2021 — Tools & methods (continuation) | `iso-56006-2021` | PLANNED | Medium leverage (strategic intelligence) | Add `docs/standards/innovation/intelligence.md` |
| 7 | ISO 56007:2023 — Innovation assessment | `iso-56007-2023` | PLANNED | High leverage (measurement + learning) | Add `internal/innovation/measurement.go` |
| 8 | ISO 56008:2024 — Vocabulary | `iso-56008-2024` | PLANNED | Foundational (every other row depends on it) | Author `docs/standards/innovation/glossary.md` |
| 9 | Continuous Innovation Feedback Standard (cross-link) | `platform-continuous-innovation` | PLANNED | Highest leverage (9-stage lifecycle) | Author `docs/standards/innovation/lifecycle.md` (anchors §1-§8) |

**Excluded / N/A:**

- `iso-56005-2020` is **NOT_APPLICABLE** for V1. The standard covers IP
  management and strategic IP guidance. Works-execution V1 owns no IP
  assets and does not act as an IP management system; the venture plan
  does not name IP as a V1 deliverable. See §5 for the full reasoning and
  the trigger that will move the row to PLANNED-on-IP.

**Bundled relationships** (one implementation, multiple standards):

- §1 (56000 overview) and §9 (`INNOVATION-FEEDBACK`) share the same
  `docs/standards/innovation/lifecycle.md` deliverable.
- §2 (56001 IMS) and §3 (56002 guidance) bundle their evidence pointers
  into §1's lifecycle doc and the `internal/innovation/` Go package.
- §4 (56003 tools) and §7 (56007 assessment) share the
  `internal/innovation/` package: opportunity → hypothesis →
  experiment → evidence → measurement.
- §6 (56006 intelligence) is a doc-only deliverable that lives in
  `docs/standards/innovation/intelligence.md` and consumes lifecycle
  events from `internal/innovation/`.
- §8 (56008 vocabulary) is a glossary doc that all other §1-§7 sections
  link to from their first paragraph.

---

## §14 Implementation Rule (binding)

Every standard in this document is processed through the five-step rule
from the user-mandated standards charter:

1. **Determine applicability** — is this standard in-scope for
   works-execution V1? (One row, ISO 56005, is N/A for V1 with a stated
   reason.)
2. **Map to system requirements** — which concrete component, contract,
   or test enforces it? (Each row points to either a `services/`,
   `internal/`, `docs/`, or `tests/` path.)
3. **Identify gaps** — what is missing today (Slice 1 + Slice 2)?
4. **Prioritize by risk and leverage** — score each gap on
   (risk-of-omission × leverage-on-platform-correctness).
5. **Recommend highest-value actionable gap with file path** — the next
   concrete change, where it lands, and the acceptance evidence.

The §14.5 next step for every row in this document is a **single,
file-pathed change** that the founder can act on in one sitting. Rows that
are bundled (e.g. §2 + §3) share the same file path; rows that are
foundational (§8 vocabulary) are listed first in any doc that cites them.

---

## §1. ISO 56000:2020 — Innovation Management (Overview & vocabulary)

- **Standard.** `iso-56000` — overview of the ISO 56000 family; introduces
  the unifying concepts (innovation, innovation unit, innovation
  portfolio, IMS, innovation system) that every other row in this
  document inherits.
- **Registry row.** `standard_id == "iso-56000"`, `control_id == "ISO-56000"`,
  `status == "PLANNED"`, `implementation == "Slice 3: innovation lifecycle
  in docs/standards/innovation/."`, `owner == "founder"`.

**Requirement (registry):** "Innovation management framework."

**Applicability (§14.1):** **In-scope, highest leverage.**
Works-execution is itself an **innovation management system** at the
venture level: it tracks the 9-stage lifecycle
(`Signal → Opportunity → Hypothesis → Experiment → Evidence → Decision →
Implementation → Measurement → Learning`) through the
`platform-continuous-innovation` standard, and it routes every new idea
through the same control plane that routes work. ISO 56000:2020 is the
parent framework that names the vocabulary and structure this platform
already implements; adopting it explicitly turns an implicit pattern
into an auditable framework. Without §1, §2-§8 have no shared frame of
reference and the lifecycle stages drift in definition across docs.

**Current status (registry):** `PLANNED`. Registry row
`implementation` reads: *`"Slice 3: innovation lifecycle in
docs/standards/innovation/."`* `enforcement_point: null`, `test: null`,
`evidence: null`. No `docs/standards/innovation/` directory exists yet
on disk (`ls docs/standards/innovation/` → not found).

**Gap (§14.3):**

1. No `docs/standards/innovation/` directory; no overview document
   linking the 9 lifecycle stages to the ISO 56000 family.
2. No mapping from ISO 56000 clauses (4 Context, 5 Leadership, 6
   Planning, 7 Support, 8 Operation, 9 Performance evaluation, 10
   Improvement) to works-execution control-plane primitives.
3. No cross-reference between ISO 56000's *innovation portfolio*
   concept and the existing `services/work/store/` (the work store is
   the closest analogue but treats work items, not opportunity units).
4. No shared vocabulary doc — §8 (ISO 56008) and §1 (ISO 56000) both
   depend on it, and neither is present.

**Next step (§14.5):** Create `docs/standards/innovation/lifecycle.md`
(new) with: (a) the 9-stage innovation lifecycle from
`platform-continuous-innovation`, (b) a clause-by-clause table mapping
ISO 56000 §4-§10 to works-execution components (`services/api/api.go`,
`services/work/store/`, `internal/worker/`, `docs/kanban/board.json`),
and (c) explicit cross-links to §2, §3, §4, §6, §7, §8 in this document.
File path: `docs/standards/innovation/lifecycle.md` (new).

**Risk / leverage.** Highest leverage — the lifecycle doc is the
*anchor* every other ISO 5600x row references. Without it, §2-§8 each
have to re-derive the lifecycle locally, which is how vocabulary drift
starts. Risk of omission is low today (Slice 1+2 has no innovation
unit type at all) but grows monotonically with each new feature that
touches a lifecycle stage.

### Traceability — ISO 56000:2020

| ISO 56000 clause | System element | File | Owner | Status |
|---|---|---|---|---|
| §4 Context of the organization | Venture plan + scope | `docs/works-venture-starter-pack/`, `docs/standards/registry.json` | founder | PARTIAL |
| §5 Leadership | `AGENTS.md`, governance docs | `AGENTS.md`, `docs/runbooks/merge-governance.md` | founder | PARTIAL |
| §6 Planning | Slice roadmap | `docs/kanban/board.json` (Slice 3 row) | founder | PARTIAL |
| §7 Support | RFC + ADRs | `docs/rfcs/`, `docs/adr/` | founder | PARTIAL |
| §8 Operation (the 9-stage lifecycle) | Continuous Innovation Feedback | `platform-continuous-innovation` row | founder | PLANNED |
| §9 Performance evaluation | Measurement row | §7 (`iso-56007-2023`) | founder | PLANNED |
| §10 Improvement | Learning row | §7 + `INNOVATION-FEEDBACK` Learning stage | founder | PLANNED |
| Lifecycle overview doc | New | `docs/standards/innovation/lifecycle.md` (new) | founder | PLANNED |
| Clause-to-component map | New | inside `lifecycle.md` | founder | PLANNED |

---

## §2. ISO 56001:2024 — Innovation Management System (Requirements)

- **Standard.** `iso-56001-2024` — the first formally normative ISO
  innovation management system standard (published 2024). Specifies
  requirements for establishing, implementing, maintaining, and
  continually improving an innovation management system.
- **Registry row.** `standard_id == "iso-56001-2024"`, `control_id ==
  "ISO-56001"`, `status == "PLANNED"`, `implementation == "Bundled with
  iso-56000."`, `owner == "founder"`.

**Requirement (registry):** "IMS requirements."

**Applicability (§14.1):** **In-scope, bundled with §1.**
Works-execution already operates as an innovation management system at
the venture level: every work item carries a state machine
(`PENDING → RUNNING → SUCCEEDED | FAILED | DEAD_LETTERED`), evidence
is required to transition to `SUCCEEDED`, and the
`platform-continuous-innovation` standard defines a 9-stage lifecycle
that the system can audit. ISO 56001:2024 adds the **normative**
framing: documented scope, leadership commitment, planning, support,
operation, performance evaluation, improvement. The 8 clauses map
one-to-one to ISO 56000 §4-§10, so the §1 lifecycle doc is also the §2
IMS requirements evidence pointer.

**Current status (registry):** `PLANNED`. `implementation: "Bundled
with iso-56000."` No independent `enforcement_point`, `test`, or
`evidence` — the registry intentionally defers to the §1 deliverable.

**Gap (§14.3):**

1. No IMS scope statement — the venture plan exists
   (`docs/works-venture-starter-pack/`) but does not say "this venture
   is an IMS conforming to ISO 56001:2024."
2. No leadership-commitment evidence pointer — the founder is the sole
   accountable party; this should be recorded explicitly so that audit
   has something to read.
3. No documented IMS risks-and-opportunities register (ISO 56001 §6.1).
   The threat model (`THREAT_MODEL.md`) is the closest analogue but is
   scoped to security, not innovation risk.
4. No IMS audit schedule — when does this IMS get reviewed? Quarterly?
   Per-slice? Per major version?

**Next step (§14.5):** Add an "ISO 56001:2024 IMS scope" section to
`docs/standards/innovation/lifecycle.md` (new, per §1) covering scope,
leadership commitment, risks-and-opportunities register, and audit
cadence. The lifecycle doc is the single evidence pointer; do not
create a separate IMS doc. File path: `docs/standards/innovation/lifecycle.md`
(new) — extend the §1 deliverable with a §2 sub-section.

**Risk / leverage.** Medium leverage — §2 is bundled with §1, so the
incremental work is one section in the same doc. Low risk of omission
in V1 (certification is not on the roadmap); the value is a
defensible audit artefact if a future customer asks "is this an
ISO 56001 IMS?".

### Traceability — ISO 56001:2024

| ISO 56001 clause | System element | File | Owner | Status |
|---|---|---|---|---|
| §4 Context | §1 lifecycle doc §4 mapping | `docs/standards/innovation/lifecycle.md` (new) | founder | PLANNED |
| §5 Leadership | Founder as accountable party | `AGENTS.md`, `lifecycle.md` §5 sub-section (new) | founder | PARTIAL |
| §6.1 Risks & opportunities | (new) Innovation risk register | `lifecycle.md` §6 sub-section (new) | founder | PLANNED |
| §6.2 Innovation objectives | Slice roadmap | `docs/kanban/board.json` | founder | PARTIAL |
| §7 Support | RFC + ADR process | `docs/rfcs/`, `docs/adr/` | founder | PARTIAL |
| §8 Operation (IMS operation) | §1 lifecycle | `lifecycle.md` (new) | founder | PLANNED |
| §9 Performance evaluation | §7 (ISO 56007) | see §7 | founder | PLANNED |
| §10 Improvement | §7 + Learning stage | see §7 | founder | PLANNED |
| IMS scope statement | New (in lifecycle doc) | `lifecycle.md` §2 sub-section (new) | founder | PLANNED |

---

## §3. ISO 56002:2019 — Innovation Management (Guidance)

- **Standard.** `iso-56002-2019` — guidance on establishing, implementing,
  maintaining, and continually improving an innovation management system.
  Pre-dates ISO 56001:2024 by 5 years; the two standards share the same
  clause structure (4 Context, 5 Leadership, 6 Planning, 7 Support, 8
  Operation, 9 Performance evaluation, 10 Improvement). ISO 56002:2019 is
  non-normative (guidance); ISO 56001:2024 is normative (requirements).
- **Registry row.** `standard_id == "iso-56002-2019"`, `control_id ==
  "ISO-56002"`, `status == "PLANNED"`, `implementation == "Bundled."`,
  `owner == "founder"`.

**Requirement (registry):** "IMS guidance."

**Applicability (§14.1):** **In-scope, bundled with §1 + §2.**
ISO 56002:2019 is the *guidance* companion to ISO 56001:2024. The
founder can use ISO 56002:2019 to *explain* what ISO 56001:2024 *requires*.
Bundling the two is correct because the underlying IMS does not change
shape when the framing switches from "requirements" to "guidance."

**Current status (registry):** `PLANNED`. `implementation: "Bundled."`
No independent deliverable; shares §1's lifecycle doc.

**Gap (§14.3):**

1. No guidance-to-requirements crosswalk — the §1 lifecycle doc needs
   a column that flags which sub-clauses are normative (ISO 56001:2024)
   vs. guidance (ISO 56002:2019) so future readers do not treat
   guidance as requirement.
2. No documented "how to operate the IMS" section (ISO 56002 §8
   operational guidance is the most operationally useful part — it
   covers portfolio, culture, structure, tools).

**Next step (§14.5):** Extend `docs/standards/innovation/lifecycle.md`
(new, per §1) with a "Guidance vs. requirements" column on the
clause-mapping table and a §3 sub-section linking each ISO 56002 §8
operational-guidance sub-clause to a works-execution primitive
(portfolio → `services/work/store/`, culture → `AGENTS.md`, structure
→ `docs/runbooks/`, tools → `internal/innovation/`). File path:
`docs/standards/innovation/lifecycle.md` (new) — extend §1's doc with
a §3 sub-section.

**Risk / leverage.** Medium leverage — same doc as §1 and §2, so cost
is incremental (a column + a sub-section). Risk of omission is low;
the value is making the IMS self-documenting for new contributors.

### Traceability — ISO 56002:2019

| ISO 56002 clause | System element | File | Owner | Status |
|---|---|---|---|---|
| §4 Context (guidance) | §1 lifecycle doc §4 | `docs/standards/innovation/lifecycle.md` (new) | founder | PLANNED |
| §5 Leadership (guidance) | `AGENTS.md` | `AGENTS.md` | founder | PARTIAL |
| §6 Planning (guidance) | Slice roadmap + kanban | `docs/kanban/board.json` | founder | PARTIAL |
| §7 Support (guidance) | RFC + ADR | `docs/rfcs/`, `docs/adr/` | founder | PARTIAL |
| §8.1 Operating the IMS | §4 (ISO 56003 tools) | see §4 | founder | PLANNED |
| §8.2 Portfolio management | §4 + §7 | see §4, §7 | founder | PLANNED |
| §8.3 Innovation culture | `AGENTS.md`, `CONTRIBUTING.md` | `AGENTS.md` | founder | PARTIAL |
| §8.4 Innovation structure | `docs/runbooks/`, RFCs | `docs/runbooks/` | founder | PARTIAL |
| §8.5 Innovation tools | §4 + §7 | see §4, §7 | founder | PLANNED |
| §9 Performance evaluation (guidance) | §7 (ISO 56007) | see §7 | founder | PLANNED |
| §10 Improvement (guidance) | §7 + Learning stage | see §7 | founder | PLANNED |
| Guidance-to-requirements column | New | `lifecycle.md` clause table (new) | founder | PLANNED |

---

## §4. ISO 56003:2019 — Innovation Management (Tools and methods, Part 1)

- **Standard.** `iso-56003-2019` — Part 1 of the ISO 56005/56006 tools
  family. Covers the **innovation tools landscape**: portfolio
  management, idea management, opportunity management, concept
  development, experimentation, and implementation. Each tool has its
  own inputs, outputs, decision points, and quality criteria.
- **Registry row.** `standard_id == "iso-56003-2019"`, `control_id ==
  "ISO-56003"`, `status == "PLANNED"`, `implementation == "Bundled."`,
  `owner == "founder"`.

**Requirement (registry):** "Innovation tools."

**Applicability (§14.1):** **In-scope, high leverage.** This is the
most operationally useful of the ISO 5600x standards for works-execution
because the standard names the *tools* (portfolio, opportunity,
hypothesis, experiment, evidence) that map directly to the
`platform-continuous-innovation` lifecycle stages 1-5
(Signal → Opportunity → Hypothesis → Experiment → Evidence). The
innovation *opportunity* is a first-class object that the V1 system
does not yet have — opportunity is a richer object than the existing
`Work` primitive, and adding it is the natural extension of the V1 work
store.

**Current status (registry):** `PLANNED`. `implementation: "Bundled."`
No independent deliverable; will land in the `internal/innovation/`
package and in `docs/standards/innovation/lifecycle.md`.

**Gap (§14.3):**

1. No `internal/innovation/` Go package exists.
2. No `Opportunity` type. The current store (`services/work/store/store.go`)
   has `Work` with a state machine but no concept of a *pre-work*
   opportunity, hypothesis, or experiment.
3. No portfolio concept — the `services/work/store/` `ListWorks`
   function is the closest analogue but has no grouping (by tenant, by
   theme, by risk class).
4. No "innovation tool" surface in the API (`services/api/api.go` has
   only work endpoints).

**Next step (§14.5):** Create `internal/innovation/opportunity.go` (new)
defining the `Opportunity` struct and its state machine
(`Signal → Opportunity → Hypothesis → Experiment → Evidence →
Decision → Implementation → Measurement → Learning`), with a
constructor that maps directly to the 9 lifecycle stages and a
`Status()` method that returns the current stage. Add a sibling
`internal/innovation/opportunity_test.go` (new) with a table-driven
test covering all 9 transitions + invalid-transition rejection. The
`Work` primitive in `services/work/store/store.go` is *not* modified in
this step; the opportunity object is a sibling type that the work
store will reference in a later slice. File path:
`internal/innovation/opportunity.go` (new) and
`internal/innovation/opportunity_test.go` (new).

**Risk / leverage.** High leverage — opportunity is the foundational
type the entire ISO 5600x mapping sits on. The cost is one Go file +
one test file. Risk of omission grows sharply once the API starts
exposing innovation endpoints (it cannot, because the type does not
exist).

### Traceability — ISO 56003:2019

| ISO 56003 tool | System element | File | Owner | Status |
|---|---|---|---|---|
| §6 Innovation portfolio | Future `services/innovation/` (Slice 4+) | TBD | founder | PLANNED |
| §7 Idea management | `internal/innovation/opportunity.go` Signal stage | `internal/innovation/opportunity.go` (new) | founder | PLANNED |
| §8 Opportunity management | `internal/innovation/opportunity.go` Opportunity stage | `internal/innovation/opportunity.go` (new) | founder | PLANNED |
| §9 Concept development | `internal/innovation/opportunity.go` Hypothesis stage | `internal/innovation/opportunity.go` (new) | founder | PLANNED |
| §10 Experimentation | `internal/innovation/opportunity.go` Experiment stage | `internal/innovation/opportunity.go` (new) | founder | PLANNED |
| §11 Implementation | (cross-link) `services/work/` (Work is the implementation carrier) | `services/work/store/store.go` | founder | PARTIAL |
| §12 Assessment | §7 (ISO 56007) | see §7 | founder | PLANNED |
| Opportunity state machine | New | `internal/innovation/opportunity.go` (new) | founder | PLANNED |
| Opportunity transitions test | New | `internal/innovation/opportunity_test.go` (new) | founder | PLANNED |

---

## §5. ISO 56005:2020 — Innovation Management (Tools and methods, Part 2 — IP)

- **Standard.** `iso-56005-2020` — Part 2 of the tools family. Covers
  **intellectual property (IP) management for innovation**: IP
  identification, IP protection strategies, IP risk management, IP
  portfolio management, and IP exploitation.
- **Registry row.** `standard_id == "iso-56005-2020"`, `control_id ==
  "ISO-56005"`, `status == "PLANNED"`, `implementation == "Bundled."`,
  `owner == "founder"`.

**Requirement (registry):** "Tools and methods."

**Applicability (§14.1):** **NOT_APPLICABLE for V1.** The venture plan
(`docs/works-venture-starter-pack/`) does not name IP management as a
V1 deliverable. Works-execution V1 owns no IP assets, does not act as
an IP management system, and does not provide IP-protection services
to customers. Adding IP-management machinery to the V1 control plane
would be premature optimization and would violate the Slice-discipline
rule (one deliverable per slice). Trigger to revisit: the venture
launches an IP-portfolio product, a customer demands an IP-asset
register, or the venture files its first patent. The row is **not**
deleted from the registry because IP management *will* matter to the
venture eventually; the status remains `PLANNED` so the row is not
silently dropped.

**Current status (registry):** `PLANNED`. `implementation: "Bundled."`
The registry keeps the row because IP management will matter to the
venture eventually; this document's recommendation is to flip the
status to `NOT_APPLICABLE` with a `not_applicable_reason` field
recording the V1 scope and the trigger to revisit.

**Gap (§14.3):**

1. No `not_applicable_reason` field in the registry row, even though
   the row is functionally N/A for V1.
2. No `docs/standards/innovation/ip-deferral.md` recording the V1
   scope and the trigger to revisit.
3. No cross-link to `docs/legal/` (the V1 venture plan has no legal
   section, but IP would land there when it lands).

**Next step (§14.5):** Two changes:
1. Update `docs/standards/registry.json` to flip the
   `iso-56005-2020` row's `status` from `PLANNED` to
   `NOT_APPLICABLE` and add a `not_applicable_reason` field:
   *"Works-execution V1 owns no IP assets and does not act as an IP
   management system. Re-evaluate when the venture launches an
   IP-portfolio product, a customer demands an IP-asset register, or
   the venture files its first patent."*
2. Add a sibling `docs/standards/innovation/ip-deferral.md` (new)
   recording the V1 scope, the trigger, and the would-be IPMS
   evidence pointers so the next person who looks at this row can
   re-evaluate the decision without re-deriving it.
File path: `docs/standards/registry.json` (modify one row) and
`docs/standards/innovation/ip-deferral.md` (new).

**Risk / leverage.** Low leverage — IP is not in V1 scope. Risk of
omission is low. The single value of this §5 next step is preventing
the row from drifting (a `PLANNED` row nobody acts on for a year
becomes a registry-cleanup ticket later; an explicit
`NOT_APPLICABLE` row is self-explaining).

### Traceability — ISO 56005:2020

| ISO 56005 clause | System element | File | Owner | Status |
|---|---|---|---|---|
| §6 IP identification | (deferred) | `docs/standards/innovation/ip-deferral.md` (new) | founder | N/A (V1) |
| §7 IP protection strategy | (deferred) | `ip-deferral.md` (new) | founder | N/A (V1) |
| §8 IP risk management | (deferred) | `ip-deferral.md` (new) | founder | N/A (V1) |
| §9 IP portfolio management | (deferred) | `ip-deferral.md` (new) | founder | N/A (V1) |
| §10 IP exploitation | (deferred) | `ip-deferral.md` (new) | founder | N/A (V1) |
| V1 deferral decision | New | `docs/standards/innovation/ip-deferral.md` (new) | founder | PLANNED |
| Registry row status flip | Modified | `docs/standards/registry.json` (one row) | founder | PLANNED |
| Re-evaluation trigger | New | `ip-deferral.md` (new) | founder | PLANNED |

---

## §6. ISO 56006:2021 — Innovation Management (Tools and methods, Part 3)

- **Standard.** `iso-56006-2021` — Part 3 of the tools family. Covers
  **strategic intelligence** for innovation: technology scouting,
  competitor monitoring, market trend analysis, IP landscape, and
  innovation-partner identification.
- **Registry row.** `standard_id == "iso-56006-2021"`, `control_id ==
  "ISO-56006"`, `status == "PLANNED"`, `implementation == "Bundled."`,
  `owner == "founder"`.

**Requirement (registry):** "Tools continuation."

**Applicability (§14.1):** **In-scope, medium leverage.** Strategic
intelligence is a doc-only deliverable in V1: the venture does not
need a tool to *capture* strategic intelligence in V1, but it does
need a **documented methodology** so that when the first customer
asks "how do you decide what to build next?", the answer is recorded
and the methodology is reproducible. The 5 intelligence sources
(technology scouting, competitor monitoring, market trends, IP
landscape, partner identification) map to the 9 lifecycle stages
1-2 (Signal, Opportunity) and feed §4's opportunity object.

**Current status (registry):** `PLANNED`. `implementation: "Bundled."`
Will land in `docs/standards/innovation/intelligence.md`.

**Gap (§14.3):**

1. No `docs/standards/innovation/` directory; no intelligence
   methodology doc.
2. No documented Signal-capture process — the
   `platform-continuous-innovation` row says "Signal" but does not
   define what counts as a Signal or how Signals are captured.
3. No link from strategic intelligence output to the §4 opportunity
   object (Signals are the input to opportunity creation).

**Next step (§14.5):** Create `docs/standards/innovation/intelligence.md`
(new) with: (a) the 5 intelligence sources from ISO 56006:2021 §6-§10,
(b) the 9-stage lifecycle stages 1-2 (Signal, Opportunity) annotated
with which intelligence source feeds each, (c) a worked example
mapping the Slice 3 roadmap back to the 5 sources (technology: the
Go 1.23 features we are using; competitor: existing work-orchestrators
like Temporal, Prefect, Inngest; market: open-core control planes;
IP: not applicable per §5; partners: none in V1), and (d) a link
to §4's `internal/innovation/opportunity.go` for the downstream type.
File path: `docs/standards/innovation/intelligence.md` (new).

**Risk / leverage.** Medium leverage — strategic intelligence is a
*content* deliverable, not a *code* deliverable, so the cost is one
doc. Risk of omission is low (the venture can survive V1 without a
recorded methodology) but the value is the same as for any
documented process: it makes the decision auditable.

### Traceability — ISO 56006:2021

| ISO 56006 source | System element | File | Owner | Status |
|---|---|---|---|---|
| §6 Technology scouting | Lifecycle Signal stage 1 | `docs/standards/innovation/intelligence.md` (new) | founder | PLANNED |
| §7 Competitor monitoring | Lifecycle Signal stage 1 | `intelligence.md` (new) | founder | PLANNED |
| §8 Market trend analysis | Lifecycle Signal stage 1 | `intelligence.md` (new) | founder | PLANNED |
| §9 IP landscape | (N/A per §5) | `ip-deferral.md` (new, see §5) | founder | N/A (V1) |
| §10 Partner identification | Lifecycle Signal stage 1 | `intelligence.md` (new) | founder | PLANNED |
| Signal → Opportunity handoff | Downstream to §4 | `internal/innovation/opportunity.go` (new, see §4) | founder | PLANNED |
| Worked example (Slice 3) | New | `intelligence.md` worked example (new) | founder | PLANNED |

---

## §7. ISO 56007:2023 — Innovation Management (Assessment)

- **Standard.** `iso-56007-2023` — covers **innovation assessment**:
  how to measure the effectiveness of an innovation management system,
  how to assess the maturity of an organization's innovation capability,
  and how to evaluate individual innovation projects.
- **Registry row.** `standard_id == "iso-56007-2023"`, `control_id ==
  "ISO-56007"`, `status == "PLANNED"`, `implementation == "Bundled."`,
  `owner == "founder"`.

**Requirement (registry):** "Innovation assessment."

**Applicability (§14.1):** **In-scope, high leverage.** The 9-stage
lifecycle ends with two stages that *are* the assessment row:
**Measurement** (stage 8) and **Learning** (stage 9). Without §7 the
innovation system can produce opportunities, hypotheses, experiments,
and evidence, but it cannot *measure* the result or *learn* from it —
which is the entire point of an IMS. ISO 56007:2023 is the row that
closes the feedback loop and turns the platform from a one-way
"ship work" system into a two-way "ship work, measure outcome, learn"
system.

**Current status (registry):** `PLANNED`. `implementation: "Bundled."`
Will land in `internal/innovation/measurement.go` (code) and
`docs/standards/innovation/lifecycle.md` (doc, §7 sub-section).

**Gap (§14.3):**

1. No `internal/innovation/` Go package exists.
2. No `Measurement` type. The existing `Work` carries a `Result`
   (SUCCEEDED | FAILED) but not a *measurement* of the innovation
   outcome (e.g., did this opportunity lead to a customer-visible
   improvement? did the experiment's evidence predict the outcome?).
3. No `Learning` capture mechanism — the 9th lifecycle stage has no
   code or doc landing pad.
4. No assessment cadence — when does the IMS itself get assessed?
   Quarterly? Per-slice? Per major version?

**Next step (§14.5):** Create `internal/innovation/measurement.go` (new)
defining the `Measurement` struct (the 8th lifecycle stage) and the
`Learning` struct (the 9th lifecycle stage), with a `Record(opp
*Opportunity, outcome string, evidencePath string) error` method that
appends a measurement to the opportunity and a `RecordLearning(text
string) error` method that records the lesson. Add
`internal/innovation/measurement_test.go` (new) with a table-driven
test covering: measurement appends to the right opportunity; learning
captures the lesson; an opportunity with no measurements still
serializes correctly; an opportunity with 3+ measurements is
append-only (no in-place edits). File path:
`internal/innovation/measurement.go` (new) and
`internal/innovation/measurement_test.go` (new).

**Risk / leverage.** High leverage — this is the row that closes the
feedback loop. Without it the IMS is a one-way pipeline. The cost is
one Go file + one test file. Risk of omission is high once the
opportunity object (§4) is live: an IMS that records opportunities
but never measures them is a funnel with no exit audit.

### Traceability — ISO 56007:2023

| ISO 56007 clause | System element | File | Owner | Status |
|---|---|---|---|---|
| §6 Innovation system assessment | IMS audit cadence | `docs/standards/innovation/lifecycle.md` §7 (new) | founder | PLANNED |
| §7 Innovation capability assessment | Maturity self-assessment | `docs/standards/innovation/lifecycle.md` §7 (new) | founder | PLANNED |
| §8 Innovation project assessment | Per-opportunity measurement | `internal/innovation/measurement.go` (new) | founder | PLANNED |
| §9 Assessment methods | (cross-link) §8 measurement methods | `measurement.go` (new) | founder | PLANNED |
| Measurement struct (stage 8) | New | `internal/innovation/measurement.go` (new) | founder | PLANNED |
| Learning struct (stage 9) | New | `internal/innovation/measurement.go` (new) | founder | PLANNED |
| Measurement + learning test | New | `internal/innovation/measurement_test.go` (new) | founder | PLANNED |

---

## §8. ISO 56008:2024 — Innovation Management (Vocabulary)

- **Standard.** `iso-56008-2024` — formal vocabulary standard. Defines
  every term used in the ISO 56000 family in a single normative
  document. This is the row every other row in this document depends
  on for shared meaning.
- **Registry row.** `standard_id == "iso-56008-2024"`, `control_id ==
  "ISO-56008"`, `status == "PLANNED"`, `implementation == "Bundled."`,
  `owner == "founder"`.

**Requirement (registry):** "Vocabulary."

**Applicability (§14.1):** **In-scope, foundational.** Every other
§1-§7 section uses ISO 56008:2024 terms (*innovation, innovation unit,
innovation portfolio, IMS, opportunity, hypothesis, experiment,
evidence, decision, implementation, measurement, learning*). Without
a recorded glossary, those terms drift in definition across docs
(e.g., is "implementation" a lifecycle stage or a V1 work-store
state? is "evidence" a file on disk or a structured bundle?). The
glossary is the *cheapest* gap to close and the *most leveraged* —
it costs one doc and unblocks every other §1-§7 from drifting.

**Current status (registry):** `PLANNED`. `implementation: "Bundled."`
Will land in `docs/standards/innovation/glossary.md`.

**Gap (§14.3):**

1. No `docs/standards/innovation/glossary.md` exists.
2. The terms *opportunity*, *hypothesis*, *experiment*, *evidence*,
   *decision*, *implementation*, *measurement*, *learning* are not
   defined anywhere in the works-execution repo — they appear only
   in the `platform-continuous-innovation` row's `requirement` field.
3. Several terms have *two* definitions in active use: "evidence"
   can mean (a) a file on disk recorded by a worker (Slice 1+2
   `services/work/store/store.go` definition) or (b) the lifecycle
   stage 5 from `platform-continuous-innovation`. This is the
   textbook vocabulary drift problem.

**Next step (§14.5):** Create `docs/standards/innovation/glossary.md`
(new) with: (a) a table mapping every ISO 56008:2024 term used in
§1-§7 to its works-execution definition (1-2 sentences each), (b) a
"terms with two definitions" section flagging "evidence" (work-store
file vs. lifecycle stage 5), "implementation" (work-store state vs.
lifecycle stage 7), and any other collisions, and (c) a reference
column pointing to the §1-§7 sub-section that uses each term. The
glossary is the first thing every other §1-§7 doc links to from its
first paragraph. File path: `docs/standards/innovation/glossary.md`
(new).

**Risk / leverage.** Foundational — the cheapest gap to close
(one doc) with the highest leverage (every other row inherits
from it). Risk of omission is low *today* (no §1-§7 docs exist
yet) but grows sharply as §1-§7 land and start using the same
words for different things.

### Traceability — ISO 56008:2024

| ISO 56008 term | Lifecycle stage | Definition owner | File | Status |
|---|---|---|---|---|
| innovation | (umbrella) | §1 | `docs/standards/innovation/lifecycle.md` (new) | PLANNED |
| innovation unit | (umbrella) | §1 | `lifecycle.md` (new) | PLANNED |
| innovation portfolio | (umbrella) | §4 | `internal/innovation/opportunity.go` (new, see §4) | PLANNED |
| innovation management system (IMS) | (umbrella) | §2 | `lifecycle.md` §2 (new) | PLANNED |
| innovation opportunity | stage 2 | §4 | `internal/innovation/opportunity.go` (new, see §4) | PLANNED |
| hypothesis | stage 3 | §4 | `internal/innovation/opportunity.go` (new, see §4) | PLANNED |
| experiment | stage 4 | §4 | `internal/innovation/opportunity.go` (new, see §4) | PLANNED |
| evidence | stage 5 | §4 (collides with work-store "evidence") | `glossary.md` (new) | PLANNED |
| decision | stage 6 | §4 | `internal/innovation/opportunity.go` (new, see §4) | PLANNED |
| implementation | stage 7 (collides with work-store "implementation") | §4 | `glossary.md` (new) | PLANNED |
| measurement | stage 8 | §7 | `internal/innovation/measurement.go` (new, see §7) | PLANNED |
| learning | stage 9 | §7 | `internal/innovation/measurement.go` (new, see §7) | PLANNED |
| Glossary doc | New | §8 | `docs/standards/innovation/glossary.md` (new) | PLANNED |

---

## §9. Cross-link — Continuous Innovation Feedback Standard

- **Standard.** `platform-continuous-innovation` (`control_id ==
  INNOVATION-FEEDBACK`, `domain == platform`, `version ==
  works-execution-1.0`). Defines the 9-stage lifecycle
  **Signal → Opportunity → Hypothesis → Experiment → Evidence →
  Decision → Implementation → Measurement → Learning** that every
  ISO 5600x requirement ultimately flows through.
- **Registry row.** `standard_id == "platform-continuous-innovation"`,
  `control_id == "INNOVATION-FEEDBACK"`, `status == "PLANNED"`,
  `implementation == "Slice 3: docs/standards/innovation/lifecycle.md."`,
  `owner == "founder"`.

**Requirement (registry):** "Innovation lifecycle: Signal → Opportunity
→ Hypothesis → Experiment → Evidence → Decision → Implementation →
Measurement → Learning."

**Applicability (§14.1):** **In-scope, highest leverage, anchors §1-§8.**
This row is not in the `innovation` domain (it is in `platform`) but it
*defines* the lifecycle every ISO 5600x row uses. The two standards
share a single deliverable: `docs/standards/innovation/lifecycle.md`
(new, per §1). The lifecycle doc carries the 9-stage diagram and
maps each stage to the §1, §2, §3, §4, §6, §7, §8 deliverables.

**Current status (registry):** `PLANNED`. `implementation: "Slice 3:
docs/standards/innovation/lifecycle.md."` No `enforcement_point`,
`test`, or `evidence` today; the doc IS the evidence once it lands.

**Gap (§14.3):**

1. No `docs/standards/innovation/lifecycle.md` (same as §1 §14.3 #1;
   the two rows share a deliverable).
2. No 9-stage diagram or table.
3. No link from each stage to the §1, §2, §3, §4, §6, §7, §8 deliverable
   that implements that stage.

**Next step (§14.5):** Same as §1: create
`docs/standards/innovation/lifecycle.md` (new) with the 9-stage table
(Signal → Opportunity → Hypothesis → Experiment → Evidence → Decision →
Implementation → Measurement → Learning), and add a column to the
table linking each stage to the §1-§8 row that implements it. The
doc is the single source of truth for both the ISO 56000 family (§1)
and the Continuous Innovation Feedback Standard (§9). File path:
`docs/standards/innovation/lifecycle.md` (new).

**Risk / leverage.** Highest leverage — this row is the *spine* the
other 8 rows hang on. Without it, every §1-§8 row re-derives the
lifecycle locally and the platform has no shared frame of reference
for "what stage is this opportunity in?". Risk of omission is low
today (no opportunity type exists yet) but is the single biggest
*future* risk in the innovation domain.

### Traceability — INNOVATION-FEEDBACK

| Lifecycle stage | ISO 5600x row | System element | File | Owner | Status |
|---|---|---|---|---|---|
| 1 Signal | §6 (ISO 56006 strategic intelligence) | Signal capture | `intelligence.md` (new, see §6) | founder | PLANNED |
| 2 Opportunity | §4 (ISO 56003 tools) | `Opportunity` type | `internal/innovation/opportunity.go` (new, see §4) | founder | PLANNED |
| 3 Hypothesis | §4 (ISO 56003 tools) | `Opportunity.Hypothesis` field | `internal/innovation/opportunity.go` (new, see §4) | founder | PLANNED |
| 4 Experiment | §4 (ISO 56003 tools) | `Opportunity.Experiment` field | `internal/innovation/opportunity.go` (new, see §4) | founder | PLANNED |
| 5 Evidence | §4 (ISO 56003 tools) | `Opportunity.Evidence` field | `internal/innovation/opportunity.go` (new, see §4) | founder | PLANNED |
| 6 Decision | §4 (ISO 56003 tools) | `Opportunity.Decision` field | `internal/innovation/opportunity.go` (new, see §4) | founder | PLANNED |
| 7 Implementation | (cross-link) `services/work/` | `Work` primitive (V1 carrier) | `services/work/store/store.go` | founder | PARTIAL |
| 8 Measurement | §7 (ISO 56007 assessment) | `Measurement` type | `internal/innovation/measurement.go` (new, see §7) | founder | PLANNED |
| 9 Learning | §7 (ISO 56007 assessment) | `Learning` type | `internal/innovation/measurement.go` (new, see §7) | founder | PLANNED |
| 9-stage lifecycle doc | §1 + §9 (anchors both) | New | `docs/standards/innovation/lifecycle.md` (new) | founder | PLANNED |

---

## Cross-cutting risks (rolled up from §1-§9)

1. **The whole innovation domain is PLANNED.** All 8 ISO rows + the
   INNOVATION-FEEDBACK row are at status `PLANNED`. None of the
   foundational code (`internal/innovation/`) or docs
   (`docs/standards/innovation/`) exists. Highest single fix:
   create `docs/standards/innovation/` directory and
   `internal/innovation/` package as part of this mapping's
   next-step recommendations.
2. **Vocabulary drift risk.** "Evidence", "implementation", and
   "decision" each have at least two definitions in active use (one
   from the V1 work store, one from the ISO 5600x lifecycle). Without
   §8's glossary, drift is guaranteed as §4 + §7 land. Mitigate by
   landing §8 first.
3. **No measurement / no learning closure.** The 9-stage lifecycle ends
   at stages 8-9, both of which depend on `internal/innovation/measurement.go`.
   Without it the platform is a one-way pipeline. Mitigate by landing
   §7 (measurement) before any opportunity is "completed" in
   production.
4. **IP row drift.** `iso-56005-2020` is functionally N/A for V1 but
   the registry still reads `PLANNED`. The row will be a
   registry-cleanup ticket in 6 months if not flipped to
   `NOT_APPLICABLE` with a `not_applicable_reason`. Mitigate by
   landing §5's registry edit.
5. **Cross-link to `INNOVATION-FEEDBACK` is not visible in the
   `innovation` domain.** The cross-link lives in this mapping doc;
   the registry does not encode it. Consider adding a `related_rows`
   field to each ISO 5600x row in a future registry schema bump
   (out of scope for this slice).

## Highest-value actionable gap (single recommendation)

> **Land §8 first — author `docs/standards/innovation/glossary.md`.** It
> is the cheapest gap to close (one doc) with the highest leverage
> (every other §1-§7 row inherits its terms from this doc). It also
> documents the two-definition collisions ("evidence", "implementation",
> "decision") *before* §4 and §7 add new code, which prevents
> vocabulary drift from compounding. File path:
> `docs/standards/innovation/glossary.md` (new).

**Recommended execution order** (after §8):

1. **§8** glossary doc (today).
2. **§1 + §9** `lifecycle.md` — the 9-stage doc that anchors both
   standards.
3. **§4** `internal/innovation/opportunity.go` + test (the
   opportunity state machine).
4. **§7** `internal/innovation/measurement.go` + test (the
   measurement + learning types that close the loop).
5. **§6** `intelligence.md` (the strategic-intelligence methodology).
6. **§2 + §3** extend `lifecycle.md` with IMS scope and
   guidance-vs-requirements sections (lowest cost — both are doc
   edits in the §1 doc).
7. **§5** flip the registry row to `NOT_APPLICABLE` and add
   `ip-deferral.md` (lowest cost — a one-row registry edit + a
   one-page doc).
