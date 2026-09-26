package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
)

func postWithRecoveryCause(t *testing.T, base string, body []byte, cause string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v1/works", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cause != "" {
		req.Header.Set(api.RecoveryCauseHeader, cause)
	}
	resp, err := http.DefaultClient.Do(req)
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

func TestReliabilityTelemetry_AttributedSurvivalRates(t *testing.T) {
	ts, metrics := newReliabilityServer(t)

	keyReconnect := "telemetry-controller-reconnect"
	first, raw := postWithRecoveryCause(t, ts.URL,
		reliabilityBody(workgraph.NewID("wrk"), keyReconnect, "echo reconnect"), "")
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first reconnect work status=%d body=%s", first.StatusCode, string(raw))
	}
	replay, raw := postWithRecoveryCause(t, ts.URL,
		reliabilityBody(workgraph.NewID("wrk"), keyReconnect, "echo reconnect"), "controller_reconnect")
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("controller reconnect replay status=%d body=%s", replay.StatusCode, string(raw))
	}

	keyAck := "telemetry-ambiguous-ack"
	first, raw = postWithRecoveryCause(t, ts.URL,
		reliabilityBody(workgraph.NewID("wrk"), keyAck, "echo ack"), "")
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first ack work status=%d body=%s", first.StatusCode, string(raw))
	}
	replay, raw = postWithRecoveryCause(t, ts.URL,
		reliabilityBody(workgraph.NewID("wrk"), keyAck, "echo ack"), "ambiguous_transport")
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("ambiguous ack replay status=%d body=%s", replay.StatusCode, string(raw))
	}

	if got := metrics.ControllerReconnectRequests.Value(); got != 1 {
		t.Fatalf("controller reconnect requests=%d want 1", got)
	}
	if got := metrics.ControllerReconnectRecovered.Value(); got != 1 {
		t.Fatalf("controller reconnect recovered=%d want 1", got)
	}
	if got := metrics.AmbiguousAckRequests.Value(); got != 1 {
		t.Fatalf("ambiguous ack requests=%d want 1", got)
	}
	if got := metrics.AmbiguousAckRecovered.Value(); got != 1 {
		t.Fatalf("ambiguous ack recovered=%d want 1", got)
	}

	resp, err := http.Get(ts.URL + "/v1/reliability?hours=1")
	if err != nil {
		t.Fatal(err)
	}
	reportRaw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status=%d body=%s", resp.StatusCode, string(reportRaw))
	}
	var report struct {
		AmbiguousAckSamples               int     `json:"ambiguous_ack_samples"`
		AmbiguousAckRecovered             int     `json:"ambiguous_ack_recovered"`
		AmbiguousAckSurvivalRate          float64 `json:"ambiguous_ack_survival_rate"`
		ControllerReconnectSamples        int     `json:"controller_reconnect_samples"`
		ControllerReconnectRecovered      int     `json:"controller_reconnect_recovered"`
		ControllerReconnectSurvivalRate   float64 `json:"controller_reconnect_survival_rate"`
	}
	if err := json.Unmarshal(reportRaw, &report); err != nil {
		t.Fatal(err)
	}
	if report.AmbiguousAckSamples != 1 || report.AmbiguousAckRecovered != 1 || report.AmbiguousAckSurvivalRate != 1 {
		t.Fatalf("unexpected ambiguous ack report: %+v", report)
	}
	if report.ControllerReconnectSamples != 1 || report.ControllerReconnectRecovered != 1 || report.ControllerReconnectSurvivalRate != 1 {
		t.Fatalf("unexpected controller reconnect report: %+v", report)
	}
}
