package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/audit"
	"github.com/JonasAbde/works-execution/services/observability"
)

const RecoveryCauseHeader = "X-Works-Recovery-Cause"

var allowedRecoveryCauses = map[string]struct{}{
	"ambiguous_transport":     {},
	"ambiguous_response_read": {},
	"transient_status":        {},
	"auth_renewal":            {},
	"controller_reconnect":    {},
}

func normalizeRecoveryCause(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if _, ok := allowedRecoveryCauses[v]; ok {
		return v
	}
	return ""
}

func ambiguousAckCause(v string) bool {
	return v == "ambiguous_transport" || v == "ambiguous_response_read"
}

// ReliabilityTelemetry joins ephemeral Prometheus counters with durable
// CloudEvents audit records. Prometheus is the fast operational surface;
// the audit stream is the longitudinal source used across API restarts.
type ReliabilityTelemetry struct {
	Metrics *observability.ReliabilityMetrics
	Audit   audit.Emitter
}

func (s *Server) recordReliabilityReplay(
	ctx context.Context,
	w *workgraph.Work,
	outcome, reason, recoveryCause string,
	queueRepaired bool,
	started time.Time,
) {
	if s.Reliability == nil {
		return
	}

	recoveryCause = normalizeRecoveryCause(recoveryCause)
	duration := time.Since(started)
	if m := s.Reliability.Metrics; m != nil {
		m.ReplayRequests.Inc()
		switch outcome {
		case "recovered":
			m.ReplayRecovered.Inc()
		case "conflict":
			m.ReplayConflicts.Inc()
		default:
			m.ReplayFailures.Inc()
		}
		if queueRepaired {
			m.QueueRepairs.Inc()
		}
		if ambiguousAckCause(recoveryCause) {
			m.AmbiguousAckRequests.Inc()
			if outcome == "recovered" {
				m.AmbiguousAckRecovered.Inc()
			}
		}
		if recoveryCause == "controller_reconnect" {
			m.ControllerReconnectRequests.Inc()
			if outcome == "recovered" {
				m.ControllerReconnectRecovered.Inc()
			}
		}
		m.ReplayDuration.ObserveDuration(duration)
	}

	if s.Reliability.Audit == nil {
		return
	}

	workID := ""
	state := ""
	durableMetadata := false
	if w != nil {
		workID = w.ID
		state = string(w.State)
		durableMetadata = w.CreationIntentHash != "" &&
			w.AdmissionDefaultsJSON != "" &&
			w.QueueRequested != nil
	}

	ev := audit.NewEvent(audit.TypeReliabilityReplayOutcome, workID)
	ev.WorkID = workID
	ev.Data = audit.ReliabilityReplayData{
		WorkID:          workID,
		Outcome:         outcome,
		Reason:          reason,
		RecoveryCause:   recoveryCause,
		CauseSource:     "authenticated_client",
		State:           state,
		QueueRepaired:   queueRepaired,
		DurableMetadata: durableMetadata,
		DurationMS:      float64(duration.Microseconds()) / 1000.0,
	}
	if err := s.Reliability.Audit.Emit(ctx, ev); err != nil {
		s.logf("reliability audit emit failed: %v", err)
	}
}

type reliabilityReport struct {
	Since                           time.Time `json:"since"`
	Until                           time.Time `json:"until"`
	Samples                         int       `json:"samples"`
	Truncated                       bool      `json:"truncated"`
	ReplayRecovered                 int       `json:"replay_recovered"`
	ReplayConflicts                 int       `json:"replay_conflicts"`
	ReplayFailures                  int       `json:"replay_failures"`
	QueueRepairs                    int       `json:"queue_repairs"`
	ReplaySuccessRate               float64   `json:"replay_success_rate"`
	ReplayFailureRate               float64   `json:"replay_failure_rate"`
	AmbiguousAckSamples             int       `json:"ambiguous_ack_samples"`
	AmbiguousAckRecovered           int       `json:"ambiguous_ack_recovered"`
	AmbiguousAckSurvivalRate        float64   `json:"ambiguous_ack_survival_rate"`
	ControllerReconnectSamples      int       `json:"controller_reconnect_samples"`
	ControllerReconnectRecovered    int       `json:"controller_reconnect_recovered"`
	ControllerReconnectSurvivalRate float64   `json:"controller_reconnect_survival_rate"`
	P50DurationMS                   float64   `json:"p50_duration_ms"`
	P95DurationMS                   float64   `json:"p95_duration_ms"`
}

func (s *Server) reliabilityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}

	hours := 24.0
	if raw := r.URL.Query().Get("hours"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || v > 24*90 {
			writeError(w, http.StatusBadRequest, "invalid_hours", "hours must be > 0 and <= 2160")
			return
		}
		hours = v
	}

	until := time.Now().UTC()
	since := until.Add(-time.Duration(hours * float64(time.Hour)))
	events, err := s.Store.ListAuditEvents(r.Context(), audit.ListFilter{
		Since: since,
		Until: until,
		Type:  audit.TypeReliabilityReplayOutcome,
		Limit: 1000,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reliability_query_failed", err.Error())
		return
	}

	report := reliabilityReport{Since: since, Until: until, Truncated: len(events) == 1000}
	durations := make([]float64, 0, len(events))
	for _, ev := range events {
		var data audit.ReliabilityReplayData
		if len(ev.Data) == 0 || json.Unmarshal(ev.Data, &data) != nil {
			continue
		}
		report.Samples++
		switch data.Outcome {
		case "recovered":
			report.ReplayRecovered++
		case "conflict":
			report.ReplayConflicts++
		default:
			report.ReplayFailures++
		}
		if data.QueueRepaired {
			report.QueueRepairs++
		}
		if ambiguousAckCause(data.RecoveryCause) {
			report.AmbiguousAckSamples++
			if data.Outcome == "recovered" {
				report.AmbiguousAckRecovered++
			}
		}
		if data.RecoveryCause == "controller_reconnect" {
			report.ControllerReconnectSamples++
			if data.Outcome == "recovered" {
				report.ControllerReconnectRecovered++
			}
		}
		if data.DurationMS >= 0 {
			durations = append(durations, data.DurationMS)
		}
	}

	if report.Samples > 0 {
		report.ReplaySuccessRate = float64(report.ReplayRecovered) / float64(report.Samples)
		report.ReplayFailureRate = float64(report.ReplayConflicts+report.ReplayFailures) / float64(report.Samples)
	}
	if report.AmbiguousAckSamples > 0 {
		report.AmbiguousAckSurvivalRate = float64(report.AmbiguousAckRecovered) / float64(report.AmbiguousAckSamples)
	}
	if report.ControllerReconnectSamples > 0 {
		report.ControllerReconnectSurvivalRate = float64(report.ControllerReconnectRecovered) / float64(report.ControllerReconnectSamples)
	}
	if len(durations) > 0 {
		sort.Float64s(durations)
		report.P50DurationMS = percentileNearestRank(durations, 0.50)
		report.P95DurationMS = percentileNearestRank(durations, 0.95)
	}

	writeJSON(w, http.StatusOK, report)
}

func percentileNearestRank(sortedValues []float64, p float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	if p <= 0 {
		return sortedValues[0]
	}
	if p >= 1 {
		return sortedValues[len(sortedValues)-1]
	}
	idx := int(math.Ceil(p*float64(len(sortedValues)))) - 1
	if idx < 0 { idx = 0 }
	if idx >= len(sortedValues) { idx = len(sortedValues)-1 }
	return sortedValues[idx]
}
