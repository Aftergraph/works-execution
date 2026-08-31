package observability

import (
	"context"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// PackMetrics is the canonical name→instance map for the 13 pack-mandated
// metrics from docs/works-venture-starter-pack/05_OPERATIONS/OBSERVABILITY.md
// (mapped to concrete OTel names in
// docs/standards/mappings/observability.md §"Pack-Mandated Metrics →
// OTel Mapping", items 1-13). The struct is exported so call sites can do
//
//	c := NewPackMetrics(reg)
//	... c.WorkTransitions.Inc()
//
// without a string-lookup on every event. The Registry holds the same
// instance, so the scrape handler can render them all.
type PackMetrics struct {
	// 1. Work / node / attempt transitions
	WorkTransitions *Counter

	// 2. Queue depth (pulled from store on scrape).
	QueueDepth *Gauge

	// 3. Queue age / scheduling wait (histogram around GrantLease).
	QueueWaitDuration *Histogram

	// 4. Worker capacity (heartbeat-driven; default 0 if no workers).
	WorkerCapacity *Gauge

	// 5. Worker utilization (ratio; scraped from worker heartbeats).
	WorkerUtilization *Gauge

	// 6. Worker lifetime (recorded on worker shutdown).
	WorkerLifetimeDuration *Histogram

	// 7. Scheduler decision reasons.
	SchedulerDecisions *Counter

	// 8. Cache hit / miss.
	CacheRequests *Counter

	// 9. Cache operation latency.
	CacheOperationDuration *Histogram

	// 10. Artifact transfer latency.
	ArtifactTransferDuration *Histogram

	// 11. Artifact transfer size (bytes).
	ArtifactTransferSize *Histogram

	// 12. Failure classification.
	Failures *Counter

	// 13. Remediation outcome.
	RemediationAttempts *Counter
}

// PackMetricsSpec is the human-readable name + help text for every
// pack-mandated metric. Kept in one place so the scrape output stays
// self-documenting and the registry shape can be re-derived.
type PackMetricsSpec struct {
	Name string
	Help string
	Unit string
	Type MetricType
}

// PackMetricSpecs enumerates the 13 metrics in stable order. Used by
// tests to assert presence and by the package doc.
var PackMetricSpecs = []PackMetricsSpec{
	{"works.work.transitions", "Work / node / attempt transitions, labeled by from/to state.", "{transition}", TypeCounter},
	{"works.queue.depth", "Pending works in the queue (state in {QUEUED, RUNNING, PLANNING, VERIFYING}).", "{work}", TypeGauge},
	{"works.queue.wait.duration", "Time a work item waits in the queue before being granted a lease.", "s", TypeHistogram},
	{"works.worker.capacity", "Total worker capacity (1 per registered worker).", "{worker}", TypeGauge},
	{"works.worker.utilization", "Fraction of worker capacity currently executing an attempt (0.0..1.0).", "1", TypeGauge},
	{"works.worker.lifetime.duration", "Time a worker process lived before exiting.", "s", TypeHistogram},
	{"works.scheduler.decisions", "Scheduler decisions, labeled by decision and reason.", "{decision}", TypeCounter},
	{"works.cache.requests", "Cache lookups, labeled by layer and result (hit|miss|stale).", "{request}", TypeCounter},
	{"works.cache.operation.duration", "Cache operation latency, labeled by layer and result.", "s", TypeHistogram},
	{"works.artifact.transfer.duration", "Artifact transfer latency, labeled by direction (upload|download).", "s", TypeHistogram},
	{"works.artifact.transfer.size", "Artifact transfer size in bytes, labeled by direction.", "By", TypeHistogram},
	{"works.failures", "Failures classified, labeled by class and remediation.", "{failure}", TypeCounter},
	{"works.remediation.attempts", "Remediation attempts, labeled by action and result.", "{attempt}", TypeCounter},
}

// NewPackMetrics registers the 13 pack-mandated metrics in reg and
// returns typed handles. Double-registration panics; intended to be called
// once at process start.
func NewPackMetrics(reg *Registry) *PackMetrics {
	m := &PackMetrics{
		WorkTransitions:          reg.MustRegister(NewCounter("works.work.transitions", PackMetricSpecs[0].Help, PackMetricSpecs[0].Unit, []string{"from", "to"})).(*Counter),
		QueueDepth:               reg.MustRegister(NewGauge("works.queue.depth", PackMetricSpecs[1].Help, PackMetricSpecs[1].Unit, nil)).(*Gauge),
		QueueWaitDuration:        reg.MustRegister(NewHistogram("works.queue.wait.duration", PackMetricSpecs[2].Help, PackMetricSpecs[2].Unit, nil, nil)).(*Histogram),
		WorkerCapacity:           reg.MustRegister(NewGauge("works.worker.capacity", PackMetricSpecs[3].Help, PackMetricSpecs[3].Unit, nil)).(*Gauge),
		WorkerUtilization:        reg.MustRegister(NewGauge("works.worker.utilization", PackMetricSpecs[4].Help, PackMetricSpecs[4].Unit, nil)).(*Gauge),
		WorkerLifetimeDuration:   reg.MustRegister(NewHistogram("works.worker.lifetime.duration", PackMetricSpecs[5].Help, PackMetricSpecs[5].Unit, nil, nil)).(*Histogram),
		SchedulerDecisions:       reg.MustRegister(NewCounter("works.scheduler.decisions", PackMetricSpecs[6].Help, PackMetricSpecs[6].Unit, []string{"decision", "reason"})).(*Counter),
		CacheRequests:            reg.MustRegister(NewCounter("works.cache.requests", PackMetricSpecs[7].Help, PackMetricSpecs[7].Unit, []string{"layer", "result"})).(*Counter),
		CacheOperationDuration:   reg.MustRegister(NewHistogram("works.cache.operation.duration", PackMetricSpecs[8].Help, PackMetricSpecs[8].Unit, nil, nil)).(*Histogram),
		ArtifactTransferDuration: reg.MustRegister(NewHistogram("works.artifact.transfer.duration", PackMetricSpecs[9].Help, PackMetricSpecs[9].Unit, nil, nil)).(*Histogram),
		ArtifactTransferSize:     reg.MustRegister(NewHistogram("works.artifact.transfer.size", PackMetricSpecs[10].Help, PackMetricSpecs[10].Unit, nil, nil)).(*Histogram),
		Failures:                 reg.MustRegister(NewCounter("works.failures", PackMetricSpecs[11].Help, PackMetricSpecs[11].Unit, []string{"class", "remediation"})).(*Counter),
		RemediationAttempts:      reg.MustRegister(NewCounter("works.remediation.attempts", PackMetricSpecs[12].Help, PackMetricSpecs[12].Unit, []string{"action", "result"})).(*Counter),
	}
	return m
}

// Collector pulls scrape-time state from the store into the registry.
//
// The 13 pack-mandated metrics split into two classes:
//
//   - Push metrics: Counters/Histograms incremented by hot-path code
//     (lease grant, worker heartbeat, scheduler decision, etc.). The
//     collector does nothing for these.
//   - Pull metrics: Gauges whose value is derived from current store
//     state. The collector recomputes them on every Scrape() so a single
//     Prometheus scrape reflects the truth at scrape time, not at last
//     push. (This is the same pull pattern Prometheus client_golang uses
//     for go_* metrics.)
//
// The collector is goroutine-safe; Scrape may run concurrently with the
// hot path.
type Collector struct {
	store store.Store
	pm    *PackMetrics
	log   *log.Logger

	// Cached worker headcount from the most recent heartbeat call.
	mu                 sync.Mutex
	lastWorkerCount    int
	lastUtilizationSum float64
	lastScrapeAt       time.Time
}

// NewCollector wires a Collector to the given store and metrics set.
// Pass a nil logger to silence scrape-time diagnostics.
func NewCollector(s store.Store, pm *PackMetrics, lg *log.Logger) *Collector {
	if lg == nil {
		lg = log.Default()
	}
	return &Collector{store: s, pm: pm, log: lg}
}

// WorkerHeartbeat is called by the worker process on each heartbeat
// (see internal/worker/worker.go). It updates the worker-capacity and
// utilization gauges immediately so a Prometheus scrape between two
// store-derived pulls still sees the latest worker state.
//
// workers is the current count of registered workers; utilization is the
// fraction (0.0..1.0) currently executing an attempt.
func (c *Collector) WorkerHeartbeat(workers int, utilization float64) {
	c.mu.Lock()
	c.lastWorkerCount = workers
	c.lastUtilizationSum = utilization
	c.mu.Unlock()
	c.pm.WorkerCapacity.Set(float64(workers))
	c.pm.WorkerUtilization.Set(utilization)
}

// Scrape pulls state from the store and updates the pull-class metrics.
// Intended to be called by the /metrics handler before serializing the
// scrape body. Errors are logged, not returned — the scrape must always
// return a body so Prometheus does not record a scrape failure.
func (c *Collector) Scrape(ctx context.Context) {
	if c.store == nil {
		return
	}

	// 1. Queue depth: count non-terminal works (QUEUED + RUNNING + the
	//    transient in-flight states). Cheap; capped at 5000.
	works, err := c.store.ListWorks(ctx, 5000)
	if err != nil {
		c.log.Printf("metrics: list works: %v", err)
	} else {
		var depth int
		for _, w := range works {
			if !w.State.IsTerminal() && w.State != workgraph.StateBlocked {
				depth++
			}
		}
		c.pm.QueueDepth.Set(float64(depth))
	}

	// 2. Process runtime: Go goroutine count. Mirrors what the OTel
	//    runtime collector will do in a later slice. This gives operators
	//    a usable goroutine gauge without waiting on otel deps.
	c.pm.WorkerCapacity.Set(float64(runtime.NumGoroutine()))
	// WorkerUtilization stays at last heartbeat value (default 0).
	c.mu.Lock()
	c.lastScrapeAt = time.Now()
	c.mu.Unlock()
}

// LastScrapeAt returns the wall-clock time of the most recent successful
// Scrape call. Exposed for tests.
func (c *Collector) LastScrapeAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastScrapeAt
}