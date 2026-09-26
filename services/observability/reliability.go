package observability

// ReliabilityMetrics is the additive reliability telemetry surface for
// idempotent submission recovery. These counters are intentionally unlabeled
// so the current lightweight metrics registry preserves exact counts.
type ReliabilityMetrics struct {
	ReplayRequests  *Counter
	ReplayRecovered *Counter
	ReplayConflicts *Counter
	ReplayFailures  *Counter
	QueueRepairs    *Counter
	ReplayDuration  *Histogram
}

// NewReliabilityMetrics registers reliability metrics in reg.
// PromQL can derive:
//   replay_success_rate = recovered / requests
//   replay_conflict_rate = conflicts / requests
func NewReliabilityMetrics(reg *Registry) *ReliabilityMetrics {
	return &ReliabilityMetrics{
		ReplayRequests: reg.MustRegister(NewCounter(
			"works.reliability.replay.requests",
			"Idempotent replay requests that found an existing canonical Work.",
			"{request}", nil,
		)).(*Counter),
		ReplayRecovered: reg.MustRegister(NewCounter(
			"works.reliability.replay.recovered",
			"Idempotent replay requests successfully reconciled to canonical Work.",
			"{recovery}", nil,
		)).(*Counter),
		ReplayConflicts: reg.MustRegister(NewCounter(
			"works.reliability.replay.conflicts",
			"Idempotent replay requests rejected because immutable intent or queue decision differed.",
			"{conflict}", nil,
		)).(*Counter),
		ReplayFailures: reg.MustRegister(NewCounter(
			"works.reliability.replay.failures",
			"Idempotent replay requests that failed for internal lookup/reconciliation reasons.",
			"{failure}", nil,
		)).(*Counter),
		QueueRepairs: reg.MustRegister(NewCounter(
			"works.reliability.queue.repairs",
			"CREATED to QUEUED crash seams repaired from durable original queue intent.",
			"{repair}", nil,
		)).(*Counter),
		ReplayDuration: reg.MustRegister(NewHistogram(
			"works.reliability.replay.duration",
			"Control-plane time spent reconciling an idempotent replay.",
			"s", nil, nil,
		)).(*Histogram),
	}
}
