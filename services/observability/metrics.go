// Package observability ships the slice-4 metrics surface for
// works-execution. It is intentionally hand-rolled — no OpenTelemetry
// dependency yet — so the 13 pack-mandated metrics from
// docs/works-venture-starter-pack/05_OPERATIONS/OBSERVABILITY.md (mapped to
// concrete OTel names in docs/standards/mappings/observability.md §
// "Pack-Mandated Metrics → OTel Mapping", items 1-13) can be scraped at
// GET /metrics in Prometheus text format. OTel tracing arrives in a
// later slice.
//
// The package has three layers:
//
//  1. Counter / Histogram / Gauge primitives (this file). Each is goroutine
//     safe, owns its own state, and implements the Metric interface so it
//     can be emitted into the Prometheus exposition format.
//  2. Registry (this file) — collects typed metric instances under names,
//     preventing duplicate registration.
//  3. Collector (collector.go) — pulls derived state (queue depth, lease
//     conflicts) from the store on each scrape and feeds the registered
//     gauges / counters.
//
// Naming follows the OTel custom-metric rule: snake_case, `<domain>.
// <subject>.<verb>`, with `_total` suffix applied to counters on the wire
// per Prometheus conventions.
package observability

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MetricType is the Prometheus exposition type.
type MetricType string

const (
	TypeCounter   MetricType = "counter"
	TypeGauge     MetricType = "gauge"
	TypeHistogram MetricType = "histogram"
)

// Sample is a single point-in-time reading of a metric. Counter samples
// carry the cumulative value; Histogram samples carry buckets and sum.
type Sample struct {
	Name   string
	Help   string
	Type   MetricType
	Unit   string // UCUM unit; e.g. "s", "By", "{work}"; empty if none.
	Labels map[string]string
	Value  float64
}

// SampleHistogram is the rendering of a Histogram at scrape time.
type SampleHistogram struct {
	Name    string
	Help    string
	Unit    string
	Labels  map[string]string // metric-level labels (shared across buckets/sum)
	Buckets []BucketSample
	Sum     float64
	Count   uint64
}

// BucketSample is one (le, count) bucket from a Histogram.
type BucketSample struct {
	LE    float64 // upper bound, inclusive; +Inf represented by math.Inf
	Count uint64
}

// Metric is anything that can be written to a Prometheus exposition stream.
// Render returns the lines to emit; WriteText writes the same lines to w.
type Metric interface {
	// Name returns the registered metric name (no _total suffix).
	Name() string
	// WriteText writes the full Prometheus text-format block for this metric
	// to w, including HELP and TYPE lines.
	WriteText(w io.Writer) error
}

// Counter is a monotonically increasing value. V1 is uint64; the spec
// reserves negative increments (no-op) and reset behavior for testing.
type Counter struct {
	name, help, unit string
	labels            []string // label keys, in stable order

	mu  sync.Mutex
	val uint64
}

// NewCounter constructs a Counter. labels is the ordered list of label
// names (Prometheus requires stable ordering for the same metric). Pass an
// empty slice for an unlabeled counter.
func NewCounter(name, help, unit string, labels []string) *Counter {
	return &Counter{name: name, help: help, unit: unit, labels: append([]string(nil), labels...)}
}

func (c *Counter) Name() string { return c.name }

// Inc adds 1 to the counter.
func (c *Counter) Inc() { c.Add(1) }

// Add adds n to the counter. Negative values are ignored (Prometheus
// counters are monotonic). n=0 is a no-op.
func (c *Counter) Add(n uint64) {
	if n == 0 {
		return
	}
	c.mu.Lock()
	c.val += n
	c.mu.Unlock()
}

// Value returns the current value.
func (c *Counter) Value() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val
}

// WithLabelValues is a placeholder for future label-aware counters; in V1
// all counters are unlabeled by design (slice 4 budget). Returns the
// counter itself so callers can use the same fluent API the OTel SDK will
// provide later.
func (c *Counter) WithLabelValues(_ ...string) *Counter { return c }

