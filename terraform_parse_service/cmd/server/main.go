package main

import (
	"context"
	"net/http"
	"os"
	"text/template"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
	s3handler "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler/aws/s3"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/logger"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/tracing"
)

func main() {
	bootstrap, _ := zap.NewProduction()
	defer bootstrap.Sync()

	cfg, err := config.Load()
	if err != nil {
		bootstrap.Error("config load failed", zap.String("error", err.Error()))
		os.Exit(1)
	}

	l, err := logger.New(cfg)
	if err != nil {
		bootstrap.Error("logger init failed", zap.String("error", err.Error()))
		os.Exit(1)
	}
	defer l.Sync()
	zap.ReplaceGlobals(l)

	_, shutdown, err := tracing.New(context.Background(), cfg)
	if err != nil {
		zap.L().Error("tracer init failed", zap.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()

	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	m.Serve(cfg.Metrics.Addr, zap.L())

	writers := make(map[string]storage.Writer)
	templates := make(map[string]*template.Template)
	for provider, pcfg := range cfg.Providers {
		w, err := storage.NewFSWriter(pcfg.StorageDir)
		if err != nil {
			zap.L().Error("storage init failed", zap.String("provider", provider), zap.String("error", err.Error()))
			os.Exit(1)
		}
		tmpl, err := service.LoadTemplates(pcfg.TemplatesDir)
		if err != nil {
			zap.L().Error("template load failed", zap.String("provider", provider), zap.String("error", err.Error()))
			os.Exit(1)
		}
		writers[provider] = w
		templates[provider] = tmpl
	}

	tfSvc := service.NewTerraformService(writers, templates, m)

	mux := http.NewServeMux()
	mux.Handle("GET /health", handler.NewHealthHandler(templates, l))
	mux.Handle("POST /api/aws/v1/s3/buckets", s3handler.NewBucketHandler(tfSvc, l, m))

	providerNames := make([]string, 0, len(cfg.Providers))
	for p := range cfg.Providers {
		providerNames = append(providerNames, p)
	}
	zap.L().Info("server starting",
		zap.String("addr", cfg.ListenAddr),
		zap.Strings("providers", providerNames),
	)
	if err := http.ListenAndServe(cfg.ListenAddr, handler.Middleware(mux)); err != nil {
		zap.L().Error("server exited", zap.String("error", err.Error()))
		os.Exit(1)
	}
}
