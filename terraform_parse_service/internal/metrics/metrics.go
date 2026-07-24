// Package metrics registers the service's domain-specific Prometheus metrics.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}

// Metrics groups the collectors shared across handlers and services.
type Metrics struct {
	reg                *prometheus.Registry
	generationTotal    *prometheus.CounterVec
	generationDuration *prometheus.HistogramVec
	renderedBytes      *prometheus.HistogramVec
	templateReloads    *prometheus.CounterVec
	templateDuration   *prometheus.HistogramVec
	templatesLoaded    *prometheus.GaugeVec
}

// New registers all service collectors in reg.
func New(reg *prometheus.Registry) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		reg: reg,
		generationTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "terraform_generation_total",
			Help: "Total number of terraform generation tasks.",
		}, []string{"provider", "resource", "status"}),

		generationDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "terraform_generation_duration_seconds",
			Help:    "Duration of terraform generation tasks.",
			Buckets: durationBuckets,
		}, []string{"provider", "resource"}),

		renderedBytes: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "terraform_rendered_bytes",
			Help:    "Size of rendered Terraform output.",
			Buckets: []float64{128, 512, 1024, 4096, 16384, 65536, 262144, 1048576},
		}, []string{"provider", "resource"}),

		templateReloads: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "terraform_template_reloads_total",
			Help: "Total provider template reload attempts.",
		}, []string{"provider", "status"}),

		templateDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "terraform_template_reload_duration_seconds",
			Help:    "Duration of provider template reload attempts.",
			Buckets: durationBuckets,
		}, []string{"provider"}),

		templatesLoaded: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "terraform_templates_loaded",
			Help: "Number of parsed templates currently loaded per provider.",
		}, []string{"provider"}),
	}
}

// ObserveGeneration records one Terraform generation attempt.
func (m *Metrics) ObserveGeneration(provider, resource, status string, duration time.Duration) {
	m.generationTotal.WithLabelValues(provider, resource, status).Inc()
	m.generationDuration.WithLabelValues(provider, resource).Observe(duration.Seconds())
}

// ObserveTemplateReload records one provider template reload attempt.
func (m *Metrics) ObserveTemplateReload(provider, status string, duration time.Duration) {
	m.templateReloads.WithLabelValues(provider, status).Inc()
	m.templateDuration.WithLabelValues(provider).Observe(duration.Seconds())
}

// SetTemplatesLoaded stores the current parsed template count for provider.
func (m *Metrics) SetTemplatesLoaded(provider string, count int) {
	m.templatesLoaded.WithLabelValues(provider).Set(float64(count))
}

// ObserveRenderedBytes records generated Terraform output size.
func (m *Metrics) ObserveRenderedBytes(provider, resource string, bytes int) {
	m.renderedBytes.WithLabelValues(provider, resource).Observe(float64(bytes))
}

// Handler returns an HTTP handler exposing Prometheus metrics at /metrics.
func (m *Metrics) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}))
	return mux
}
