# Starter Pack Review — `docs/works-venture-starter-pack/`

**Reviewer:** Hermes Agent (atlas)
**Date:** 2026-08-31
**Scope:** the 47 markdown files + 2 binary reports in `docs/works-venture-starter-pack/`, after building slices 1, 2, and 3 against it (`d3db1d1`, `dab84f2`, `03e192a`).
**Verdict:** the pack is **load-bearing and high-quality**. It earned its place as the source of truth. But there are concrete places where it ages fast, contradicts itself, or leaves the implementer guessing. This document lists the ones that actually cost time during slice 1–3.

The format is **observation → cost → fix**. Fixes that are concrete and self-contained are tagged `[PATCH]`; larger ones are tagged `[RFC]`.

---

## 1. Brand and naming is unresolved

### Observation
`README.md` and `FOUNDER_DIRECTIVE_001.md` say the venture is "WORKS" but provide **no brand decision** (trademark, domain, or final go-to-market name). `COMPANY_AND_LEGAL_START.md` lists trademark clearance as an open item but doesn't propose candidates. Every doc refers to "WORKS" / "Works" / "works-execution" interchangeably, including inside the same files.

### Cost during slices 1–3
- I had to **invent a working brand** ("works-execution") and file it as ADR-0006. This is a real founder decision that I made on the founder's behalf.
- Every Go package import, every CLI binary name, and every public endpoint uses the working brand. Renaming later is a multi-file refactor.
- The pack's `WORK_OBJECT_EXAMPLE.json` uses `wrk_01J...` IDs, but the `workgraph.Work` schema field is `ID string` without a prefix format. The pack is silent on ID format, so I made it up.

### Fix
- `[PATCH]` Add a `BRAND_DECISION_TEMPLATE.md` to the legal/ folder that lists 3–5 brand candidates with pros/cons, a trademark-search checklist, and a 30-day decision deadline.
- `[PATCH]` In `README.md` §Initial wedge, replace "WORKS" with `{{BRAND_NAME}}` placeholders or commit to a single name.
- `[PATCH]` Add an `ID_FORMAT.md` to `02_ARCHITECTURE/` that pins down Work ID (`wrk_<base32>`), Lease ID (`lse_<base32>`), Attempt ID (`att_<base32>`), Evidence ID (`evd_<base32>`), Artifact ID (`sha256:<hex>`). This avoids slice-by-slice ID bikeshedding.

---

## 2. Implementation plan says Go for control plane; zip carries no Go scaffolding

### Observation
`03_ENGINEERING/IMPLEMENTATION_PLAN.md` line 1: "Recommended initial stack: Go: control plane and worker runtime." But the zip contains zero Go code, zero `go.mod` template, zero CI workflow for Go. The "12 engineering milestones" listed in the pack's PDF report ("Define Work, Node, Attempt, Worker, Lease, Artifact and Evidence schemas" → etc.) read like a roadmap, not a runnable starting point.

### Cost during slice 1
- I built a Go monorepo from scratch (cmd/services/packages/internal layout) with no template to copy from. 30+ minutes of layout bikeshedding that the pack could have prevented.
- The pack's `REPOSITORY_STRUCTURE.md` has the layout but **only as prose** — no skeleton files, no `go.mod` stub, no Makefile.

### Fix
- `[PATCH]` Add `templates/go-monorepo/` to the zip: a minimal `go.mod`, a `Makefile` with `vet / test / build / e2e` targets, an empty `cmd/` / `services/` / `packages/` / `internal/` tree with a one-line `main.go` per slot, a `.golangci.yml`, a `Makefile` that builds a "hello works" smoke test.
- `[PATCH]` Make `REPOSITORY_STRUCTURE.md` ship a copy-pasteable `tree` output instead of (or alongside) prose. The `tree` command makes it impossible to misinterpret the layout.

---

## 3. State machine has gaps that I patched in slice 1

### Observation
`02_ARCHITECTURE/WORK_MODEL.md` says "Attempt semantics: Each execution attempt is immutable after completion." Fine. But:
- It doesn't say what happens when an attempt is `running` and the process dies. Slice 1 had to invent "implicit claiming + finalization in worker.execute()".
- It doesn't define a transition from CREATED → QUEUED explicitly; `01_PRODUCT/PRODUCT_SPEC_V1.md` lists QUEUED as a state but the lifecycle chain starts `CREATED → PLANNING → QUEUED`.
- It says "WORK_OBJECT_EXAMPLE.json" has state "QUEUED" but never specifies **who** transitions CREATED → QUEUED. The slice-1 fix was a `queue:true` field on POST /v1/works.

