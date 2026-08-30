# Observability Domain — Per-Standard Mapping

> **Scope.** This document maps the **6 user-mandated observability standards**
> declared in `docs/standards/registry.json` (domain = `observability`) to the
> works-execution system: **OpenTelemetry**, **OTel Semantic Conventions**,
> **OpenMetrics**, **Prometheus Exposition**, **OpenTracing (legacy)**, and
> **OpenFeature**. For each standard we record: applicability, current status
> (sourced from `registry.json`), gap, concrete next step with file path, and a
> traceability table back to the registry row, slice deliverables, and the
> pack-mandated telemetry surface.
>
> **Method.** The §14 implementation rule from the user-mandated standards
> charter is applied uniformly:
> 1. determine applicability,
> 2. map to system requirements (V1 telemetry surface in
>    `docs/works-venture-starter-pack/05_OPERATIONS/OBSERVABILITY.md` and SLOs in
>    `SLOS_AND_SRE.md`),
> 3. identify gaps,
> 4. prioritize by risk and leverage,
> 5. recommend the highest-value actionable gap with a concrete file path.
>
> **Authoritative sources for status.**
> `docs/standards/registry.json` (machine-readable), `Makefile` (gate targets),
> `internal/worker/` (slice-2 lease/recovery), `services/api/api.go` (HTTP
> control plane), `services/work/store/` (SQLite state), and the starter pack
> `05_OPERATIONS/OBSERVABILITY.md`, `05_OPERATIONS/SLOS_AND_SRE.md`,
> `02_ARCHITECTURE/CACHE_AND_CAS.md`, `02_ARCHITECTURE/SCHEDULER_DESIGN.md`.

---

## Summary table

| #  | Standard                       | Registry `control_id`  | Status         | Risk / Leverage                       | Top next step                                                                                                                                          |
|----|--------------------------------|------------------------|----------------|---------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------|
| 1  | OpenTelemetry                  | `OTEL`                 | PLANNED        | **High leverage** (everything else hangs off it) | Author `services/observability/tracing.go` + `services/observability/metrics.go` (new package) and wire into `cmd/works-api/main.go` and `cmd/works-worker/main.go`. |
| 2  | OTel Semantic Conventions      | `OTEL-SEMCONV`         | PLANNED        | High leverage (interoperability)      | Define attribute registry in `services/observability/semconv.go` referencing `http.server.*`, `db.client.*`, `process.*`, `otelcol.*`.                 |
| 3  | OpenMetrics                    | `OPENMETRICS`          | PLANNED        | Medium leverage (vendor-neutral scrape) | Expose `/metrics` with content negotiation in `services/api/api.go` returning `application/openmetrics-text; version=1.0.0`.                          |
| 4  | Prometheus Exposition          | `PROM-EXPOSITION`      | PLANNED        | Medium leverage (Prometheus ecosystem) | Same `/metrics` endpoint negotiates `text/plain; version=0.0.4` per Prometheus content negotiation — covered by OpenMetrics implementation.            |
| 5  | OpenTracing (legacy)           | `OPENTRACING`          | NOT_APPLICABLE | Zero (superseded)                     | Record explicit rationale in `docs/standards/RATIONALE_LOG.md`; no code change.                                                                       |
| 6  | OpenFeature                    | `OPENFEATURE`          | PLANNED        | Medium leverage (slice 4+)            | Defer to slice 4; track in kanban. Pre-stage contract in `services/featureflags/provider.go` (skeleton with no provider).                            |

> **Companion standards** (in registry but out of this domain mapping's primary
> 6): **CloudEvents** (`CLOUDEVENTS`, domain `platform`) — audit events emitted
> as CloudEvents in `services/observability/audit.go` (PLANNED); and **DORA
> metrics** (`DORA`, domain `performance`) — emitted as OTel metrics in slice
> 3+. Both reuse the OTel pipeline described in §1 below; they are referenced
> here so the audit-event ↔ trace ↔ metric pipeline is designed as one.

---

## 1. OpenTelemetry (OTel)

- **Standard.** `opentelemetry` — vendor-neutral traces + metrics + logs SDK,
  W3C trace context propagation.
- **Registry row.** `standards[].standard_id == "opentelemetry"`,
  `control_id == "OTEL"`, `status == "PLANNED"`,
  `implementation == "Slice 3: OTel SDK with W3C trace context propagation."`,
  `enforcement_point == "services/observability/"`,
  `test == "tests/observability/"`,
  `evidence == "PLANNED"`.
- **Applicability.** **In scope — the spine.** Every other observability
  standard in this domain either consumes OTel (Semantic Conventions,
  OpenMetrics, the audit-event→trace linkage) or is superseded by it
  (OpenTracing). Slice-3-onwards is in scope; slice 1 + slice 2 already expose
  the seam where OTel hooks attach (`services/api/api.go`, `internal/worker/worker.go`,
  `services/work/store/store.go`).
