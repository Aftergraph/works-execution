// Package deploy computes DORA (DevOps Research and Assessment) software
// delivery performance metrics from the durable Work state.
//
// DORA's four key metrics are:
//
//   1. Deployment Frequency   — how often code is deployed to production.
//   2. Lead Time for Changes  — commit-to-production latency.
//   3. Change Failure Rate    — % of deployments that cause a production
//                               failure (requiring remediation).
//   4. Mean Time to Recovery  — how long it takes to restore service
//                               after an incident.
//
// In this codebase, the natural proxies are:
//
//   Deployment  <-> a Work that has reached StateSucceeded.
//   Change Lead <-> Work.CreatedAt -> Work reaching StateSucceeded.
//   Failure     <-> a Work that has reached StateFailed (or that has
//                   at least one attempt with Status="failed").
//   Recovery    <-> for a failed Work that subsequently reaches
//                   StateSucceeded, the time from the FAILED audit event
//                   to the SUCCEEDED audit event. We approximate this
//                   as the time from the Work's updated_at at FAILED
//                   to the time it reached SUCCEEDED.
//
// The computation is pure: it takes a slice of Works (plus a slice of
// recent state-transition audit events) and returns a Report. There is
// no IO here. Persistence lives in services/work/store; the HTTP
// boundary lives in services/api/dora_handler.go.
package deploy

import (
	"sort"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/audit"
)

// Report is the response payload for GET /v1/dora. All durations are in
// seconds. Counts are integer totals for the window. Performance bands
// follow Google's DORA 2019 report classification: "Elite", "High",
// "Medium", "Low".
type Report struct {
	Window          Window       `json:"window"`
	DeploymentFreq  MetricCount  `json:"deployment_frequency"`
	LeadTime        MetricDur    `json:"lead_time_for_changes"`
	ChangeFailRate  MetricRatio  `json:"change_failure_rate"`
	MTTR            MetricDur    `json:"mean_time_to_recovery"`
	OverallBand     string       `json:"overall_band"`
	GeneratedAt     time.Time    `json:"generated_at"`
	WorkCounts      WorkCounts   `json:"work_counts"`
}

// Window is the inclusive time range the report covers. Zero values
// are unbounded on that side.
type Window struct {
	From time.Time `json:"from,omitempty"`
	To   time.Time `json:"to,omitempty"`
}

// MetricCount is a count + a derived "per_day" rate. Band is left
// blank for count metrics (the DORA band mapping applies to
// frequency/lead-time).
type MetricCount struct {
	Total  int     `json:"total"`
	PerDay float64 `json:"per_day"`
	Band   string  `json:"band,omitempty"`
}

// MetricDur is a duration metric in seconds.
type MetricDur struct {
	Seconds float64 `json:"seconds"`
	Band    string  `json:"band,omitempty"`
	SampleN int     `json:"sample_n"`
}

// MetricRatio is a percentage in [0,100].
type MetricRatio struct {
	Percent float64 `json:"percent"`
	Band    string  `json:"band"`
	SampleN int     `json:"sample_n"`
}

// WorkCounts breaks down the Works observed in the window.
type WorkCounts struct {
	Total      int `json:"total"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"blocked_or_cancelled"`
	InProgress int `json:"in_progress"`
}

