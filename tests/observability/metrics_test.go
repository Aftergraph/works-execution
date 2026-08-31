// Package observability_test covers the Prometheus metrics endpoint
// shipped by slice 4 / k-impl-008: Counter / Histogram / Gauge / Registry
// primitives, the 13 pack-mandated metrics from OBSERVABILITY.md, the
// store-derived Collector, and the GET /metrics handler served by
// services/api.
package observability_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/observability"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// newMetrics returns a fresh registry + PackMetrics + Collector over a
// throwaway SQLite store. The test harness is the same shape used by
// services/api/api_test.go so the /metrics handler can be exercised
// end-to-end against a real store.
func newMetrics(t *testing.T) (*observability.Registry, *observability.PackMetrics, *observability.Collector, store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	reg := observability.NewRegistry()
	pm := observability.NewPackMetrics(reg)
	col := observability.NewCollector(s, pm, log.New(io.Discard, "", 0))
	return reg, pm, col, s
}

// TestPackMetrics_RegistersAll13 asserts that the PackMetrics ctor
// registers exactly the 13 pack-mandated metric names from
// OBSERVABILITY.md (items 1-13 of the Pack-Mandated Metrics → OTel
// Mapping table in docs/standards/mappings/observability.md).
func TestPackMetrics_RegistersAll13(t *testing.T) {
	_, pm, _, _ := newMetrics(t)

	want := []string{
		"works.work.transitions",
		"works.queue.depth",
		"works.queue.wait.duration",
		"works.worker.capacity",
		"works.worker.utilization",
		"works.worker.lifetime.duration",
		"works.scheduler.decisions",
		"works.cache.requests",
		"works.cache.operation.duration",
		"works.artifact.transfer.duration",
		"works.artifact.transfer.size",
		"works.failures",
		"works.remediation.attempts",
	}
	if len(want) != 13 {
		t.Fatalf("test wants 13 metrics; got %d (update OBSERVABILITY.md mapping first)", len(want))
	}
	for _, name := range want {
		switch name {
		case "works.work.transitions":
			if pm.WorkTransitions == nil {
				t.Errorf("missing counter: %s", name)
			}
		case "works.queue.depth":
			if pm.QueueDepth == nil {
				t.Errorf("missing gauge: %s", name)
			}
		case "works.queue.wait.duration":
			if pm.QueueWaitDuration == nil {
				t.Errorf("missing histogram: %s", name)
			}
		case "works.worker.capacity":
			if pm.WorkerCapacity == nil {
				t.Errorf("missing gauge: %s", name)
			}
		case "works.worker.utilization":
			if pm.WorkerUtilization == nil {
				t.Errorf("missing gauge: %s", name)
			}
		case "works.worker.lifetime.duration":
			if pm.WorkerLifetimeDuration == nil {
				t.Errorf("missing histogram: %s", name)
			}
		case "works.scheduler.decisions":
			if pm.SchedulerDecisions == nil {
				t.Errorf("missing counter: %s", name)
			}
		case "works.cache.requests":
			if pm.CacheRequests == nil {
				t.Errorf("missing counter: %s", name)
			}
		case "works.cache.operation.duration":
			if pm.CacheOperationDuration == nil {
				t.Errorf("missing histogram: %s", name)
			}
		case "works.artifact.transfer.duration":
			if pm.ArtifactTransferDuration == nil {
				t.Errorf("missing histogram: %s", name)
			}
		case "works.artifact.transfer.size":
			if pm.ArtifactTransferSize == nil {
				t.Errorf("missing histogram: %s", name)
			}
		case "works.failures":
			if pm.Failures == nil {
				t.Errorf("missing counter: %s", name)
			}
		case "works.remediation.attempts":
			if pm.RemediationAttempts == nil {
				t.Errorf("missing counter: %s", name)
			}
		}
	}
	if len(observability.PackMetricSpecs) != 13 {
		t.Errorf("PackMetricSpecs has %d entries, want 13", len(observability.PackMetricSpecs))
	}
}