- **System requirements mapped (from starter pack
  `05_OPERATIONS/OBSERVABILITY.md`).**
  - Work/node/attempt transitions → traces + spans with attributes.
  - Queue depth/age, worker capacity/utilization/churn → metrics (counters,
    up/down counters, histograms).
  - Scheduling decision reasons → span events + log fields.
  - Cache hit/miss/latency → metrics (`cache.requests` counter,
    `cache.operation.duration` histogram).
  - Artifact transfer metrics → metrics + traces.
  - Failure classification → span status + log fields.
  - Remediation outcome → audit event + metric.
  - Cost attribution → metric attributes (`tenant.id`, `pool`).
  - Critical path → trace tree (parent/child spans).
  - External dependency health → span status + metric.
- **Current status.** PLANNED. No OTel SDK in `go.mod`; no
  `services/observability/` package exists.
- **Gap.** The observability package is referenced by every registry row in
  this domain and by `cis-controls-v8` (audit), `nist-csf-2.0` (Detect), and
  `cloudevents` (audit envelope) — but no code has landed. Without it, the
  slice-3 SLO assertions in `SLOS_AND_SRE.md` (control-plane availability,
  scheduling P95, lost-worker detection) cannot be evidenced.
- **Concrete next step (highest leverage).**
  1. `go get go.opentelemetry.io/otel go.opentelemetry.io/otel/sdk go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` and add to `go.mod`.
  2. Create package `services/observability/` with files
     `tracing.go` (TracerProvider init, W3C propagator, OTLP exporter behind
     `OTEL_EXPORTER_OTLP_ENDPOINT`), `metrics.go` (MeterProvider init,
     periodic reader, OTLP exporter), `resource.go` (service.name,
     service.version, deployment.environment attributes), and
     `audit.go` (CloudEvents-shaped audit envelope; this also unblocks the
     `cloudevents` row).
  3. Wire `services/api/api.go` with `otelhttp.NewHandler` middleware around
     the `ServeMux` so every HTTP request emits an `http.server.*` span/metric
     pair per §2 below.
  4. Wire `cmd/works-api/main.go` and `cmd/works-worker/main.go` to call
     `observability.Init(ctx)` and `defer observability.Shutdown(ctx)`.
  5. Create `tests/observability/otel_smoke_test.go` asserting (a) a span is
     exported for an HTTP request, (b) `works.queue.depth` counter is
     incremented on `GrantLease`, (c) trace context propagates across the
     HTTP boundary (W3C `traceparent` header round-trip).
  File paths: `services/observability/{tracing.go,metrics.go,resource.go,audit.go,doc.go}` (new),
  `services/api/api.go` (add otelhttp middleware),
  `internal/worker/worker.go` (start child span around subprocess exec),
  `services/work/store/leases.go` (record queue-depth metric on grant),
  `cmd/works-api/main.go` + `cmd/works-worker/main.go` (init/shutdown),
  `tests/observability/otel_smoke_test.go` (new),
  `Makefile` (add `otel-smoke` target).
- **Risk / leverage.** **Highest leverage in this domain.** Every other
  observability row, plus `cloudevents` (audit envelope), `dora-metrics`
  (deployment/lead-time/MTTR counters), and `nist-csf-2.0` (Detect function),
  depend on this package existing. The OTLP exporter is wired in slice 3
  design; the cost is two new files + 4 lines of init.

### Traceability — OpenTelemetry

| Requirement                              | System element                                  | File                                                                | Owner    | Status  |
|------------------------------------------|-------------------------------------------------|---------------------------------------------------------------------|----------|---------|
| W3C trace context propagation            | otelhttp middleware                             | `services/observability/tracing.go`, `services/api/api.go`         | founder  | PLANNED |
| OTLP trace export                        | OTLP/gRPC exporter behind env var               | `services/observability/tracing.go`                                 | founder  | PLANNED |
| OTLP metric export                       | Periodic reader + OTLP exporter                 | `services/observability/metrics.go`                                 | founder  | PLANNED |
| Service resource attributes              | resource detector                              | `services/observability/resource.go`                                | founder  | PLANNED |
| Lease/scheduling spans                   | Worker wraps subprocess exec in child span      | `internal/worker/worker.go`                                         | founder  | PLANNED |
| Lease-grant counters                     | Counter inc on `GrantLease`                     | `services/work/store/leases.go`                                     | founder  | PLANNED |
| Audit envelope (CloudEvents-shaped)      | Emitted on every state transition               | `services/observability/audit.go`                                   | founder  | PLANNED |
| Lifecycle init/shutdown                  | `main.go` boot order                            | `cmd/works-api/main.go`, `cmd/works-worker/main.go`                 | founder  | PLANNED |
| Smoke test                               | Span + counter + traceparent round-trip         | `tests/observability/otel_smoke_test.go`                            | founder  | PLANNED |
| CI gate                                  | `make otel-smoke`                               | `Makefile`                                                          | founder  | PLANNED |

