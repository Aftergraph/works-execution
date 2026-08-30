# Performance Domain — Per-Standard Mapping

> **Scope.** This document maps the **1 user-mandated CI/CD performance standard**
> declared in `docs/standards/registry.json` under `domain == "performance"` —
> **DORA Metrics** (`standard_id: "dora-metrics"`, `control_id: "DORA"`) — to
> the works-execution system. For this standard we record: applicability,
> current status (sourced from the registry), gap, concrete next step with
> file path, and a traceability table back to the registry row, slice
> deliverables, and the pack-mandated telemetry surface.
>
> **Method.** The §14 implementation rule from the user-mandated standards
> charter is applied uniformly:
> 1. determine applicability,
> 2. map to system requirements (V1 telemetry surface in
>    `docs/works-venture-starter-pack/05_OPERATIONS/OBSERVABILITY.md` and SLOs in
>    `docs/works-venture-starter-pack/05_OPERATIONS/SLOS_AND_SRE.md`),
> 3. identify gaps,
> 4. prioritize by risk and leverage,
> 5. recommend the highest-value actionable gap with a concrete file path.
>
> **Authoritative sources for status.**
> `docs/standards/registry.json` (machine-readable, row index by
> `standard_id`), `docs/works-venture-starter-pack/05_OPERATIONS/SLOS_AND_SRE.md`
> (V1 SLO basis), `docs/works-venture-starter-pack/05_OPERATIONS/OBSERVABILITY.md`
> (telemetry surface that DORA metrics ride on), `docs/standards/mappings/observability.md`
> (OTel pipeline that emits the `works.dora.*` series), `e2e/` (release smoke
> that proves deployment events land in the recorder), and `Makefile` (gate
> targets).

---

## Summary table

| # | Standard   | Registry `control_id` | Status   | Risk / Leverage                          | Top next step                                                                                                            |
|---|------------|-----------------------|----------|------------------------------------------|--------------------------------------------------------------------------------------------------------------------------|
| 1 | DORA Metrics | `DORA`              | PLANNED  | **High leverage** (design-partner SLO; sole `performance`-domain row) | Author `services/deploy/recorder.go` (new package) emitting `works.dora.{deployments,lead_time,change_failures,mttr}` counters/histograms via the OTel pipeline from `docs/standards/mappings/observability.md`. |

> **Cross-references.** DORA metrics ride on the OpenTelemetry pipeline
> authored in `docs/standards/mappings/observability.md` (items 29–32 in that
> document's metrics table — `works.dora.deployments.total`,
> `works.dora.lead_time.duration`, `works.dora.change_failures.total`,
> `works.dora.mttr.duration`). This document assumes the OTel SDK from that
> mapping is in place; DORA adds the **domain-specific instrumentation** and
> the **definitions** of when each metric increments.

---

## 1. DORA Metrics (`dora-metrics`)

- **Standard.** `dora-metrics` — the four "DORA" software-delivery performance
  metrics published by DORA / Google Cloud's *Accelerate* research:
  deployment frequency, lead time for changes, change failure rate, and mean
  time to recovery (MTTR).
- **Registry row.** `standards[].standard_id == "dora-metrics"`,
  `control_id == "DORA"`, `domain == "performance"`,
  `status == "PLANNED"`,
  `implementation == "Slice 3+: emit DORA metrics for design-partner deployments."`,
  `enforcement_point == null`,
  `test == null`,
  `evidence == null`,
  `owner == "founder"`,
  `exceptions == []`.
  (`docs/standards/registry.json` rows 1000–1014; this is the **only**
  row in the `performance` domain — `docs/standards/registry.json` line 2010
  counts exactly 1 standard under `"performance"`.)
- **Applicability (§14.1).** **In scope, high leverage.** works-execution's V1
  go-to-market posture (per the pack's `07_BUSINESS/` materials) depends on
  a small number of design-partner deployments rather than a broad self-serve
  fleet. DORA metrics are the canonical way to evidence "we ship reliably"
  to those partners. The pack's
  `05_OPERATIONS/SLOS_AND_SRE.md` already declares V1 SLOs that overlap with
  DORA's "lead time" and "MTTR" axes (work-creation P95 < 500 ms, lost-worker
  detection < 30 s); the DORA implementation reuses those SLO data points
  rather than inventing new ones.
