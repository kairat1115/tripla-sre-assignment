package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
)

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
	fields := []zap.Field{zap.String("service_name", cfg.ServiceName)}
	for k, v := range cfg.Logger.Metadata {
		fields = append(fields, zap.String(k, v))
	}
	return l.With(fields...), nil
}
