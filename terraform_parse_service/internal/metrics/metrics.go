// Package metrics registers Prometheus metrics used by the HTTP and rendering
// layers.
package metrics

import (
	"net/http"
	"time"

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
	RenderedBytes      *prometheus.HistogramVec
	HTTPRequestsTotal  *prometheus.CounterVec
	HTTPDuration       *prometheus.HistogramVec
	HTTPInFlight       *prometheus.GaugeVec
	ResourceOperations *prometheus.CounterVec
	ResourceDuration   *prometheus.HistogramVec
	StorageOperations  *prometheus.CounterVec
	StorageDuration    *prometheus.HistogramVec
	TemplateReloads    *prometheus.CounterVec
	TemplateDuration   *prometheus.HistogramVec
	TemplatesLoaded    *prometheus.GaugeVec
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

		RenderedBytes: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "terraform_rendered_bytes",
			Help:    "Size of rendered Terraform output.",
			Buckets: []float64{128, 512, 1024, 4096, 16384, 65536, 262144, 1048576},
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

		ResourceOperations: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "terraform_resource_operations_total",
			Help: "Total Terraform resource API operations.",
		}, []string{"provider", "service", "resource", "operation", "status"}),

		ResourceDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "terraform_resource_operation_duration_seconds",
			Help:    "Duration of Terraform resource API operations.",
			Buckets: durationBuckets,
		}, []string{"provider", "service", "resource", "operation"}),

		StorageOperations: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "terraform_storage_operations_total",
			Help: "Total Terraform storage operations.",
		}, []string{"provider", "operation", "status"}),

		StorageDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "terraform_storage_operation_duration_seconds",
			Help:    "Duration of Terraform storage operations.",
			Buckets: durationBuckets,
		}, []string{"provider", "operation"}),

		TemplateReloads: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "terraform_template_reloads_total",
			Help: "Total provider template reload attempts.",
		}, []string{"provider", "status"}),

		TemplateDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "terraform_template_reload_duration_seconds",
			Help:    "Duration of provider template reload attempts.",
			Buckets: durationBuckets,
		}, []string{"provider"}),

		TemplatesLoaded: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "terraform_templates_loaded",
			Help: "Number of parsed templates currently loaded per provider.",
		}, []string{"provider"}),
	}
}

// ObserveResourceOperation records one resource API operation.
func (m *Metrics) ObserveResourceOperation(provider, service, resource, operation, status string, duration time.Duration) {
	if m == nil {
		return
	}
	m.ResourceOperations.WithLabelValues(provider, service, resource, operation, status).Inc()
	m.ResourceDuration.WithLabelValues(provider, service, resource, operation).Observe(duration.Seconds())
}

// ObserveStorageOperation records one storage backend operation.
func (m *Metrics) ObserveStorageOperation(provider, operation, status string, duration time.Duration) {
	if m == nil {
		return
	}
	m.StorageOperations.WithLabelValues(provider, operation, status).Inc()
	m.StorageDuration.WithLabelValues(provider, operation).Observe(duration.Seconds())
}

// ObserveTemplateReload records one provider template reload attempt.
func (m *Metrics) ObserveTemplateReload(provider, status string, duration time.Duration) {
	if m == nil {
		return
	}
	m.TemplateReloads.WithLabelValues(provider, status).Inc()
	m.TemplateDuration.WithLabelValues(provider).Observe(duration.Seconds())
}

// SetTemplatesLoaded stores the current parsed template count for provider.
func (m *Metrics) SetTemplatesLoaded(provider string, count int) {
	if m == nil {
		return
	}
	m.TemplatesLoaded.WithLabelValues(provider).Set(float64(count))
}

// ObserveRenderedBytes records generated Terraform output size.
func (m *Metrics) ObserveRenderedBytes(provider, resource string, bytes int) {
	if m == nil {
		return
	}
	m.RenderedBytes.WithLabelValues(provider, resource).Observe(float64(bytes))
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