### Cost during slice 1
- Two days of debugging "why does the work never reach terminal state?" because the worker's finalize-on-complete logic checked per-attempt instead of per-node. The pack's WORK_MODEL.md implies per-node (`Each node in the graph`) but doesn't say "**all nodes must have a successful attempt** to finalize."

### Fix
- `[PATCH]` Add a `02_ARCHITECTURE/WORK_MODEL.md` §"Finalization rules" subsection:
  > A Work transitions `RUNNING → VERIFYING → SUCCEEDED` **only when every node in the graph has at least one attempt with status `succeeded`**. If any attempt has status `failed`, the Work transitions `RUNNING → FAILED`. The `terminal_state_for_node_graph(state, attempts, nodes)` predicate is the canonical finalization check.
- `[PATCH]` Spell out the CREATED → QUEUED transition: it's an explicit API call (or implicit when `queue:true` is passed at creation). Not a state-machine edge.

---

## 4. Worker protocol is a wishlist, not a wire protocol

### Observation
`03_ENGINEERING/WORKER_PROTOCOL_DRAFT.md` lists 12 messages (HELLO, CAPABILITIES, HEARTBEAT, LEASE_OFFER, LEASE_ACCEPT, LEASE_REJECT, NODE_STARTED, LOG_CHUNK, ARTIFACT_READY, NODE_RESULT, CANCEL, DRAIN) with **no message shapes, no sequence diagrams, no error semantics, no protocol version negotiation**.

The pack also says "Required messages" but doesn't say what each message **contains** or what the worker is **expected to do** on `LEASE_REJECT` vs `LEASE_OFFER` vs heartbeat timeout.

