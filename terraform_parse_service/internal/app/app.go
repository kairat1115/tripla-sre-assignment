// Package app wires the service dependencies and owns server lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"text/template"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/httpapi"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/render"
	awsresource "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource/aws"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/store"
)

const tracerName = "app"

// App owns the API server, metrics server, renderer, and template polling
// lifecycle.
type App struct {
	apiServer       *http.Server
	metricsServer   *http.Server
	renderer        *render.Renderer
	templateSources []templateSource
	logger          *zap.Logger
	metrics         *metrics.Metrics
}

// templateSource describes one provider template directory watched for reloads.
type templateSource struct {
	provider string
	dir      string
	interval time.Duration
}

// New builds the runtime application from configuration. It initializes
// provider stores, provider template sets, template polling sources, resource
// routes, and metrics serving.
func New(cfg config.Config, logger *zap.Logger) (*App, error) {
	m := metrics.New(prometheus.NewRegistry())
	renderer, sources, err := newRenderer(cfg, m)
	if err != nil {
		return nil, err
	}

	router := httpapi.NewRouter(m, logger)
	router.HandleUninstrumented(http.MethodGet, "/health", httpapi.NewHealthHandler(renderer, logger))
	awsresource.NewRouter(renderer, m).RegisterRoutes(router)

	return &App{
		apiServer: &http.Server{
			Addr:    cfg.ListenAddr,
			Handler: router.Handler(),
		},
		metricsServer:   m.Server(cfg.Metrics.Addr),
		renderer:        renderer,
		templateSources: sources,
		logger:          logger,
		metrics:         m,
	}, nil
}

// Run starts the API server, metrics server, and template polling loops. It
// blocks until ctx is canceled or a server exits unexpectedly, then performs a
// graceful server shutdown.
func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	shutdownCtx := context.WithoutCancel(ctx)

	errCh := make(chan error, 2)

	go serveHTTP(errCh, "api", a.apiServer, a.logger)
	go serveHTTP(errCh, "metrics", a.metricsServer, a.logger)
	a.watchTemplates(runCtx)

	select {
	case <-runCtx.Done():
		return a.Shutdown(shutdownCtx)
	case err := <-errCh:
		cancel()
		_ = a.Shutdown(shutdownCtx)
		return err
	}
}

// Shutdown gracefully stops the API and metrics HTTP servers using a bounded
// timeout.
func (a *App) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	err := errors.Join(
		a.apiServer.Shutdown(shutdownCtx),
		a.metricsServer.Shutdown(shutdownCtx),
	)
	if err != nil {
		return fmt.Errorf("shutdown servers: %w", err)
	}
	return nil
}

// serveHTTP runs one HTTP server and reports only unexpected exits.
func serveHTTP(errCh chan<- error, name string, server *http.Server, logger *zap.Logger) {
	logger.Info(name+" server starting", zap.String("addr", server.Addr))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s server exited: %w", name, err)
	}
}

// newRenderer creates provider stores, loads initial templates, and returns the
// template sources that should be polled after startup.
func newRenderer(cfg config.Config, m *metrics.Metrics) (*render.Renderer, []templateSource, error) {
	stores := make(map[string]store.Store, len(cfg.Providers))
	templates := make(map[string]*template.Template, len(cfg.Providers))
	sources := make([]templateSource, 0, len(cfg.Providers))

	for provider, pcfg := range cfg.Providers {
		st, err := store.NewFSStore(pcfg.StorageDir)
		if err != nil {
			return nil, nil, fmt.Errorf("init %s store: %w", provider, err)
		}
		tmpl, err := render.LoadTemplates(pcfg.TemplatesDir)
		if err != nil {
			return nil, nil, fmt.Errorf("load %s templates: %w", provider, err)
		}
		stores[provider] = st
		templates[provider] = tmpl
		sources = append(sources, templateSource{
			provider: provider,
			dir:      pcfg.TemplatesDir,
			interval: templatesPollInterval(pcfg),
		})
	}

	return render.New(stores, templates, m), sources, nil
}

// watchTemplates starts one polling goroutine for each configured provider.
func (a *App) watchTemplates(ctx context.Context) {
	for _, source := range a.templateSources {
		go a.watchProviderTemplates(ctx, source)
	}
}