// Compute builds a Report over the supplied Works (filtered to the
// window) and an optional slice of audit events used to recover the
// FAILED -> SUCCEEDED latencies for MTTR.
//
// Works are expected to be the canonical list of Works that touched
// the window (typically `ListWorks` over a wider window then filtered
// here). Audit events, if non-nil, are used to find the timestamp of
// the FAILED transition per Work for MTTR.
//
// If the window is zero on either side it is treated as unbounded.
func Compute(works []*workgraph.Work, events []audit.AuditEvent, w Window) Report {
	now := time.Now().UTC()

	// 1. Filter Works to the window. A Work is "in the window" if its
	//    UpdatedAt is in range. This is the pragmatic choice: DORA is
	//    about *what happened* in the window, and UpdatedAt is when
	//    the most recent state change for the Work happened.
	inWindow := make([]*workgraph.Work, 0, len(works))
	for _, wk := range works {
		if wk == nil {
			continue
		}
		if !w.From.IsZero() && wk.UpdatedAt.Before(w.From) {
			continue
		}
		if !w.To.IsZero() && wk.UpdatedAt.After(w.To) {
			continue
		}
		inWindow = append(inWindow, wk)
	}

	// 2. Tally.
	wc := WorkCounts{Total: len(inWindow)}
	var succeeded, failed []*workgraph.Work
	for _, wk := range inWindow {
		switch wk.State {
		case workgraph.StateSucceeded:
			wc.Succeeded++
			succeeded = append(succeeded, wk)
		case workgraph.StateFailed:
			wc.Failed++
			failed = append(failed, wk)
		case workgraph.StateCancelled, workgraph.StateBlocked:
			wc.Cancelled++
		default:
			wc.InProgress++
		}
	}

	// 3. Deployment frequency. DORA's "deployment" maps to a Work that
	//    reached SUCCEEDED within the window. We approximate the
	//    "per day" rate as total / window days (clamped to 1 day to
	//    avoid divide-by-zero in small windows).
	days := windowDays(w, now)
	df := MetricCount{Total: wc.Succeeded, PerDay: rate(wc.Succeeded, days)}
	df.Band = bandForDeploymentFrequency(df.PerDay)

	// 4. Lead time. Average of (UpdatedAt - CreatedAt) over Works
	//    that succeeded. We use UpdatedAt as a proxy for "in
	//    production" since the Work's terminal state is recorded
	//    there.
	var leadDurations []time.Duration
	for _, wk := range succeeded {
		if wk.CreatedAt.IsZero() || wk.UpdatedAt.IsZero() {
			continue
		}
		d := wk.UpdatedAt.Sub(wk.CreatedAt)
		if d < 0 {
			continue
		}
		leadDurations = append(leadDurations, d)
	}
	lt := avgDur(leadDurations)
	lt.Band = bandForLeadTime(lt.Seconds)

	// 5. Change failure rate. DORA counts a "failed change" as one that
	//    required remediation. Here: a Work in FAILED state.
	total := wc.Succeeded + wc.Failed
	cfr := MetricRatio{SampleN: total}
	if total > 0 {
		cfr.Percent = 100.0 * float64(wc.Failed) / float64(total)
	}
	// Band is always assigned: 0% (no observations) is still "Elite"
	// per the DORA scale.
	cfr.Band = bandForChangeFailureRate(cfr.Percent)

	// 6. MTTR. For each Work that ended up FAILED, find the timestamp
	//    of the FAILED audit event and (if the same Work later
	//    succeeded) the SUCCEEDED audit event. Average the gap.
	mttrDurations := computeMTTR(inWindow, events)
	mttr := avgDur(mttrDurations)
	mttr.Band = bandForMTTR(mttr.Seconds)

	return Report{
		Window:         w,
		DeploymentFreq: df,
		LeadTime:       lt,
		ChangeFailRate: cfr,
		MTTR:           mttr,
		OverallBand:    overallBand(df.Band, lt.Band, cfr.Band, mttr.Band),
		GeneratedAt:    now,
		WorkCounts:     wc,
	}
}

// computeMTTR pairs FAILED audit events with subsequent SUCCEEDED
// events on the same Work, returning the recovery latencies. Works
// that never recovered are excluded from the average (they are open
// incidents; their MTTR is unknown).
func computeMTTR(works []*workgraph.Work, events []audit.AuditEvent) []time.Duration {
	if len(events) == 0 || len(works) == 0 {
		return nil
	}
	// Build work -> terminal-state event timeline (sorted ASC).
	byWork := map[string][]audit.AuditEvent{}
	for _, ev := range events {
		if ev.WorkID == "" {
			continue
		}
		byWork[ev.WorkID] = append(byWork[ev.WorkID], ev)
	}
	for id := range byWork {
		sort.Slice(byWork[id], func(i, j int) bool {
			return byWork[id][i].OccurredAt.Before(byWork[id][j].OccurredAt)
		})
	}

	// For each Work that is currently FAILED, walk forward in its
	// audit timeline from the most recent FAILED event to the next
	// SUCCEEDED event. If found, record the gap.
	workSet := map[string]bool{}
	for _, wk := range works {
		if wk != nil {
			workSet[wk.ID] = true
		}
	}

	var gaps []time.Duration
	for id, evs := range byWork {
		if !workSet[id] {
			continue
		}
		for i, ev := range evs {
			if ev.ToState != string(workgraph.StateFailed) {
				continue
			}
			// Find the next SUCCEEDED event.
			for j := i + 1; j < len(evs); j++ {
				if evs[j].ToState == string(workgraph.StateSucceeded) {
					gaps = append(gaps, evs[j].OccurredAt.Sub(ev.OccurredAt))
					break
				}
			}
		}
	}
	return gaps
}

