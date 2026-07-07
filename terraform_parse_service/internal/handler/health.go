package handler

import (
	"net/http"
	"text/template"

	"go.uber.org/zap"
)

type HealthHandler struct {
	templates map[string]*template.Template
	logger    *zap.Logger
}

func NewHealthHandler(templates map[string]*template.Template, logger *zap.Logger) *HealthHandler {
	return &HealthHandler{templates: templates, logger: logger}
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(h.templates) == 0 {
		h.logger.Warn("health check failed", zap.String("reason", "no providers configured"))
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	for provider, tmpl := range h.templates {
		if len(tmpl.Templates()) == 0 {
			h.logger.Warn("health check failed", zap.String("reason", "templates directory is empty"), zap.String("provider", provider))
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}
