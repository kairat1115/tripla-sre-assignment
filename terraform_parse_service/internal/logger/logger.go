// Package logger builds the service logger from runtime configuration.
package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
)

// New returns a production zap logger with configured level and static
// metadata. Invalid configured levels fall back to info.
func New(cfg config.Config) (*zap.Logger, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(cfg.Logger.Level)); err != nil {
		level = zapcore.InfoLevel
	}
	zapCfg := zap.NewProductionConfig()
	zapCfg.Level = zap.NewAtomicLevelAt(level)
	l, err := zapCfg.Build()
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}
	fields := []zap.Field{
		zap.String("service_name", cfg.ServiceName),
		zap.String("environment", cfg.Environment),
		zap.String("version", cfg.Version),
	}
	for k, v := range cfg.Logger.Metadata {
		fields = append(fields, zap.String(k, v))
	}
	return l.With(fields...), nil
}