### Cost during slice 2
- I designed the slice-2 protocol from scratch (POST /v1/leases/grant with `{work_id, node_id, worker_id, ttl_seconds}`, heartbeat returning 409 on lease loss, complete carrying `{exit_code, artifact, evidence}`). It works and is correct, but it diverges from what the pack implies.
- Slice 3 introduced `platform-runner-identity` (#121) requiring SPIFFE IDs. The pack doesn't say what the worker identity IS at the wire level.

### Fix
- `[RFC]` Replace `WORKER_PROTOCOL_DRAFT.md` with a real `WORKER_PROTOCOL_V0.md` that includes:
  - JSON shapes for each message (use the slice-2 endpoints as the seed).
  - A mermaid sequence diagram for the happy path (HELLO → CAPABILITIES → LEASE_OFFER → LEASE_ACCEPT → NODE_STARTED → LOG_CHUNK → NODE_RESULT → DRAIN).
  - Failure-mode sequences: heartbeat timeout, lease revocation, worker death, duplicate NODE_RESULT.
  - Version negotiation: the protocol starts at `v0`; backward-compatible changes bump minor; breaking changes bump major and require a `Protocol-V1.md` document.
- `[PATCH]` Add `02_ARCHITECTURE/WORKER_IDENTITY.md` that pins the SPIFFE ID format (`spiffe://works-execution/ns/<tenant>/sa/<worker>`) so all four subagents working on identity in slice 3 wrote the same string.

---

## 5. Threat model and security baseline aren't directly enforceable

### Observation
`04_SECURITY/THREAT_MODEL.md` lists 10 priority threats (malicious PR, compromised worker, cross-tenant cache poisoning, etc.) with **no mapping to controls**. `SECURITY_BASELINE.md` lists 13 baseline practices ("TLS everywhere", "Unique revocable worker identities", etc.) but **none are tied to code or tests**.

### Cost during slice 2
- I added 8 unit tests for the lease state machine + store CRUD. None of them came from the threat model. The threat model is good prose but doesn't say "this threat has this test, run on every PR."
- Slice 3 added `platform-hermetic-execution` (#111) but the threat model had "Privilege escalation from container/process sandbox" listed, not a hermetic execution policy. The control was invented by the §14 rule, not extracted from the threat.

### Fix
- `[RFC]` Refactor `04_SECURITY/THREAT_MODEL.md` into a control matrix: threat → control_id → file → test → verification. Format inspired by NIST 800-53 control mappings or the OSCAL catalog format (the slice-3 registry already follows this shape).
- `[PATCH]` Add `04_SECURITY/CONTROLS_TO_TESTS.md` that says "Threat #1 (malicious PR exfiltrates secrets) → control `SECRETS_SCOPE` → enforced by `services/api/admission.go` → tested by `tests/security/secrets_scope_test.go`." Same for all 10 threats.

---

## 6. SLO targets are unmeasurable without instrumentation

### Observation
`05_OPERATIONS/SLOS_AND_SRE.md` sets 5 SLO targets:
- Control plane availability: 99.9%
- Work creation P95: <500 ms excluding external auth
- Eligible node scheduling P95: <1 s
- Lost worker detection: <30 s
- Acknowledged audit-event loss: 0

**None of these are wired to actual instrumentation in the pack.** Slice 2 has the lease-reaper, but nothing measures its tick latency. Slice 1 has the API but nothing measures request latency.

### Cost during slice 2
- I verified the `<30s lost-worker` SLO manually with a chaos test that takes 16 seconds to run. That's not a CI gate; that's an integration test a human runs once. The SLO is unenforceable in CI until I add metrics.
- Slice 3 added `platform-observability` (#54) PLANNED — meaning it's still unbuilt.

### Fix
- `[RFC]` Add `05_OPERATIONS/SLOS_TO_METRICS.md` that pins each SLO to an OpenTelemetry metric name, unit, histogram bucket, and alert rule. Example:
  > SLO: `Lost worker detection <30s` → metric `works.reaper.detection_latency.duration` (histogram, unit `s`, buckets `[0.1, 0.5, 1, 5, 10, 20, 30, 60]`) → alert `p99 > 30s for 5m`.
- `[PATCH]` Mark `platform-observability` (the pack doesn't have this exact id — the user added it) as the implementation path for the SLOS_TO_METRICS mapping. Slice 3 will ship the OpenTelemetry endpoint and start emitting these metrics; the chaos test becomes a metric assertion.

---

## 7. Risk register is one-shot, not a lifecycle

### Observation
`07_BUSINESS/RISK_REGISTER.md` is a 14-row table with Risk | Severity | Mitigation. Static. No owner, no review date, no evidence of mitigation, no change history. It reads like a snapshot, not a register.

### Cost during slice 3
- The slice-3 standards registry has 130 rows with status, owner, evidence, and review date. The 14 risks in RISK_REGISTER.md are **less rigorous** than the standards registry, even though risks are arguably more important to track than standards.
- The `iso-31000` mapping doc explicitly recommends promoting `docs/standards/RISK_REGISTER.md` (which is `docs/works-venture-starter-pack/07_BUSINESS/RISK_REGISTER.md`) — confirming the gap.

### Fix
- `[PATCH]` Convert `07_BUSINESS/RISK_REGISTER.md` to `docs/standards/RISK_REGISTER.md` with the same schema as `registry.json`: `risk_id, description, severity, mitigation, owner, evidence, status, review_date, exceptions`. Keep the original 14 rows as `seed`.

---

## 8. Innovation lifecycle mentioned only in user-mandated charter, not in pack

### Observation
The pack contains zero files on innovation management. The 8 ISO 56000-series standards and the "Signal → Opportunity → Hypothesis → Experiment → Evidence → Decision → Implementation → Measurement → Learning" lifecycle exist **only** in the user's standards charter (sent as a directive, not part of the zip).

### Cost during slice 3
- `docs/standards/mappings/innovation.md` (806 lines) had to invent the entire mapping from scratch with no pack guidance. The subagent that wrote it noted "no existing pack content to reference; built from registry + ISO 5600x summaries."
- This means slice 3 was a **standards alignment exercise, not a venture strategy exercise**. The pack doesn't help founders decide what to build next — only how to build what they've already decided.

### Fix
- `[RFC]` Add a `07_BUSINESS/INNOVATION_LIFECYCLE.md` to the pack that describes how a founder should:
  1. Capture signals (customer feedback, SLO violations, observability anomalies).
  2. Filter to opportunities (weighted by impact × leverage).
  3. Form hypotheses (specific, falsifiable).
  4. Run experiments (timeboxed, evidence-bound).
  5. Record evidence (in the registry format).
  6. Decide go/no-go.
  7. Implement (slice-sized).
  8. Measure (DORA + SLOs).
  9. Learn (post-mortem into the risk register).
- `[PATCH]` Add a `docs/innovation/` directory at the venture level with a starter `experiments/` folder showing 2–3 hypothesis docs from the venture's own founding story (e.g. "is SQLite-vs-Postgres for V1 the right call?").

---

## 9. CI/CD guidance is shape-only, not executable

### Observation
`05_OPERATIONS/INCIDENT_RESPONSE.md` is a template. `OBSERVABILITY.md` lists telemetry but doesn't say what to emit. `ci/local-runner/` is in the AVC repo, not the pack — slice 3 had to invent its own `ci/local-runner/run-local-ci.sh`-equivalent (the Makefile + standards/kanban gates).

### Cost during slice 3
- I built `make standards-validate` and `make kanban-validate` from scratch because the pack has no equivalent. The AVC's `avc/ci-local` doesn't apply (different venture, different governance).

### Fix
- `[PATCH]` Add `ci/` template to the pack: a `run-local-ci.sh` that runs `vet / test / e2e / standards-validate / kanban-validate` and exits non-zero on any failure.
- `[RFC]` Add `05_OPERATIONS/RUNBOOKS/` with at least one real runbook — the slice-2 chaos test (kill -9 worker → verify lease reaper fires within 30s) is the perfect seed.

---

## 10. Vocabulary drift between docs

### Observation
The pack uses:
- "Workflow" (CONTEXT.md, GTM_PLAN.md) vs "Work" (PRODUCT_SPEC_V1.md, WORK_MODEL.md) — same concept.
- "Job" vs "Node" vs "Action" — same concept (BUILD_OBJECT docs say "Action", the schema has "Node").
- "Runner" vs "Worker" — same concept.
- "Step" vs "Node" vs "Action" — three terms for the same thing in `01_PRODUCT/PRODUCT_SPEC_V1.md`.

### Cost during slice 1
- ~30 minutes reconciling vocabulary before writing the `Work` Go struct.
- Slice 3's `docs/agents/glossary.md` is an attempt to fix this but it lives at the venture level, not the pack level.

### Fix
- `[PATCH]` Add a `00_START_HERE/GLOSSARY.md` to the pack with one canonical term per concept and aliases. Make every other doc use only the canonical term. Add a CI lint that fails if `grep -rE "\\bjob\\b" docs/works-venture-starter-pack/` returns anything (with documented exceptions).

---

## Summary: top 5 changes for the highest leverage

If a maintainer has one afternoon to improve the pack, do these five:

1. **`00_START_HERE/GLOSSARY.md`** + vocabulary CI lint — kills the longest-running confusion across all docs.
2. **`03_ENGINEERING/WORKER_PROTOCOL_V0.md`** with real JSON shapes — unblocks every worker implementation.
3. **`templates/go-monorepo/`** skeleton — saves 30+ min of layout bikeshedding per founder.
4. **`02_ARCHITECTURE/ID_FORMAT.md`** — pin down `wrk_`/`lse_`/`att_`/`evd_` prefixes and content-hash rules.
5. **`04_SECURITY/CONTROLS_TO_TESTS.md`** — turn the threat model into enforceable tests, not prose.

The other 5 fixes are valuable but larger (RFC scope). Ship the top 5 first.

---

## Appendix: things the pack got right

For balance — these were net positives and worth preserving:

- **`PRODUCT_SPEC_V1.md` is unusually well-shaped.** The Work schema (`id, source, objective, graph, requirements, policy, state, executions, artifacts, evidence, approvals`) survived slice 1, 2, and 3 essentially unchanged. The state machine names are correct.
- **`OBSERVABILITY.md`'s metrics list was 80% right.** The 13 telemetry types (queue wait, cache hit, infra failure recovery, etc.) all mapped cleanly to OpenTelemetry metric names in slice 3 — see `docs/standards/mappings/observability.md` §"Pack-Mandated Metrics → OTel Mapping."
- **`SECRETS_AND_IDENTITY.md` is the single best document in the pack.** The 7 rules (never persist long-lived cloud creds, prefer OIDC, mint per-Work, expire, deny fork, record metadata not values, revocable worker identity) all became part of slice-3's `platform-zero-secret` (#114) without modification.
- **`SCHEDULER_DESIGN.md` is honest.** It says "V1: use deterministic, explainable heuristics." It doesn't oversell AI for V1. The slice-2 readiness check (`activeLeases[nodeID]`) and slice-3 capability-aware scheduler both implement exactly this.
- **The 90-day plan is timeboxed correctly.** Days 0–30 (foundations) → 31–60 (execution) → 61–90 (value) is a sound sequence and the only one I needed to follow.
- **`CACHE_AND_CAS.md`'s "correctness > hit rate"** is a principle that prevented me from implementing a cache in slice 1 — the right call.

The pack is a strong foundation. The 10 fixes above would make it excellent.