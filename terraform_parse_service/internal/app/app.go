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
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/httpapi"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/render"
	s3resource "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource/aws/s3"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/store"
)

// App groups the API and metrics servers so they can be started and stopped
// together.
type App struct {
	apiServer       *http.Server
	metricsServer   *http.Server
	renderer        *render.Renderer
	templateSources []templateSource
	logger          *zap.Logger
}

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
	router.Handle(http.MethodGet, "/health", httpapi.NewHealthHandler(renderer, logger))
	s3resource.NewBucketHandler(renderer).RegisterRoutes(router)

	return &App{
		apiServer: &http.Server{
			Addr:    cfg.ListenAddr,
			Handler: router.Handler(),
		},
		metricsServer:   m.Server(cfg.Metrics.Addr),
		renderer:        renderer,
		templateSources: sources,
		logger:          logger,
	}, nil
}

// Run starts the API server, metrics server, and template polling loops. It
// blocks until ctx is canceled or a server exits with an unexpected error.
func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)

	go serveHTTP(errCh, "api", a.apiServer, a.logger)
	go serveHTTP(errCh, "metrics", a.metricsServer, a.logger)
	a.watchTemplates(runCtx)

	select {
	case <-runCtx.Done():
		return a.Shutdown(context.Background())
	case err := <-errCh:
		cancel()
		_ = a.Shutdown(context.Background())
		return err
	}
}

// Shutdown gracefully stops both HTTP servers using a bounded timeout.
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

func serveHTTP(errCh chan<- error, name string, server *http.Server, logger *zap.Logger) {
	logger.Info(name+" server starting", zap.String("addr", server.Addr))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s server exited: %w", name, err)
	}
}

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

func (a *App) watchTemplates(ctx context.Context) {
	for _, source := range a.templateSources {
		source := source
		go a.watchProviderTemplates(ctx, source)
	}
}

func (a *App) watchProviderTemplates(ctx context.Context, source templateSource) {
	signature, err := render.TemplateSignature(source.dir)
	if err != nil {
		a.logger.Warn("template signature failed",
			zap.String("provider", source.provider),
			zap.String("templates_dir", source.dir),
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
				a.logger.Warn("template scan failed",
					zap.String("provider", source.provider),
					zap.String("templates_dir", source.dir),
					zap.Error(err),
				)
				continue
			}
			if nextSignature == signature {
				continue
			}
			if err := a.renderer.ReloadTemplates(source.provider, source.dir); err != nil {
				a.logger.Error("template reload failed",
					zap.String("provider", source.provider),
					zap.String("templates_dir", source.dir),
					zap.Error(err),
				)
				continue
			}
			signature = nextSignature
			a.logger.Info("templates reloaded",
				zap.String("provider", source.provider),
				zap.String("templates_dir", source.dir),
			)
		}
	}
}

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
