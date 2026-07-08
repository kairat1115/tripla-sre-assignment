// Package tracing configures OpenTelemetry tracing for the service.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
)

// New creates and installs the global OpenTelemetry tracer provider, resource,
// sampler, exporter, and W3C propagators. The returned provider must be shut
// down during service shutdown so queued spans can flush.
func New(ctx context.Context, cfg config.Config) (*sdktrace.TracerProvider, error) {
	var exp sdktrace.SpanExporter
	var err error
	switch cfg.Tracing.Exporter {
	case "stdout":
		exp, err = stdouttrace.New()
	case "otlp_grpc":
		endpoint := cfg.Tracing.Endpoint
		if endpoint == "" {
			endpoint = "localhost:4317"
		}
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
		if cfg.Tracing.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		exp, err = otlptracegrpc.New(ctx, opts...)
	default:
		return nil, fmt.Errorf("unknown trace exporter %q: supported values are stdout, otlp_grpc", cfg.Tracing.Exporter)
	}
	if err != nil {
		return nil, fmt.Errorf("build trace exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.ServiceVersionKey.String(cfg.Version),
			attribute.String("service.environment", cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}
	sampler := sdktrace.ParentBased(sdktrace.AlwaysSample())
	if cfg.Tracing.SampleRatio < 1.0 {
		sampler = sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Tracing.SampleRatio))
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp, nil
}
