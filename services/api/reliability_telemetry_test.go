package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/audit"
	"github.com/JonasAbde/works-execution/services/observability"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func newReliabilityServer(t *testing.T) (*httptest.Server, *observability.ReliabilityMetrics) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "reliability.db"))
	if err != nil {
		t.Fatal(err)
	}

	reg := observability.NewRegistry()
	pack := observability.NewPackMetrics(reg)
	reliability := observability.NewReliabilityMetrics(reg)
	collector := observability.NewCollector(st, pack, log.New(io.Discard, "", 0))
	srv := &api.Server{
		Store:            st,
		Metrics:          reg,
		MetricsCollector: collector,
		Reliability: &api.ReliabilityTelemetry{
			Metrics: reliability,
			Audit:   audit.NewSQLiteEmitter(st.DB(), log.New(io.Discard, "", 0)),
		},
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})
	return ts, reliability
}

func reliabilityBody(id, key, run string) []byte {
	w := workgraph.Work{
		ID:             id,
		IdempotencyKey: key,
		Source: workgraph.Source{
			Type:       "test",
			Repository: "Aftergraph/works-execution",
		},
		Objective: workgraph.Objective{Type: "verify_change"},
		Requirements: workgraph.Requirements{OS: "linux"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"run": {ID: "run", Run: run},
		}},
	}
	raw, _ := json.Marshal(w)
	return raw
}

func postReliabilityWork(t *testing.T, base string, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(base+"/v1/works", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

func TestReliabilityTelemetry_DurableReportAndPrometheusCounters(t *testing.T) {
	ts, metrics := newReliabilityServer(t)
	key := "telemetry-proof"

	first, raw := postReliabilityWork(t, ts.URL,
		reliabilityBody(workgraph.NewID("wrk"), key, "echo same"))
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first create status=%d body=%s", first.StatusCode, string(raw))
	}

	replay, raw := postReliabilityWork(t, ts.URL,
		reliabilityBody(workgraph.NewID("wrk"), key, "echo same"))
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.StatusCode, string(raw))
	}
	if replay.Header.Get("X-Works-Idempotent-Replay") != "true" {
		t.Fatal("missing idempotent replay marker")
	}

	conflict, raw := postReliabilityWork(t, ts.URL,
		reliabilityBody(workgraph.NewID("wrk"), key, "echo changed"))
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.StatusCode, string(raw))
	}

	if got := metrics.ReplayRequests.Value(); got != 2 {
		t.Fatalf("replay requests=%d want 2", got)
	}
	if got := metrics.ReplayRecovered.Value(); got != 1 {
		t.Fatalf("recovered=%d want 1", got)
	}
	if got := metrics.ReplayConflicts.Value(); got != 1 {
		t.Fatalf("conflicts=%d want 1", got)
	}
	if got := metrics.ReplayFailures.Value(); got != 0 {
		t.Fatalf("failures=%d want 0", got)
	}

	resp, err := http.Get(ts.URL + "/v1/reliability?hours=1")
	if err != nil {
		t.Fatal(err)
	}
	reportRaw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reliability report status=%d body=%s", resp.StatusCode, string(reportRaw))
	}
	var report struct {
		Samples           int     `json:"samples"`
		ReplayRecovered   int     `json:"replay_recovered"`
		ReplayConflicts   int     `json:"replay_conflicts"`
		ReplayFailures    int     `json:"replay_failures"`
		ReplaySuccessRate float64 `json:"replay_success_rate"`
	}
	if err := json.Unmarshal(reportRaw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Samples != 2 || report.ReplayRecovered != 1 ||
		report.ReplayConflicts != 1 || report.ReplayFailures != 0 {
		t.Fatalf("unexpected durable report: %+v", report)
	}
	if report.ReplaySuccessRate != 0.5 {
		t.Fatalf("success rate=%v want 0.5", report.ReplaySuccessRate)
	}

	metricsResp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metricsRaw, _ := io.ReadAll(metricsResp.Body)
	_ = metricsResp.Body.Close()
	if metricsResp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", metricsResp.StatusCode, string(metricsRaw))
	}
	text := string(metricsRaw)
	for _, want := range []string{
		"works_reliability_replay_requests_total 2",
		"works_reliability_replay_recovered_total 1",
		"works_reliability_replay_conflicts_total 1",
		"works_reliability_replay_failures_total 0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q\n%s", want, text)
		}
	}
}
