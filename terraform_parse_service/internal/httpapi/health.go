package httpapi

import (
	"net/http"

	"go.uber.org/zap"
)

// TemplateStatus exposes a safe snapshot of parsed template availability.
type TemplateStatus interface {
	// TemplateCounts returns the number of parsed templates per provider.
	TemplateCounts() map[string]int
}

// HealthHandler reports whether providers have parsed templates available. It
// logs failing health checks but leaves successful probes silent.
type HealthHandler struct {
	status TemplateStatus
	logger *zap.Logger
}

// NewHealthHandler creates a health handler backed by a template status source.
func NewHealthHandler(status TemplateStatus, logger *zap.Logger) *HealthHandler {
	return &HealthHandler{status: status, logger: logger}
}

// ServeHTTP returns 200 when at least one provider is configured and each
// provider has templates available; otherwise it returns 503.
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	counts := h.status.TemplateCounts()
	if len(counts) == 0 {
		h.logger.Warn("health check failed", zap.String("reason", "no providers configured"))
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	for provider, count := range counts {
		if count == 0 {
			h.logger.Warn("health check failed",
				zap.String("reason", "templates directory is empty"),
				zap.String("terraform.provider.name", provider),
			)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}
