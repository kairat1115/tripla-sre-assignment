// Package server wires the HTTP API and owns its runtime lifecycle.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/aws"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
	terraformservice "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/terraform"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 5 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
)

type templateWatcher struct {
	providerName string
	interval     time.Duration
}

type errorResponse struct {
	Error string `json:"error"`
}

var errNotFound = errors.New(http.StatusText(http.StatusNotFound))

// Server owns the API server, metrics server, and template polling workers.
type Server struct {
	apiServer     *http.Server
	metricsServer *http.Server
	terraform     *terraformservice.Service
	watchers      []templateWatcher
	log           *zap.Logger
	domainMetrics *metrics.Metrics
}

// New validates cfg and builds the service runtime.
func New(cfg config.Config, log *zap.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	domainMetrics := metrics.New(prometheus.NewRegistry())
	terraformService := terraformservice.New(domainMetrics)
	watchers := make([]templateWatcher, 0, len(cfg.Providers))

	for name, providerConfig := range cfg.Providers {
		outputs, err := storage.NewFilesystem(providerConfig.StorageDir)
		if err != nil {
			return nil, fmt.Errorf("initialize %s storage: %w", name, err)
		}
		if err := terraformService.AddProvider(name, providerConfig.TemplatesDir, outputs); err != nil {
			return nil, err
		}
		interval, err := providerConfig.TemplatePollInterval()
		if err != nil {
			return nil, fmt.Errorf("parse %s template poll interval: %w", name, err)
		}
		watchers = append(watchers, templateWatcher{providerName: name, interval: interval})
	}

	s := &Server{
		terraform:     terraformService,
		watchers:      watchers,
		log:           log,
		domainMetrics: domainMetrics,
	}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /health", health)
	if _, configured := cfg.Providers["aws"]; configured {
		aws.RegisterRoutes(apiMux, terraformService, log)
	}
	apiMux.HandleFunc("/", notFound)

	s.apiServer = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           apiMux,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	s.metricsServer = &http.Server{
		Addr:              cfg.Metrics.Addr,
		Handler:           domainMetrics.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	return s, nil
}

// Handler returns the API handler. It is primarily useful for integration
// tests that do not need to bind a real port.
func (s *Server) Handler() http.Handler {
	return s.apiServer.Handler
}

// Run serves the API and metrics endpoints until ctx is canceled or a server
// exits unexpectedly.
func (s *Server) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go serve(errCh, "api", s.apiServer, s.log)
	go serve(errCh, "metrics", s.metricsServer, s.log)
	for _, watcher := range s.watchers {
		go s.watchTemplates(runCtx, watcher)
	}

	var runErr error
	select {
	case <-runCtx.Done():
		s.log.Info("shutdown requested")
	case runErr = <-errCh:
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer shutdownCancel()
	return errors.Join(
		runErr,
		s.apiServer.Shutdown(shutdownCtx),
		s.metricsServer.Shutdown(shutdownCtx),
	)
}

func serve(errCh chan<- error, name string, server *http.Server, log *zap.Logger) {
	log.Info("server listening",
		zap.String("service.listener.name", name),
		zap.String("server.address", server.Addr),
	)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s server: %w", name, err)
	}
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: errNotFound.Error()})
}

func (s *Server) watchTemplates(ctx context.Context, watcher templateWatcher) {
	signature, err := s.terraform.TemplateSignature(watcher.providerName)
	if err != nil {
		s.log.Warn("template scan failed",
			zap.String("terraform.provider.name", watcher.providerName),
			zap.Error(err),
		)
	}

	ticker := time.NewTicker(watcher.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nextSignature, err := s.terraform.TemplateSignature(watcher.providerName)
			if err != nil {
				s.log.Warn("template scan failed",
					zap.String("terraform.provider.name", watcher.providerName),
					zap.Error(err),
				)
				continue
			}
			if nextSignature == signature {
				continue
			}

			// Remember the observed content even when it is invalid so one bad
			// update produces one error instead of one error per poll.
			signature = nextSignature
			start := time.Now()
			templateCount, err := s.terraform.Reload(watcher.providerName)
			duration := time.Since(start)
			if err != nil {
				s.domainMetrics.ObserveTemplateReload(watcher.providerName, "error", duration)
				s.log.Error("template reload failed",
					zap.String("terraform.provider.name", watcher.providerName),
					zap.Float64("terraform.provider.template.reload.duration", duration.Seconds()),
					zap.Error(err),
				)
				continue
			}

			s.domainMetrics.ObserveTemplateReload(watcher.providerName, "success", duration)
			s.log.Info("templates reloaded",
				zap.String("terraform.provider.name", watcher.providerName),
				zap.Int("terraform.provider.template.count", templateCount),
				zap.Float64("terraform.provider.template.reload.duration", duration.Seconds()),
			)
		}
	}
}