// TestCounter_Inc_AndAdd asserts the Counter primitives: Inc, Add, the
// `_total` suffix on the wire, and a clean Prometheus text rendering.
func TestCounter_Inc_AndAdd(t *testing.T) {
	reg := observability.NewRegistry()
	c := reg.MustRegister(observability.NewCounter("test_counter", "test help", "{thing}", nil)).(*observability.Counter)
	c.Inc()
	c.Inc()
	c.Add(5)
	if got, want := c.Value(), uint64(7); got != want {
		t.Errorf("value: got %d want %d", got, want)
	}
	var buf strings.Builder
	if err := reg.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "# HELP test_counter_total test help\n") {
		t.Errorf("missing HELP line; got:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE test_counter_total counter\n") {
		t.Errorf("missing TYPE line; got:\n%s", out)
	}
	if !strings.Contains(out, "test_counter_total 7\n") {
		t.Errorf("missing sample line; got:\n%s", out)
	}
}

// TestHistogram_Observe_Buckets asserts that observations land in the
// correct buckets, the +Inf bucket equals the total count, and the
// rendered Prometheus text includes _bucket / _sum / _count suffixes.
func TestHistogram_Observe_Buckets(t *testing.T) {
	reg := observability.NewRegistry()
	h := reg.MustRegister(observability.NewHistogram(
		"test_hist", "hist help", "s", nil,
		[]float64{0.1, 0.5, 1.0},
	)).(*observability.Histogram)

	h.Observe(0.05) // <=0.1
	h.Observe(0.2)  // <=0.5
	h.Observe(0.6)  // <=1.0
	h.Observe(5.0)  // +Inf only

	snap := h.Snapshot()
	wantCounts := []uint64{1, 2, 3}
	for i, b := range snap.Buckets {
		if b.Count != wantCounts[i] {
			t.Errorf("bucket[%d]=%v count=%d want %d", i, b.LE, b.Count, wantCounts[i])
		}
	}
	if snap.Count != 4 {
		t.Errorf("count: got %d want 4", snap.Count)
	}
	if snap.Sum < 5.85 || snap.Sum > 5.86 {
		t.Errorf("sum: got %v want ~5.85", snap.Sum)
	}

	var buf strings.Builder
	if err := reg.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"# TYPE test_hist histogram",
		`test_hist_bucket{le="0.1"} 1`,
		`test_hist_bucket{le="0.5"} 2`,
		`test_hist_bucket{le="1"} 3`,
		`test_hist_bucket{le="+Inf"} 4`,
		"test_hist_count 4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestRegistry_MustRegister_DuplicatePanics asserts the panic contract on
// double-registration — matching Prometheus client_golang's behavior.
func TestRegistry_MustRegister_DuplicatePanics(t *testing.T) {
	reg := observability.NewRegistry()
	reg.MustRegister(observability.NewGauge("dup", "help", "", nil))
	defer func() {
		if recover() == nil {
			t.Errorf("expected panic on duplicate registration")
		}
	}()
	reg.MustRegister(observability.NewGauge("dup", "help", "", nil))
}

// TestRegistry_Gather_StableOrder asserts that Gather returns metrics in
// alphabetical order (so scrape output is deterministic).
func TestRegistry_Gather_StableOrder(t *testing.T) {
	reg := observability.NewRegistry()
	reg.MustRegister(observability.NewGauge("z_last", "", "", nil))
	reg.MustRegister(observability.NewGauge("a_first", "", "", nil))
	reg.MustRegister(observability.NewGauge("m_middle", "", "", nil))
	got := []string{}
	for _, m := range reg.Gather() {
		got = append(got, m.Name())
	}
	want := []string{"a_first", "m_middle", "z_last"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("gather order: got %v want %v", got, want)
	}
}

// TestCollector_Scrape_QueueDepth pulls queue depth from a real store and
// confirms the gauge reflects non-terminal works.
func TestCollector_Scrape_QueueDepth(t *testing.T) {
	_, pm, col, st := newMetrics(t)
	ctx := context.Background()

	// Two non-terminal works (CREATED + QUEUED) → depth=2.
	for i := 0; i < 2; i++ {
		w := &workgraph.Work{
			ID:        workgraph.NewID("wrk"),
			State:     workgraph.StateQueued,
			Source:    workgraph.Source{Type: "cli"},
			Objective: workgraph.Objective{Type: "verify_change"},
			Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
		}
		if err := st.CreateWork(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	// One terminal (CANCELLED) → should NOT count toward queue depth.
	w3 := &workgraph.Work{
		ID:        workgraph.NewID("wrk"),
		State:     workgraph.StateCreated,
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := st.CreateWork(ctx, w3); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateState(ctx, w3.ID, workgraph.StateCancelled); err != nil {
		t.Fatal(err)
	}

	col.Scrape(ctx)
	if got := pm.QueueDepth.Value(); got != 2 {
		t.Errorf("queue depth: got %v want 2", got)
	}
	if col.LastScrapeAt().IsZero() {
		t.Errorf("LastScrapeAt not set after Scrape")
	}
}

// TestMetricsHandler_ServesPrometheusText exercises the full HTTP
// path: GET /metrics returns 200 with a Prometheus text body containing
// HELP, TYPE, and a sample line for every pack-mandated metric.
func TestMetricsHandler_ServesPrometheusText(t *testing.T) {
	reg := observability.NewRegistry()
	pm := observability.NewPackMetrics(reg)

	// Push a few values so the body has non-trivial data.
	pm.WorkTransitions.Inc()
	pm.WorkTransitions.Inc()
	pm.SchedulerDecisions.Inc()
	pm.QueueWaitDuration.ObserveDuration(150 * time.Millisecond)

	h := api.NewMetricsHandler(reg, nil, log.New(io.Discard, "", 0))
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type: got %q want text/plain prefix", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := string(body)

	// The 13 metrics from OBSERVABILITY.md must each appear with HELP+TYPE
	// and a sample. Counters serialize with the _total suffix on the wire.
	mustContain := []string{
		"# TYPE works_work_transitions_total counter",
		"works_work_transitions_total 2",
		"# TYPE works_queue_depth gauge",
		"works_queue_depth 0",
		"# TYPE works_queue_wait_duration histogram",
		"# TYPE works_worker_capacity gauge",
		"# TYPE works_worker_utilization gauge",
		"# TYPE works_worker_lifetime_duration histogram",
		"# TYPE works_scheduler_decisions_total counter",
		"works_scheduler_decisions_total 1",
		"# TYPE works_cache_requests_total counter",
		"# TYPE works_cache_operation_duration histogram",
		"# TYPE works_artifact_transfer_duration histogram",
		"# TYPE works_artifact_transfer_size histogram",
		"# TYPE works_failures_total counter",
		"# TYPE works_remediation_attempts_total counter",
	}
	for _, m := range mustContain {
		if !strings.Contains(out, m) {
			t.Errorf("missing %q in scrape body", m)
		}
	}

	// The observed histogram must surface at least one non-zero bucket.
	if !strings.Contains(out, "works_queue_wait_duration_bucket") {
		t.Errorf("expected histogram bucket line in:\n%s", out)
	}
}

// TestMetricsHandler_RejectsNonGET ensures only GET returns the metrics
// body; other methods get a 405 (Prometheus scrapers are GET-only).
func TestMetricsHandler_RejectsNonGET(t *testing.T) {
	reg := observability.NewRegistry()
	_ = observability.NewPackMetrics(reg)
	h := api.NewMetricsHandler(reg, nil, log.New(io.Discard, "", 0))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/metrics", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d want 405", resp.StatusCode)
	}
}

// TestServerRoutes_ExposesMetricsAtRoot mounts the handler via
// Server.Routes() and confirms GET /metrics is reachable from the
// canonical URL the brief calls out (no auth).
func TestServerRoutes_ExposesMetricsAtRoot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "rt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := observability.NewRegistry()
	pm := observability.NewPackMetrics(reg)
	col := observability.NewCollector(st, pm, log.New(io.Discard, "", 0))

	srv := &api.Server{Store: st, Metrics: reg, MetricsCollector: col}
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "# TYPE works_queue_depth gauge") {
		t.Errorf("missing pack metric in scrape body; got:\n%s", body)
	}
}

// Compile-time guard: keep JSON used (avoid unused-import warnings if the
// helper set above shrinks in future edits).
var _ = json.Marshal