- **System requirements mapped (from starter pack + slice-2 surface).**
  - **Deployment frequency.** Each successful `cmd/works-api` release
    (tag + push to design-partner cluster) increments
    `works.dora.deployments.total{deployment="design-partner"}`.
    Source of truth: `Makefile` `release` target + CI gate
    `avc/ci-local` (per `docs/adr/ADR-0005-self-hosted-ci.md`).
  - **Lead time for changes.** Defined as the delta between a commit's
    `git committer date` and the `timestamp` of the first successful
    deployment event that contains that commit SHA. Reported as the
    `works.dora.lead_time.duration` histogram (seconds).
    Source of truth: git history (`git log --pretty=%ct <sha>`) and the
    deploy recorder's persisted `deploy_attempts` table.
  - **Change failure rate.** Ratio `change_failures / deployments` over
    the trailing 30-day window. Reported as both a raw counter
    `works.dora.change_failures.total{failure="…"}` and a derived
    gauge `works.dora.change_failure_rate`.
    "Failure" = (a) a design-partner-reported rollback, (b) a P0 incident
    in `docs/runbooks/INCIDENT_RESPONSE.md` whose root cause is a code
    change shipped in the prior 7 days, or (c) any
    `failed_to_running` transition of a work node that the on-call
    attributes to a release (recorded via the
    `services/work/store/attempts.go` failure field; not invented here).
  - **MTTR.** Mean time from incident-open to incident-resolved for
    incidents classified as P0/P1 in the incident-response runbook.
    Reported as the `works.dora.mttr.duration` histogram (seconds).
    Source of truth: `docs/runbooks/INCIDENT_RESPONSE.md` (to be
    authored — see Gap below) and the audit-event log persisted via
    `services/observability/audit.go` (per
    `docs/standards/mappings/observability.md`).
- **Current status (registry).** **PLANNED.**
  `implementation` reads: *"Slice 3+: emit DORA metrics for design-partner
  deployments."* `enforcement_point`, `test`, and `evidence` are all `null`.
  No code, no test, and no runbook exist today (Slice 1 `d3db1d1` shipped
  the Work primitive + SQLite store + HTTP API + CLI + worker;
  Slice 2 `dab84f2` added lease-based scheduling + worker-loss recovery
  + log streaming — neither emits DORA signals). The `performance` domain
  contains **only** this single row, so this mapping is also the entire
  performance-domain documentation until a second standard is added.
- **Gap (§14.3).**
  1. **No `services/deploy/` package exists.** DORA data has nowhere to land
     in the binary tree. Slice 1 + Slice 2 created
     `services/api/`, `services/work/`, `internal/worker/`, and
     `cmd/{works,works-api,works-worker,works-kanban,works-standards}` —
     no `services/deploy/`.
  2. **No deploy-recorder instrumented on the OTel pipeline.** The OTel
     metrics package promised by `docs/standards/mappings/observability.md`
     (`services/observability/metrics.go`) does not exist yet, so even if
     `services/deploy/recorder.go` were written it has no exporter.
  3. **No definition of "change failure" for works-execution.** The pack's
     `07_BUSINESS/RISK_REGISTER.md` lists "release-induced regression" as a
     risk but does not classify which transitions count. This must be
     decided before the counter is meaningful.
  4. **No incident-response runbook.** `MTTR` cannot be measured without
     a clock that starts on incident-open and stops on incident-resolved.
     `docs/runbooks/INCIDENT_RESPONSE.md` is the canonical location;
     it does not exist today.
  5. **No design-partner deployment inventory.** DORA's
     "deployment frequency" denominator is the design-partner deploy
     stream; until the V1 release process is defined
     (`docs/operations/RELEASE_PROCESS.md` is PLANNED), the metric has no
     stable input set.
  6. **No dashboard / scrape contract.** Even if the metrics emit, no
     Prometheus / OTLP collector job is wired. (Bundled with the OTel
     gap above; tracked separately in
     `docs/standards/mappings/observability.md` §1.)
- **Concrete next step (§14.5).**
  Author the DORA recorder as a new package
  **`services/deploy/recorder.go`** that defines a `Recorder` interface
  with methods `RecordDeployment(ctx, evt DeploymentEvent) error` and
  `RecordIncident(sev Severity, openedAt, resolvedAt time.Time) error`,
  and emits through the OTel `Meter` from
  `docs/standards/mappings/observability.md`. The recorder is invoked
  from (a) `Makefile` `release` target via a `works-deploy-record`
  binary in `cmd/works-deploy/`, (b) the incident-response runbook's
  open/close hooks (when that runbook exists), and (c) the CI gate
  `avc/ci-local` on every `MERGED` commit so lead time is measured
  end-to-end.

  File paths:
  - `services/deploy/recorder.go` (new) — `Recorder` interface +
    `OTelRecorder` implementation; declares the four `works.dora.*`
    instruments with `works.dora.env`, `works.dora.service` attributes.
  - `services/deploy/recorder_test.go` (new) — golden test asserting
    (a) deployment increments the counter, (b) lead-time histogram
    receives the right duration, (c) MTTR histogram receives the right
    duration, (d) change-failure counter increments only on
    `IncidentSeverity=P0` + `CausedByRelease=true`.
  - `services/deploy/classify.go` (new) — small helper that maps an
    `attempts.failure_class` to the DORA "change failure" criterion
    above; keeps the policy in one place.
  - `cmd/works-deploy/main.go` (new) — thin CLI called from
    `Makefile`'s `release` target; reads `GIT_COMMITTER_DATE` of the
    deployed SHA and the merge commit, computes lead time, calls
    `Recorder.RecordDeployment`.
  - `Makefile` (extend) — add `release` target that (i) runs the
    self-hosted gate, (ii) on success invokes
    `bin/works-deploy record --sha <head> --env design-partner`.
  - `docs/runbooks/INCIDENT_RESPONSE.md` (new) — defines P0/P1
    classification, the "incident-open" / "incident-resolved"
    clocks, and how the on-call calls
    `works-deploy record-incident`. Without this file MTTR is
    unmeasurable.
  - `docs/operations/RELEASE_PROCESS.md` (new) — defines what counts
    as a "deployment" (merge to `main` + green `avc/ci-local` +
    pushed to design-partner cluster) so the deployment-frequency
    denominator is unambiguous.
  - `docs/standards/mappings/dora-evidence.json` (new) — periodic
    evidence dump: last 30 days of deployments, lead-time P50/P95,
    change-failure ratio, MTTR P50/P95. Populated by a small
    `cmd/works-dora-report/main.go` (new) that reads
    `services/deploy/` persisted state. This is the
    `evidence` pointer the registry row needs to move
    PLANNED → IMPLEMENTED.
  - `tests/observability/dora_smoke_test.go` (new) — black-box test:
    start the recorder in-process with an in-memory OTel exporter,
    fire a `DeploymentEvent`, assert the counter increments and
    the histogram observes the expected duration.

