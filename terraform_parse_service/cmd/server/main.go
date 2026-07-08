// Command server starts the Terraform Parse Service HTTP server.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/app"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/logger"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/tracing"
)

const tracingShutdownTimeout = 5 * time.Second

func main() {
	rootCtx := context.Background()

	bootstrap, _ := zap.NewProduction()
	defer func() { _ = bootstrap.Sync() }()

	cfg, err := config.Load()
	if err != nil {
		bootstrap.Error("config load failed", zap.Error(err))
		os.Exit(1)
	}

	log, err := logger.New(cfg)
	if err != nil {
		bootstrap.Error("logger init failed", zap.Error(err))
		os.Exit(1)
	}
	defer func() { _ = log.Sync() }()
	zap.ReplaceGlobals(log)

	traceProvider, err := tracing.New(rootCtx, cfg)
	if err != nil {
		log.Error("tracer init failed", zap.Error(err))
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(rootCtx, tracingShutdownTimeout)
		defer cancel()
		if err := traceProvider.Shutdown(shutdownCtx); err != nil {
			log.Warn("tracer shutdown failed", zap.Error(err))
		}
	}()

	application, err := app.New(cfg, log)
	if err != nil {
		log.Error("app init failed", zap.Error(err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(rootCtx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("server exited", zap.Error(err))
		os.Exit(1)
	}
}