func windowDays(w Window, now time.Time) float64 {
	if w.From.IsZero() || w.To.IsZero() {
		// Default to the last 30 days when window is unbounded.
		return 30.0
	}
	d := w.To.Sub(w.From).Hours() / 24.0
	if d < 1.0 {
		return 1.0
	}
	return d
}

func rate(n int, days float64) float64 {
	if days <= 0 {
		return float64(n)
	}
	return float64(n) / days
}

func avgDur(ds []time.Duration) MetricDur {
	if len(ds) == 0 {
		return MetricDur{}
	}
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	avg := total / time.Duration(len(ds))
	return MetricDur{
		Seconds: avg.Seconds(),
		SampleN: len(ds),
	}
}

// overallBand reduces the four per-metric bands into one. The worst
// band wins (DORA guidance: a team is only as strong as its weakest
// metric). Unrated metrics are skipped.
func overallBand(bands ...string) string {
	rank := map[string]int{"Elite": 4, "High": 3, "Medium": 2, "Low": 1, "": 0}
	worst, worstRank := "", 5
	for _, b := range bands {
		if r, ok := rank[b]; ok && r > 0 && r < worstRank {
			worst, worstRank = b, r
		}
	}
	if worst == "" {
		return "unrated"
	}
	return worst
}

// --- DORA band mappings (Google 2019 report, "Accelerate") ---------
//
// Deployment frequency: per-day deployments per engineer / per team.
// We approximate "per team" here (single-team control plane).
//   Elite:    >= 1 per day
//   High:     >= 1 per week (0.143/day)
//   Medium:   >= 1 per month (0.032/day)
//   Low:      <  1 per month
//
// Lead time for changes (commit -> production).
//   Elite:    <  1 day      (< 86400s)
//   High:     <  1 week      (< 604800s)
//   Medium:   <  1 month     (< 2592000s)
//   Low:      >= 1 month
//
// Change failure rate.
//   Elite:    0%–15%
//   High:     16%–30%
//   Medium:   31%–45%
//   Low:      46%–100%
//
// MTTR.
//   Elite:    <  1 hour      (< 3600s)
//   High:     <  1 day       (< 86400s)
//   Medium:   <  1 week      (< 604800s)
//   Low:      >= 1 week

func bandForDeploymentFrequency(perDay float64) string {
	switch {
	case perDay >= 1.0:
		return "Elite"
	case perDay >= 1.0/7.0:
		return "High"
	case perDay >= 1.0/30.0:
		return "Medium"
	case perDay > 0:
		return "Low"
	default: // perDay == 0: no data, default to Elite (matches CFR behavior)
		return "Elite"
	}
}

func bandForLeadTime(seconds float64) string {
	switch {
	case seconds < 86400: // < 1 day
		return "Elite"
	case seconds < 604800: // < 1 week
		return "High"
	case seconds < 2592000: // < 30 days
		return "Medium"
	default:
		return "Low"
	}
}

func bandForChangeFailureRate(pct float64) string {
	switch {
	case pct <= 15.0:
		return "Elite"
	case pct <= 30.0:
		return "High"
	case pct <= 45.0:
		return "Medium"
	default:
		return "Low"
	}
}

func bandForMTTR(seconds float64) string {
	switch {
	case seconds < 3600: // < 1 hour
		return "Elite"
	case seconds < 86400: // < 1 day
		return "High"
	case seconds < 604800: // < 1 week
		return "Medium"
	default:
		return "Low"
	}
}