---

## 2. OpenTelemetry Semantic Conventions

- **Standard.** `otel-semconv` — stable attribute/metric/span names for HTTP,
  DB, runtime, messaging, etc.
- **Registry row.** `standard_id == "otel-semconv"`,
  `control_id == "OTEL-SEMCONV"`, `status == "PLANNED"`,
  `implementation == "Slice 3: use semantic conventions for HTTP, DB, runtime spans."`,
  `enforcement_point == "services/observability/"`.
- **Applicability.** **In scope.** works-execution speaks HTTP (control plane
  API) and SQLite (authoritative state store) and spawns subprocesses (worker
  runtime). All three have published OTel semantic conventions.
- **System requirements mapped.**
  - HTTP attributes (`http.request.method`, `http.route`,
    `http.response.status_code`, `url.path`, `server.address`,
    `network.protocol.version`) on every server span.
  - DB attributes (`db.system="sqlite"`, `db.namespace`,
    `db.collection.name="works|leases|attempts"`,
    `db.operation.name="SELECT|INSERT|UPDATE"`,
    `db.query.summary`) on every store call.
  - Process/runtime attributes (`process.pid`, `process.executable.path`,
    `process.runtime.name="go"`, `process.runtime.version`).
  - Custom domain attributes prefixed `works.*` (e.g. `works.work.id`,
    `works.node.id`, `works.tenant.id`, `works.attempt.id`, `works.pool`,
    `works.trust_class`).
- **Current status.** PLANNED. The attributes are not yet declared; no spans
  are emitted at all (depends on §1).
- **Gap.** Without semconv, even after the OTel SDK lands, downstream APM
  backends (Jaeger, Tempo, Honeycomb, Datadog) cannot map names to their
  out-of-the-box dashboards — operators would have to write ad-hoc queries for
  every metric.
- **Concrete next step.**
  1. Create `services/observability/semconv.go` exporting typed
     `attribute.Key` constants for the HTTP/DB/process domains (use the
     `go.opentelemetry.io/otel/semconv/...` packages where stable).
  2. Define a custom domain schema in
     `docs/standards/mappings/semconv-works.md` (new) listing every
     `works.*` attribute, its type, allowed values, and the code site that
     sets it. This is the canonical reference for the audit attribute set
     consumed by `cloudevents`.
  3. Use `semconv` constants in `services/api/api.go` (otelhttp middleware
     auto-populates HTTP attributes; for Go 1.25 use the
     `otelhttp.WithSpanNameFormatter` option to format `HTTP {method} {route}`),
     in `services/work/store/{store,leases}.go` (wrap every query in a
     child span with `db.*` attributes), and in
     `internal/worker/worker.go` (subprocess span with `process.*` attributes).
  4. Lock the attribute set in `tests/observability/semconv_test.go` (new)
     that fails CI if a metric/span is emitted without its declared attributes.
  File paths: `services/observability/semconv.go` (new),
  `docs/standards/mappings/semconv-works.md` (new),
  `tests/observability/semconv_test.go` (new),
  `services/api/api.go` (otelhttp options),
  `services/work/store/{store.go,leases.go}` (child spans),
  `internal/worker/worker.go` (process attrs).
- **Risk / leverage.** High leverage — semconv is what turns raw telemetry
  into interoperable telemetry. Locked-in CI test means we cannot drift.

### Traceability — OTel Semantic Conventions

| Convention group | System element                       | File                                          | Owner    | Status  |
|------------------|--------------------------------------|-----------------------------------------------|----------|---------|
| HTTP attributes  | otelhttp middleware                  | `services/observability/semconv.go`           | founder  | PLANNED |
| DB attributes    | store query wrappers                 | `services/work/store/{store.go,leases.go}`    | founder  | PLANNED |
| Process/runtime  | resource detector                    | `services/observability/resource.go`          | founder  | PLANNED |
| Domain (`works.*`)| doc + constant exports             | `docs/standards/mappings/semconv-works.md`, `services/observability/semconv.go` | founder | PLANNED |
| CI lock          | attribute presence assertion         | `tests/observability/semconv_test.go`         | founder  | PLANNED |

---

## 3. OpenMetrics

- **Standard.** `openmetrics` 1.0 — vendor-neutral text metrics exposition
  format (`application/openmetrics-text; version=1.0.0`, terminated with
  `# EOF`, supports counter/gauge/histogram/gaugehistogram/summary/info/stateset).
- **Registry row.** `standard_id == "openmetrics"`,
  `control_id == "OPENMETRICS"`, `status == "PLANNED"`,
  `implementation == "Slice 3: /metrics endpoint exposes OpenMetrics format."`.
- **Applicability.** **In scope.** works-execution needs a scrapable metrics
  endpoint for Prometheus, VictoriaMetrics, Mimir, and OpenTelemetry Collector
  (with the `prometheus` receiver). OpenMetrics is the canonical content type
  all modern scrapers prefer.
