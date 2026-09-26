package observability

// ReliabilityMetrics is the additive reliability telemetry surface for
// idempotent submission recovery. Counters stay unlabeled to avoid
// cardinality growth in the lightweight registry; the durable audit stream
// carries the detailed low-cardinality cause.
type ReliabilityMetrics struct {
	ReplayRequests               *Counter
	ReplayRecovered              *Counter
	ReplayConflicts              *Counter
	ReplayFailures               *Counter
	QueueRepairs                 *Counter
	ReplayDuration               *Histogram
	AmbiguousAckRequests         *Counter
	AmbiguousAckRecovered        *Counter
	ControllerReconnectRequests  *Counter
	ControllerReconnectRecovered *Counter
}

// NewReliabilityMetrics registers reliability metrics in reg.
//
// Derived SLOs:
//
//	replay_success_rate = recovered / requests
//	ambiguous_ack_survival_rate = ambiguous_ack_recovered / ambiguous_ack_requests
//	controller_reconnect_survival_rate = controller_reconnect_recovered / controller_reconnect_requests
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
		AmbiguousAckRequests: reg.MustRegister(NewCounter(
			"works.reliability.ambiguous_ack.requests",
			"Replay outcomes attributed by the client to ambiguous transport/response acknowledgement.",
			"{request}", nil,
		)).(*Counter),
		AmbiguousAckRecovered: reg.MustRegister(NewCounter(
			"works.reliability.ambiguous_ack.recovered",
			"Ambiguous acknowledgement replay outcomes successfully reconciled.",
			"{recovery}", nil,
		)).(*Counter),
		ControllerReconnectRequests: reg.MustRegister(NewCounter(
			"works.reliability.controller_reconnect.requests",
			"Replay outcomes explicitly attributed by an authenticated controller to reconnect/resume.",
			"{request}", nil,
		)).(*Counter),
		ControllerReconnectRecovered: reg.MustRegister(NewCounter(
			"works.reliability.controller_reconnect.recovered",
			"Controller reconnect replay outcomes successfully reconciled.",
			"{recovery}", nil,
		)).(*Counter),
	}
}
