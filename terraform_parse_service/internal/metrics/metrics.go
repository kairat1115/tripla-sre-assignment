// Package metrics registers Prometheus metrics used by the HTTP and rendering
// layers.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}

// Metrics groups the collectors shared across handlers and services.
type Metrics struct {
	reg                prometheus.Gatherer
	GenerationTotal    *prometheus.CounterVec
	GenerationDuration *prometheus.HistogramVec
	HTTPRequestsTotal  *prometheus.CounterVec
	HTTPDuration       *prometheus.HistogramVec
	HTTPInFlight       *prometheus.GaugeVec
}

// New registers all service collectors in reg.
func New(reg *prometheus.Registry) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		reg: reg,
		GenerationTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "terraform_generation_total",
			Help: "Total number of terraform generation tasks.",
		}, []string{"provider", "resource", "status"}),

		GenerationDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "terraform_generation_duration_seconds",
			Help:    "Duration of terraform generation tasks.",
			Buckets: durationBuckets,
		}, []string{"provider", "resource"}),

		HTTPRequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		}, []string{"method", "path", "status_code"}),

		HTTPDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration.",
			Buckets: durationBuckets,
		}, []string{"method", "path"}),

		HTTPInFlight: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Number of currently in-flight HTTP requests.",
		}, []string{"method", "path"}),
	}
}

// Serve starts a Prometheus scrape endpoint on addr. When addr is empty it
// listens on :9091.
func (m *Metrics) Serve(addr string, log *zap.Logger) {
	server := m.Server(addr)
	go func() {
		log.Info("metrics server starting", zap.String("addr", server.Addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server exited", zap.String("error", err.Error()))
		}
	}()
}

// Server returns an HTTP server exposing the Prometheus /metrics endpoint.
func (m *Metrics) Server(addr string) *http.Server {
	if addr == "" {
		addr = ":9091"
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}))
	return &http.Server{Addr: addr, Handler: mux}
}
