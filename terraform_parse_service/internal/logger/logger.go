// Package logger builds the service logger from runtime configuration.
package logger

import (
	"fmt"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
)

const instrumentationName = "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/logger"

// New returns a production zap logger that writes JSON to stdout and forwards
// records to the global OpenTelemetry LoggerProvider installed by
// auto-instrumentation.
func New(cfg config.Config) (*zap.Logger, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(cfg.Logger.Level)); err != nil {
		return nil, fmt.Errorf("parse logger level %q: %w", cfg.Logger.Level, err)
	}
	zapCfg := zap.NewProductionConfig()
	zapCfg.Encoding = "json"
	zapCfg.Level = zap.NewAtomicLevelAt(level)
	l, err := zapCfg.Build()
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}
	stdoutFields := []zap.Field{
		zap.String("service.name", cfg.ServiceName),
		zap.String("deployment.environment.name", cfg.Environment),
		zap.String("service.version", cfg.Version),
	}
	l = l.WithOptions(
		zap.WrapCore(func(core zapcore.Core) zapcore.Core {
			return zapcore.NewTee(
				core.With(stdoutFields),
				otelzap.NewCore(instrumentationName),
			)
		}),
		zap.IncreaseLevel(level),
	)
	return l, nil
}