// WriteText renders the counter in Prometheus text format. Counters get
// the `_total` suffix on the wire (per Prometheus exposition convention).
func (c *Counter) WriteText(w io.Writer) error {
	return writeText(w, Sample{
		Name:   promName(c.name, TypeCounter),
		Help:   c.help,
		Type:   TypeCounter,
		Unit:   c.unit,
		Value:  float64(c.Value()),
	})
}

// Gauge is a value that can go up or down. V1 is float64.
type Gauge struct {
	name, help, unit string
	labels            []string

	mu  sync.Mutex
	val float64
}

// NewGauge constructs a Gauge.
func NewGauge(name, help, unit string, labels []string) *Gauge {
	return &Gauge{name: name, help: help, unit: unit, labels: append([]string(nil), labels...)}
}

func (g *Gauge) Name() string { return g.name }

// Set replaces the gauge value.
func (g *Gauge) Set(v float64) {
	g.mu.Lock()
	g.val = v
	g.mu.Unlock()
}

// Add adds delta to the gauge (negative subtracts).
func (g *Gauge) Add(delta float64) {
	g.mu.Lock()
	g.val += delta
	g.mu.Unlock()
}

// Value returns the current value.
func (g *Gauge) Value() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.val
}

// WithLabelValues is the V1 placeholder — see Counter.WithLabelValues.
func (g *Gauge) WithLabelValues(_ ...string) *Gauge { return g }

// WriteText renders the gauge in Prometheus text format.
func (g *Gauge) WriteText(w io.Writer) error {
	return writeText(w, Sample{
		Name:  promName(g.name, TypeGauge),
		Help:  g.help,
		Type:  TypeGauge,
		Unit:  g.unit,
		Value: g.Value(),
	})
}

// Histogram is a bucketed observation. Default buckets are tuned for the
// works-execution latency surface (1ms → 10s); callers can override.
type Histogram struct {
	name, help, unit string
	labels            []string
	buckets           []float64 // upper bounds, ascending, must include +Inf conceptually

	mu       sync.Mutex
	counts   []uint64 // counts[i] = #observations with value <= buckets[i]
	sum      float64
	count    uint64
}

// DefaultBuckets are the recommended histogram buckets in seconds for
// latency histograms (queue wait, worker lifetime, cache, artifact).
var DefaultBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// NewHistogram constructs a Histogram with the given buckets (upper
// bounds, ascending, finite). +Inf is appended automatically.
func NewHistogram(name, help, unit string, labels []string, buckets []float64) *Histogram {
	if len(buckets) == 0 {
		buckets = append([]float64(nil), DefaultBuckets...)
	} else {
		buckets = append([]float64(nil), buckets...)
	}
	return &Histogram{
		name:    name,
		help:    help,
		unit:    unit,
		labels:  append([]string(nil), labels...),
		buckets: buckets,
		counts:  make([]uint64, len(buckets)),
	}
}

func (h *Histogram) Name() string { return h.name }

// Observe records a single observation.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += v
	for i, ub := range h.buckets {
		if v <= ub {
			h.counts[i]++
		}
	}
}

// ObserveDuration is a convenience wrapper around Observe.
func (h *Histogram) ObserveDuration(d time.Duration) {
	h.Observe(d.Seconds())
}

// Snapshot returns the current counts/sum/count for the histogram.
func (h *Histogram) Snapshot() SampleHistogram {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := SampleHistogram{
		Name:   h.name,
		Help:   h.help,
		Unit:   h.unit,
		Labels: map[string]string{},
		Buckets: make([]BucketSample, len(h.buckets)),
		Sum:    h.sum,
		Count:  h.count,
	}
	for i, ub := range h.buckets {
		out.Buckets[i] = BucketSample{LE: ub, Count: h.counts[i]}
	}
	return out
}

// WithLabelValues is the V1 placeholder — see Counter.WithLabelValues.
func (h *Histogram) WithLabelValues(_ ...string) *Histogram { return h }

