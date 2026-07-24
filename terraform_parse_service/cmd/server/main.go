// Command server starts the Terraform Parse Service HTTP server.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/logger"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/server"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bootstrap, err := zap.NewProduction()
	if err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{
			"level": "error",
			"msg":   "bootstrap logger initialization failed",
			"error": err.Error(),
		})
		return 1
	}
	defer func() { _ = bootstrap.Sync() }()

	cfg, err := config.Load()
	if err != nil {
		bootstrap.Error("configuration load failed", zap.Error(err))
		return 1
	}

	log, err := logger.New(cfg)
	if err != nil {
		bootstrap.Error("service logger initialization failed", zap.Error(err))
		return 1
	}
	defer func() { _ = log.Sync() }()

	service, err := server.New(cfg, log)
	if err != nil {
		log.Error("service initialization failed", zap.Error(err))
		return 1
	}

	log.Info("service starting", zap.String("server.address", cfg.ListenAddr))
	if err := service.Run(ctx); err != nil {
		log.Error("server exited", zap.Error(err))
		return 1
	}
	log.Info("service stopped")
	return 0
}