- **Acceptance evidence (to flip registry `status` to `IMPLEMENTED`).**
  `make test` passes `services/deploy/recorder_test.go` and
  `tests/observability/dora_smoke_test.go`; `services/deploy/recorder.go`
  exists; `cmd/works-deploy/main.go` exists and is called by
  `Makefile` `release`; the first design-partner deployment of Slice 3
  emits `works.dora.deployments.total{deployment="design-partner"} == 1`
  on the OTLP endpoint; `docs/standards/mappings/dora-evidence.json`
  is generated by `cmd/works-dora-report` and committed as the
  registry row's `evidence` pointer.
- **Risk / leverage.** **High leverage.** DORA is the only
  `performance`-domain standard in the registry, so closing it lifts
  the entire domain from PLANNED to IMPLEMENTED in one move. It also
  closes a gap in the design-partner sales narrative
  (`docs/works-venture-starter-pack/06_GTM/`) — without a DORA series
  the venture has no quantitative way to answer "how reliable is your
  deploy process?" Low implementation cost: one new package, one new
  CLI, two new docs files, and the OTel dependency from the
  observability mapping. Blockers are documentation-only (the
  incident runbook and release-process doc must exist before the
  counters are meaningful), not technical.

### Traceability — DORA Metrics

| Requirement                          | System element                                   | File                                                              | Owner   | Status  |
|--------------------------------------|--------------------------------------------------|-------------------------------------------------------------------|---------|---------|
| Deployment-frequency counter         | `Recorder.RecordDeployment` → OTel counter       | `services/deploy/recorder.go`                                     | founder | PLANNED |
| Lead-time histogram                  | Deploy-recorder computes Δ(t_merge, t_deploy)     | `services/deploy/recorder.go`, `cmd/works-deploy/main.go`         | founder | PLANNED |
| Change-failure counter               | `classify.go` maps `attempts.failure_class`      | `services/deploy/classify.go`, `services/deploy/recorder.go`      | founder | PLANNED |
| MTTR histogram                       | Incident runbook open/close hooks                | `docs/runbooks/INCIDENT_RESPONSE.md`, `services/deploy/recorder.go` | founder | PLANNED |
| Release trigger (deployment denominator) | `Makefile` `release` target                  | `Makefile`, `cmd/works-deploy/main.go`                            | founder | PLANNED |
| CI gate (`avc/ci-local`) on exact-head | Existing slice-1 + slice-2 gate                | `ci/local-runner/run-local-ci.sh`                                 | founder | IMPLEMENTED |
| Deployment-attempt persistence       | SQLite table `deploy_attempts`                   | `services/deploy/store.go` (new)                                  | founder | PLANNED |
| OTel meter (DORA series rides on it) | OTel pipeline from observability mapping         | `services/observability/metrics.go`                               | founder | PLANNED |
| Audit envelope (incident open/close) | CloudEvents-shaped envelope                      | `services/observability/audit.go`                                 | founder | PLANNED |
| Unit test                            | Golden OTel-exporter test                        | `services/deploy/recorder_test.go`                                | founder | PLANNED |
| Smoke test                           | Black-box deploy event → counter/histogram       | `tests/observability/dora_smoke_test.go`                          | founder | PLANNED |
| Evidence pointer                     | 30-day report JSON                               | `docs/standards/mappings/dora-evidence.json`                     | founder | PLANNED |
| Makefile gate                        | `make dora-smoke`                                | `Makefile`                                                        | founder | PLANNED |

> **Dependency note.** This row depends on the OTel row
> (`docs/standards/mappings/observability.md` §1) being at least
> PARTIAL — `services/observability/metrics.go` must exist before
> `services/deploy/recorder.go` can compile. That is the only
> technical dependency; everything else is documentation.

> **Domain closure.** When this row moves to `IMPLEMENTED`, the
> `performance` domain closes (it contains exactly one standard).
> `docs/standards/registry.json` line 2010 should update the
> `"performance"` count to `0` PLANNED rows at that point.