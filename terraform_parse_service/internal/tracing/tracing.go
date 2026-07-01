package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
)

func New(ctx context.Context, serviceName string, cfg config.TracingConfig) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	var exp sdktrace.SpanExporter
	var err error
	switch cfg.Exporter {
	case "stdout":
		exp, err = stdouttrace.New()
	case "otlp_grpc":
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = "localhost:4317"
		}
		exp, err = otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint))
	default:
		return nil, nil, fmt.Errorf("unknown trace exporter %q: supported values are stdout, otlp_grpc", cfg.Exporter)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("build trace exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build trace resource: %w", err)
	}
	sampler := sdktrace.AlwaysSample()
	if cfg.SampleRatio < 1.0 {
		sampler = sdktrace.TraceIDRatioBased(cfg.SampleRatio)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	otel.SetTracerProvider(tp)
	return tp, tp.Shutdown, nil
}