// WriteText renders the histogram in Prometheus text format, including
// _bucket, _sum, _count suffixes and the +Inf bucket.
func (h *Histogram) WriteText(w io.Writer) error {
	snap := h.Snapshot()
	snap.Name = promName(snap.Name, TypeHistogram)
	// HELP + TYPE.
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n", snap.Name, escapeHelp(snap.Help)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE %s %s\n", snap.Name, TypeHistogram); err != nil {
		return err
	}
	// _bucket lines (in ascending le order).
	for _, b := range snap.Buckets {
		if _, err := fmt.Fprintf(w, "%s_bucket%s %g\n",
			snap.Name, leLabel(b.LE), float64(b.Count)); err != nil {
			return err
		}
	}
	// +Inf bucket = total count.
	if _, err := fmt.Fprintf(w, "%s_bucket%s %g\n",
		snap.Name, leLabelPosInf(), float64(snap.Count)); err != nil {
		return err
	}
	// _sum + _count.
	if _, err := fmt.Fprintf(w, "%s_sum %s\n", snap.Name, formatFloat(snap.Sum)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s_count %g\n", snap.Name, float64(snap.Count)); err != nil {
		return err
	}
	return nil
}

// Registry is a name-indexed collection of metrics. It enforces that a
// name is registered exactly once; double-registration panics, matching
// the Prometheus client_golang contract.
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]Metric
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{metrics: map[string]Metric{}}
}

// MustRegister registers a metric; panics on duplicate name. The caller is
// expected to hold a typed reference to the returned metric anyway, so
// returning the input is the most ergonomic API.
func (r *Registry) MustRegister(m Metric) Metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.metrics[m.Name()]; ok {
		panic(fmt.Sprintf("observability: metric %q already registered", m.Name()))
	}
	r.metrics[m.Name()] = m
	return m
}

// Get returns a previously-registered metric by name, or nil if absent.
func (r *Registry) Get(name string) Metric {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.metrics[name]
}

// Gather returns all registered metrics in stable name order. Determinism
// makes the scrape output diffable, which matters for tests and for
// audit-style reproducibility.
func (r *Registry) Gather() []Metric {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.metrics))
	for n := range r.metrics {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Metric, 0, len(names))
	for _, n := range names {
		out = append(out, r.metrics[n])
	}
	return out
}

// WriteText writes all registered metrics in Prometheus text format to w.
func (r *Registry) WriteText(w io.Writer) error {
	for _, m := range r.Gather() {
		if err := m.WriteText(w); err != nil {
			return fmt.Errorf("write %s: %w", m.Name(), err)
		}
	}
	return nil
}

// promName returns the wire name. Per the Prometheus exposition spec,
// metric names must match `[a-zA-Z_:][a-zA-Z0-9_:]*` — dots are NOT valid
// on the wire even though OpenMetrics-text permits them. We translate the
// OTel-style dot namespace (`works.work.transitions`) into the canonical
// underscore form (`works_work_transitions`). Counters get the `_total`
// suffix per Prometheus convention.
func promName(name string, t MetricType) string {
	wire := strings.ReplaceAll(name, ".", "_")
	if t == TypeCounter && !strings.HasSuffix(wire, "_total") {
		return wire + "_total"
	}
	return wire
}

// writeText renders one Sample block (used by Counter / Gauge).
func writeText(w io.Writer, s Sample) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n", s.Name, escapeHelp(s.Help)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE %s %s\n", s.Name, s.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s %s\n", s.Name, formatFloat(s.Value)); err != nil {
		return err
	}
	return nil
}

// escapeHelp escapes backslashes and newlines in HELP text per the
// Prometheus exposition spec.
func escapeHelp(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	return r.Replace(s)
}

// formatFloat formats a float64 in Prometheus exposition form. We use
// strconv.FormatFloat with 'g' to mirror client_golang behavior: integers
// like 1 render as "1", 10 renders as "10", 0.5 renders as "0.5". No
// trailing-zero stripping — that breaks le="10" by collapsing it to "1".
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// leLabel formats the `le="..."` label suffix for histogram buckets.
// Per spec: 1 → "1", 0.5 → "0.5", +Inf → "+Inf".
func leLabel(le float64) string {
	if le != le { // NaN
		return `{le="NaN"}`
	}
	return fmt.Sprintf(`{le="%s"}`, formatFloat(le))
}

func leLabelPosInf() string { return `{le="+Inf"}` }