- **System requirements mapped.** From `OBSERVABILITY.md`: queue depth/age,
  worker capacity/utilization/churn, cache hit/miss/latency, artifact transfer,
  scheduling decision reason counts, failure class counts, cost attribution
  per tenant — all must be scrapable. The SLO from `SLOS_AND_SRE.md`
  (control-plane availability, scheduling P95, lost-worker detection) requires
  histograms on the API request path and the lease-reaper.
- **Current status.** PLANNED. No `/metrics` endpoint exists.
- **Gap.** The OTel SDK can export via OTLP, but operators routinely want a
  Prometheus-style scrape endpoint for tools that do not yet support OTLP
  metrics. Without it, even a deployed works-execution cluster is "dark" to
  Prometheus.
- **Concrete next step.**
  1. Add `github.com/prometheus/client_golang/prometheus` and
     `github.com/prometheus/common` to `go.mod`. (Note: OTel SDK's own
     `prometheus` exporter is also an option; client_golang is chosen here
     because it natively speaks OpenMetrics text.)
  2. Create `services/observability/metrics_endpoint.go` with `Handler()` that
     does **Prometheus content negotiation**: inspect the `Accept` header and
     reply with `application/openmetrics-text; version=1.0.0` when the scraper
     prefers OpenMetrics, else `text/plain; version=0.0.4` (see §4). Always
     terminate OpenMetrics bodies with `# EOF` per spec.
  3. Register the `Handler()` at `GET /metrics` in `services/api/api.go`
     (outside auth — same convention as Kubernetes).
  4. Create `tests/observability/metrics_endpoint_test.go` asserting (a)
     `curl -H 'Accept: application/openmetrics-text; version=1.0.0' /metrics`
     returns content with `# EOF`, (b) every metric from the
     [Pack-Mandated Metrics Table](#pack-mandated-metrics--otel-mapping)
     below is present, (c) each metric has a `# HELP` and `# TYPE` line.
  File paths: `services/observability/metrics_endpoint.go` (new),
  `services/api/api.go` (route registration),
  `tests/observability/metrics_endpoint_test.go` (new).
- **Risk / leverage.** Medium-high leverage. Critical for Prometheus-era
  observability tooling and unlocks the SLO evidence path.

### Traceability — OpenMetrics

| Requirement                              | System element                       | File                                                | Owner    | Status  |
|------------------------------------------|--------------------------------------|-----------------------------------------------------|----------|---------|
| `/metrics` endpoint                      | HTTP route                           | `services/api/api.go`                               | founder  | PLANNED |
| Content negotiation (OM preferred)       | Accept-header parser                 | `services/observability/metrics_endpoint.go`        | founder  | PLANNED |
| `# EOF` terminator                       | body writer                          | `services/observability/metrics_endpoint.go`        | founder  | PLANNED |
| `# HELP` + `# TYPE` per metric           | metric registration                  | `services/observability/metrics_endpoint.go`        | founder  | PLANNED |
| Endpoint contract test                   | e2e + scrape                         | `tests/observability/metrics_endpoint_test.go`      | founder  | PLANNED |

---

## 4. Prometheus Exposition

- **Standard.** `prometheus-exposition` — Prometheus text format (current
  versions `0.0.4` and `1.0.0`). Content types
  `text/plain; version=0.0.4` and `text/plain; version=1.0.0; escaping=allow-utf-8`
  per Prometheus content negotiation.
- **Registry row.** `standard_id == "prometheus-exposition"`,
  `control_id == "PROM-EXPOSITION"`, `status == "PLANNED"`,
  `implementation == "Compatible with OpenMetrics endpoint."`.
- **Applicability.** **In scope — same endpoint as §3.** Prometheus
  exposition is the older wire format that every Prometheus version since
  v0.4.0 understands. OpenMetrics is the newer superset. The /metrics
  endpoint MUST serve both because Prometheus ≥ 3.0 requires a valid
  `Content-Type` header and content-negotiates which to use.
- **System requirements mapped.** Same metrics as §3. Naming must follow
  Prometheus conventions: `[a-zA-Z_:][a-zA-Z0-9_:]*`, `_total` suffix for
  counters, `_seconds`/`_bytes` suffixes for unit-suffixed metrics, snake_case
  labels.
- **Current status.** PLANNED. No exposition endpoint exists.
- **Gap.** Same as §3 — Prometheus is the de facto scrape target. Coverage
  requires both text formats plus OpenMetrics.
- **Concrete next step.** Implement in the **same** endpoint as §3
  (`services/observability/metrics_endpoint.go`) via content negotiation —
  no separate code path. Specifically:
  1. On `Accept: text/plain; version=1.0.0` → reply `text/plain; version=1.0.0; escaping=allow-utf-8`.
  2. On `Accept: text/plain; version=0.0.4` → reply `text/plain; version=0.0.4`.
  3. On `Accept: application/openmetrics-text; version=1.0.0` → reply
     `application/openmetrics-text; version=1.0.0`.
  4. Fallback to `text/plain; version=0.0.4`.
  5. Counter names get `_total` suffix when serialized in Prometheus text
     format (OpenMetrics spec omits the suffix on the wire but it MUST be
     added in Prometheus exposition).
  File paths: `services/observability/metrics_endpoint.go` (same file as §3),
  `tests/observability/metrics_endpoint_test.go` (add Prometheus-text-0.0.4
  assertion), `services/api/api.go`.
- **Risk / leverage.** Medium leverage — covered together with §3. Two
  formats, one handler.

### Traceability — Prometheus Exposition

| Requirement                              | System element                       | File                                                | Owner    | Status  |
|------------------------------------------|--------------------------------------|-----------------------------------------------------|----------|---------|
| `text/plain; version=0.0.4`             | content-neg branch                   | `services/observability/metrics_endpoint.go`        | founder  | PLANNED |
| `text/plain; version=1.0.0`             | content-neg branch                   | `services/observability/metrics_endpoint.go`        | founder  | PLANNED |
| `_total` counter suffix                  | name transformer                     | `services/observability/metrics_endpoint.go`        | founder  | PLANNED |
| Prometheus ≥ 3.0 scrape OK              | Content-Type header                  | `services/observability/metrics_endpoint.go`        | founder  | PLANNED |
| Endpoint contract test (both formats)    | e2e                                  | `tests/observability/metrics_endpoint_test.go`      | founder  | PLANNED |

---

## 5. OpenTracing Concepts (legacy)

- **Standard.** `opentracing-concepts` — pre-OpenTelemetry tracing
  vocabulary (`io.opentracing.Tracer`, `SpanContext`, `Format.Propagation.Header`).
- **Registry row.** `standard_id == "opentracing-concepts"`,
  `control_id == "OPENTRACING"`, `status == "NOT_APPLICABLE"`,
  `implementation == "Superseded by OpenTelemetry; covered there."`,
  `exceptions == ["Use OpenTelemetry directly."]`.
- **Applicability.** **NOT_APPLICABLE** — OpenTracing was merged into
  OpenTelemetry in 2021 and the project was archived. The CNCF itself
  recommends OTel for any new implementation.
- **System requirements mapped.** None. Trace context propagation (the only
  surviving concern) is provided by W3C Trace Context in §1.
- **Current status.** NOT_APPLICABLE (registry-confirmed).
- **Gap.** None — but we must record the explicit rationale so a future
  contributor who finds an `io.opentracing` import in an old tutorial knows
  why it was rejected.
- **Concrete next step.** Append to `docs/standards/RATIONALE_LOG.md`
  (PLANNED) one paragraph: "OpenTracing was merged into OpenTelemetry
  (CNCF, 2021). All tracing in works-execution uses the OTel SDK directly;
  the opentracing-go shim is forbidden in `go.mod` to prevent silent
  re-introduction." Add a `forbidigo` lint rule in
  `services/observability/lint_test.go` that fails CI on any
  `go.opentelemetry.io/contrib/instrumentation/.../opentracing` or
  `github.com/opentracing/opentracing-go` import.
  File paths: `docs/standards/RATIONALE_LOG.md` (new), `services/observability/lint_test.go` (new).
- **Risk / leverage.** Zero risk — but a one-line lint rule prevents the
  legacy library from sneaking back in via a tutorial copy-paste.

### Traceability — OpenTracing (legacy)

| Requirement                              | System element                       | File                                       | Owner    | Status         |
|------------------------------------------|--------------------------------------|--------------------------------------------|----------|----------------|
| Explicit NOT_APPLICABLE rationale        | rationale log                        | `docs/standards/RATIONALE_LOG.md`          | founder  | PLANNED        |
| Forbid legacy imports                    | CI lint                              | `services/observability/lint_test.go`      | founder  | PLANNED        |

---

## 6. OpenFeature

- **Standard.** `openfeature` — vendor-neutral feature-flag evaluation API
  with provider model, evaluation context, and hook system.
- **Registry row.** `standard_id == "openfeature"`,
  `control_id == "OPENFEATURE"`, `status == "PLANNED"`,
  `implementation == "Slice 4+: feature flags for staged rollouts."`.
- **Applicability.** **In scope, but deferred to slice 4.** Slice 1 + slice 2
  do not yet ship anything that benefits from runtime flag evaluation
  (max-attempts, scheduler heuristics, cache TTLs all compile-time). The
  scheduler-design doc (`02_ARCHITECTURE/SCHEDULER_DESIGN.md`) and
  cache-and-CAS doc (`02_ARCHITECTURE/CACHE_AND_CAS.md`) flag exactly the
  knobs that *will* become flags in slice 4+: scheduler scoring weights,
  cache TTL per layer, lease TTL override, max-attempts override.
- **System requirements mapped.** None for slice 1–3. For slice 4+:
  - Boolean flags for staged rollout of new policies.
  - String/number flags for tunable parameters (lease TTL, retry backoff).
  - Evaluation context carries `tenant.id`, `pool`, `trust_class`.
- **Current status.** PLANNED. Slice 1 + 2 ship without flags; the registry
  marks this PLANNED precisely because it does not block the current slices.
- **Gap.** No provider plumbing. If a slice-3 caller adds an
  `if config.GetBool("feature.x")` hardcode, it bypasses the evaluation
  context and audit story.
- **Concrete next step (slice 4 prep).** In slice 3, create the skeleton
  package `services/featureflags/provider.go` exposing a single
  `NoopProvider` (returns `flag value` as-is, no I/O) so slice 3 callers can
  import the type without depending on a real backend. Wire slice-4's
  actual provider (likely `flagd` via gRPC or an in-memory map for tests).
  Add a CI test in `tests/featureflags/openfeature_test.go` that asserts the
  no-op provider conforms to the OpenFeature SDK contract (Boolean, String,
  Integer, Float, Object evaluation, plus hooks).
  File paths: `services/featureflags/provider.go` (new, slice 3 skeleton),
  `services/featureflags/noop.go` (new),
  `tests/featureflags/openfeature_test.go` (new, slice 4).
- **Risk / leverage.** Medium leverage — keeps slice 4 from being a flag
  retrofit. Slice 1 + 2 do not need this.

### Traceability — OpenFeature

| Requirement                              | System element                       | File                                                | Owner    | Status  |
|------------------------------------------|--------------------------------------|-----------------------------------------------------|----------|---------|
| Skeleton provider (slice 3)              | NoopProvider                         | `services/featureflags/noop.go`                     | founder  | PLANNED |
| Typed API surface                        | Go interface aligned with SDK        | `services/featureflags/provider.go`                 | founder  | PLANNED |
| Provider conformance test                | Conformance suite                    | `tests/featureflags/openfeature_test.go`            | founder  | PLANNED |
| Real provider (slice 4)                  | flagd/in-memory                      | `services/featureflags/flagd.go`                    | founder  | PLANNED |
| Evaluation context                       | tenant/pool/trust                    | `services/featureflags/context.go`                  | founder  | PLANNED |

---

## Pack-Mandated Metrics → OTel Mapping

This table is the contract between the starter-pack telemetry requirements
(`05_OPERATIONS/OBSERVABILITY.md`) and the OpenTelemetry metric namespace
declared in `services/observability/semconv.go`. Every metric the pack
calls out maps to a concrete OTel instrument name, instrument type, unit
(UCUM), and the code site that records it.

> Naming follows OTel semantic conventions where stable
> ([HTTP metrics](https://opentelemetry.io/docs/specs/semconv/http/http-metrics),
> [DB metrics](https://opentelemetry.io/docs/specs/semconv/database/database-metrics),
> [process metrics](https://opentelemetry.io/docs/specs/semconv/system/process-metrics)),
> and follows the OTel custom-metric naming rule (`<domain>.<subject>.<verb>`,
> snake_case) for the `works.*` namespace. Counter instruments receive the
> `_total` suffix in Prometheus exposition (see §4).

| #  | Pack requirement (`OBSERVABILITY.md` line) | OTel metric name                                  | Type             | Unit (UCUM) | Recorded at                                              | Attributes                                       |
|----|--------------------------------------------|---------------------------------------------------|------------------|-------------|----------------------------------------------------------|--------------------------------------------------|
| 1  | Work / node / attempt transitions          | `works.work.transitions.total`                    | Counter          | `{transition}` | `services/work/store/store.go` (transition logger)   | `works.work.state.from`, `works.work.state.to`, `works.work.id` |
| 2  | Queue depth                                | `works.queue.depth`                               | UpDownCounter    | `{work}`     | `services/work/store/leases.go` (inc on enqueue, dec on grant) | `works.pool`, `works.tenant.id`                 |
| 3  | Queue age (SLO: scheduling P95 < 1 s)      | `works.queue.wait.duration`                       | Histogram        | `s`          | `services/work/store/leases.go` (timer around GrantLease) | `works.pool`, `works.tenant.id`                 |
| 4  | Worker capacity                            | `works.worker.capacity`                           | UpDownCounter    | `{worker}`   | `internal/worker/worker.go` (heartbeat)                | `works.pool`, `works.trust_class`                |
| 5  | Worker utilization                         | `works.worker.utilization`                        | Gauge            | `1` (ratio)  | `internal/worker/worker.go` (per-tick)                  | `works.pool`, `works.worker.id`                  |
| 6  | Worker churn                               | `works.worker.lifetime.duration`                  | Histogram        | `s`          | `internal/worker/worker.go` (defer on shutdown)         | `works.pool`, `works.worker.id`                  |
| 7  | Scheduling decision reasons                | `works.scheduler.decisions.total`                 | Counter          | `{decision}` | `internal/worker/worker.go` + scheduler (slice 3+)     | `works.scheduler.decision`, `works.scheduler.reason` |
| 8  | Cache hit / miss                           | `works.cache.requests.total`                      | Counter          | `{request}`  | `services/work/store/cache.go` (slice 4+)              | `works.cache.layer` (l1\|l2\|l3), `works.cache.result` (hit\|miss\|stale) |
| 9  | Cache latency                              | `works.cache.operation.duration`                  | Histogram        | `s`          | `services/work/store/cache.go` (slice 4+)              | `works.cache.layer`, `works.cache.result`        |
| 10 | Artifact transfer                          | `works.artifact.transfer.duration`                | Histogram        | `s`          | `services/work/store/artifact.go` (slice 4+)           | `works.artifact.direction` (upload\|download), `works.artifact.size_bucket` |
| 11 | Artifact transfer size                     | `works.artifact.transfer.size`                    | Histogram        | `By`         | `services/work/store/artifact.go` (slice 4+)           | `works.artifact.direction`                      |
| 12 | Failure classification                     | `works.failures.total`                            | Counter          | `{failure}`  | `internal/worker/worker.go` (catch block)               | `works.failure.class`, `works.failure.remediation` |
| 13 | Remediation outcome                        | `works.remediation.attempts.total`                | Counter          | `{attempt}`  | `internal/worker/worker.go` (remediation hook)         | `works.remediation.action`, `works.remediation.result` |
| 14 | Cost attribution                           | `works.cost.units.total`                          | Counter          | `{unit}`     | `internal/worker/worker.go` (per-attempt cost)          | `works.tenant.id`, `works.pool`, `works.work.kind` |
| 15 | Critical path duration                     | `works.work.critical_path.duration`               | Histogram        | `s`          | `services/work/store/store.go` (work SUCCEEDED)        | `works.work.id`, `works.work.kind`               |
| 16 | External dependency health (DB)           | `db.client.operation.duration`                    | Histogram        | `s`          | `services/work/store/{store,leases}.go` (wraps every query) | `db.system=sqlite`, `db.collection.name`, `db.operation.name` |
| 17 | DB pool in-use                             | `db.client.connections.usage`                     | UpDownCounter    | `{connection}` | `services/work/store/store.go` (per query)            | `db.system=sqlite`, `db.pool.name`               |
| 18 | External dependency health (HTTP API)     | `http.server.request.duration`                    | Histogram        | `s`          | `services/api/api.go` (otelhttp middleware)            | `http.request.method`, `http.route`, `http.response.status_code` |
| 19 | Concurrent HTTP requests                   | `http.server.active_requests`                     | UpDownCounter    | `{request}`  | `services/api/api.go` (otelhttp middleware)            | `http.request.method`, `url.scheme`              |
| 20 | HTTP server throughput                     | `http.server.requests.total` (Prometheus: `http_server_requests_total`) | Counter | `{request}` | `services/api/api.go` (otelhttp middleware) | `http.request.method`, `http.route`, `http.response.status_code` |
| 21 | Lost-worker detection latency (SLO: < 30 s) | `works.lease.detection.duration`                  | Histogram        | `s`          | `services/work/store/leases.go` (reaper logs time-since-grant) | `works.pool`, `works.lease.outcome`            |
| 22 | Lease grant conflicts                      | `works.lease.conflicts.total`                     | Counter          | `{conflict}` | `services/work/store/leases.go`                        | `works.pool`                                     |
| 23 | Process CPU                                | `process.cpu.time`                                | Counter          | `s`          | `services/observability/runtime.go` (process collector) | (none)                                          |
| 24 | Process memory (RSS)                       | `process.memory.usage`                            | UpDownCounter    | `By`         | `services/observability/runtime.go` (process collector) | (none)                                          |
| 25 | Go runtime GC pause                       | `go.gc.duration`                                  | Histogram        | `s`          | `services/observability/runtime.go` (Go runtime collector) | (none)                                          |
| 26 | Go goroutine count                         | `go.goroutine.count`                              | UpDownCounter    | `{goroutine}`| `services/observability/runtime.go` (Go runtime collector) | (none)                                          |
| 27 | OpenTelemetry Collector scrape health (slice 3+, OTLP) | `otelcol_exporter_sent_spans_total` (Collector-side; we record nothing on the app side) | — | — | n/a | n/a |
| 28 | Audit-event loss (SLO: 0 acknowledged)    | `works.audit.events.dropped.total`                | Counter          | `{event}`    | `services/observability/audit.go` (drop counter)        | `works.audit.reason`                            |
| 29 | DORA — deployment frequency                | `works.dora.deployments.total`                    | Counter          | `{deployment}` | `services/deploy/recorder.go` (slice 3+)             | `works.dora.env`, `works.dora.service`           |
| 30 | DORA — lead time for changes               | `works.dora.lead_time.duration`                   | Histogram        | `s`          | `services/deploy/recorder.go` (slice 3+)               | `works.dora.env`, `works.dora.service`           |
| 31 | DORA — change failure rate                 | `works.dora.change_failures.total`                | Counter          | `{failure}`  | `services/deploy/recorder.go` (slice 3+)               | `works.dora.env`, `works.dora.service`           |
| 32 | DORA — MTTR                                | `works.dora.mttr.duration`                        | Histogram        | `s`          | `services/deploy/recorder.go` (slice 3+)               | `works.dora.env`, `works.dora.service`           |

> **Notes.**
> - Items 1–17 are required by `OBSERVABILITY.md` and must ship in slice 3.
> - Items 18–20 are required by `SLOS_AND_SRE.md` (HTTP control-plane
>   availability / P95) and ship for free with otelhttp instrumentation.
> - Items 21–22 are slice-2 invariants that the slice-2 chaos test
>   (`e2e/chaos_test.go`) currently checks structurally; the histogram and
>   counter turn the assertion into continuous telemetry.
> - Items 23–26 are auto-instrumented by the OTel process/runtime collectors
>   and free us from hand-coding CPU/memory/GC counters.
> - Item 28 backs the SLO "Acknowledged audit-event loss: 0" — the only SLO
>   expressed as a hard zero — by emitting a counter that any alert
>   (`works.audit.events.dropped.total > 0`) can fire on.
> - Items 29–32 cover the `dora-metrics` registry row (domain `performance`)
>   and share the OTel pipeline from §1.

---

## Cross-Standard Traceability (cross-cutting view)

This view shows which observability row satisfies which requirement sourced
from the starter pack, the registry, or the security domain. It exists to
catch omissions: every "OTel" cross-reference here must be implemented in
the single `services/observability/` package so we do not fragment the
telemetry surface.

| Requirement                                                | Sourced from                                                                  | Satisfied by                       | File path                                                  |
|------------------------------------------------------------|-------------------------------------------------------------------------------|------------------------------------|------------------------------------------------------------|
| Work / node / attempt transitions logged                   | `OBSERVABILITY.md`                                                            | OT-1 + OT-2 + OT-3 + PE-4          | `services/observability/{tracing,metrics_endpoint,semconv}.go` |
| Control-plane availability 99.9%                           | `SLOS_AND_SRE.md`                                                             | OT-1 + PE-4 (`http.server.request.duration`, `http.server.requests.total`) | `services/api/api.go`, `services/observability/metrics.go` |
| Scheduling P95 < 1 s                                       | `SLOS_AND_SRE.md`                                                             | OT-1 + PE-4 (`works.queue.wait.duration`) | `services/work/store/leases.go`                            |
| Lost-worker detection < 30 s                               | `SLOS_AND_SRE.md`                                                             | OT-1 + PE-4 (`works.lease.detection.duration`) | `services/work/store/leases.go`                            |
| Audit-event loss = 0                                       | `SLOS_AND_SRE.md`, `cloudevents` row                                          | OT-1 + OT-2 (`works.audit.events.dropped.total`) | `services/observability/audit.go`                          |
| Detect function (NIST CSF)                                  | `nist-csf-2.0` Detect                                                         | OT-1 + OM-3 + PE-4                 | `services/observability/{tracing,metrics_endpoint}.go`     |
| Audit (CIS Controls v8 #8)                                | `cis-controls-v8`                                                              | OT-1 + OM-3                        | `services/observability/audit.go`                          |
| Feature-flag staging for slice 4+ rollout                  | `OBSERVABILITY.md` (remediation outcome) + `OPENFEATURE` row                 | OF-6                                | `services/featureflags/{provider,noop}.go`                 |
| DORA metrics for design partners                            | `dora-metrics` row                                                            | OT-1 + OT-2 (`works.dora.*`)       | `services/deploy/recorder.go`                              |

---

## Open items (status: PLANNED) and next-action order

Ordered by risk × leverage (highest first):

1. **Author `services/observability/` package** — §1, the spine. Unblocks §2,
   §3, §4, `cloudevents`, `dora-metrics`, `nist-csf-2.0` Detect.
2. **Author `services/observability/semconv.go` + `docs/standards/mappings/semconv-works.md`** — §2,
   the attribute schema. Locks the wire contract.
3. **Implement `/metrics` endpoint with content negotiation** — §3 + §4
   together (one handler).
4. **Record `works.audit.events.dropped.total`** — §1 follow-up, satisfies
   the only "loss = 0" SLO and the `cloudevents` audit row.
5. **Append `RATIONALE_LOG.md` + forbidigo lint** — §5, prevents regression.
6. **Slice 3 skeleton for `services/featureflags/noop.go`** — §6, does not
   block slice 3 but keeps slice 4 from being a retrofit.

All items above are PLANNED, none BLOCKED, none depend on external counsel
or audit bodies.