package api

import (
	"log"
	"net/http"

	"github.com/JonasAbde/works-execution/services/observability"
)

// MetricsHandler serves GET /metrics in Prometheus text format. It is
// registered outside auth (same convention as Kubernetes' kubelet and
// most OpenMetrics scrapers): the operator-network-only posture means
// the surface is considered internal and an ingress firewall is the
// authentication boundary.
//
// The handler is content-type agnostic on the request side — Prometheus
// sends `Accept: application/openmetrics-text; version=1.0.0,text/plain;
// version=0.0.4;q=0.7,*/*;q=0.1` and we answer text/plain; version=0.0.4
// which both Prometheus and OpenMetrics scrapers understand. The OTel
// upgrade in a later slice will add OpenMetrics negotiation.
type MetricsHandler struct {
	// Registry is the metrics registry to render. Required.
	Registry *observability.Registry
	// Collector is invoked before each scrape to refresh pull-class
	// metrics (queue depth, active leases, process runtime). Optional;
	// when nil, the handler just renders what is already in the registry.
	Collector *observability.Collector
	// Logger is used to record scrape failures. Optional.
	Logger *log.Logger
}

// NewMetricsHandler constructs a MetricsHandler.
func NewMetricsHandler(reg *observability.Registry, col *observability.Collector, lg *log.Logger) *MetricsHandler {
	return &MetricsHandler{Registry: reg, Collector: col, Logger: lg}
}

// ServeHTTP implements http.Handler.
func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Collector != nil {
		h.Collector.Scrape(r.Context())
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := h.Registry.WriteText(w); err != nil {
		if h.Logger != nil {
			h.Logger.Printf("metrics: write text: %v", err)
		}
	}
}