// watchProviderTemplates reloads a provider only when its template signature
// changes.
func (a *App) watchProviderTemplates(ctx context.Context, source templateSource) {
	signature, err := render.TemplateSignature(source.dir)
	if err != nil {
		a.recordTemplateWatchError(ctx, "terraform.provider.template.scan", source, "err-template-signature", err)
		a.logger.Warn("template signature failed",
			zap.String("terraform.provider.name", source.provider),
			zap.String("terraform.provider.template.dir", source.dir),
			zap.Error(err),
		)
	}

	ticker := time.NewTicker(source.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nextSignature, err := render.TemplateSignature(source.dir)
			if err != nil {
				a.recordTemplateWatchError(ctx, "terraform.provider.template.scan", source, "err-template-scan", err)
				a.logger.Warn("template scan failed",
					zap.String("terraform.provider.name", source.provider),
					zap.String("terraform.provider.template.dir", source.dir),
					zap.Error(err),
				)
				continue
			}
			if nextSignature == signature {
				continue
			}
			if err := a.reloadProviderTemplates(ctx, source, signature, nextSignature); err != nil {
				a.logger.Error("template reload failed",
					zap.String("terraform.provider.name", source.provider),
					zap.String("terraform.provider.template.dir", source.dir),
					zap.Error(err),
				)
				continue
			}
			signature = nextSignature
			a.logger.Info("templates reloaded",
				zap.String("terraform.provider.name", source.provider),
				zap.String("terraform.provider.template.dir", source.dir),
				zap.String("terraform.provider.template.signature", nextSignature),
			)
		}
	}
}

// reloadProviderTemplates records trace and metric data for one template reload
// attempt.
func (a *App) reloadProviderTemplates(ctx context.Context, source templateSource, currentSignature, nextSignature string) error {
	start := time.Now()
	ctx, span := otel.Tracer(tracerName).Start(ctx, "terraform.provider.template.reload",
		trace.WithAttributes(templateSourceAttributes(source,
			attribute.String("terraform.provider.template.signature.current", currentSignature),
			attribute.String("terraform.provider.template.signature.next", nextSignature),
		)...),
	)
	defer span.End()

	span.AddEvent("terraform.provider.template.reload.start")
	if err := a.renderer.ReloadTemplates(source.provider, source.dir); err != nil {
		duration := time.Since(start).Seconds()
		a.metrics.ObserveTemplateReload(source.provider, "error", time.Since(start))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(
			attribute.String("exception.slug", "err-template-reload"),
			attribute.String("exception.message", err.Error()),
			attribute.Bool("error", true),
			attribute.Float64("terraform.provider.template.reload.duration", duration),
		)
		return err
	}
	duration := time.Since(start).Seconds()
	a.metrics.ObserveTemplateReload(source.provider, "success", time.Since(start))
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(
		attribute.Bool("terraform.provider.template.reload.changed", true),
		attribute.Float64("terraform.provider.template.reload.duration", duration),
	)
	span.AddEvent("terraform.provider.template.reload.success",
		trace.WithAttributes(attribute.Float64("terraform.provider.template.reload.duration", duration)),
	)
	return nil
}

// recordTemplateWatchError emits a short error span for polling failures that
// happen before a reload attempt starts.
func (a *App) recordTemplateWatchError(ctx context.Context, spanName string, source templateSource, slug string, err error) {
	_, span := otel.Tracer(tracerName).Start(ctx, spanName,
		trace.WithAttributes(templateSourceAttributes(source)...),
	)
	defer span.End()

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(
		attribute.String("exception.slug", slug),
		attribute.String("exception.message", err.Error()),
		attribute.Bool("error", true),
	)
}

// templateSourceAttributes returns shared trace attributes for template polling
// and reload spans.
func templateSourceAttributes(source templateSource, extra ...attribute.KeyValue) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("terraform.provider.name", source.provider),
		attribute.String("terraform.provider.template.dir", source.dir),
		attribute.Float64("terraform.provider.template.poll_interval", source.interval.Seconds()),
	}
	return append(attrs, extra...)
}

// templatesPollInterval returns the configured provider poll interval or the
// service default when the value is empty or invalid.
func templatesPollInterval(pcfg config.ProviderConfig) time.Duration {
	if pcfg.TemplatesPollInterval == "" {
		return 5 * time.Second
	}
	interval, err := time.ParseDuration(pcfg.TemplatesPollInterval)
	if err != nil {
		return 5 * time.Second
	}
	return interval
}
