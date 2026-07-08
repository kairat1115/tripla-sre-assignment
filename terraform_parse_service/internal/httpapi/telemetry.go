package httpapi

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type telemetryKey struct{}

type telemetryContext struct {
	mu      sync.Mutex
	fields  []zap.Field
	indexes map[string]int
}

func withTelemetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, telemetryKey{}, &telemetryContext{indexes: make(map[string]int)})
}

// AddSpanAttributes adds attributes to the current request span only. Handlers
// use AddLogFields separately for fields that belong in request logs.
func AddSpanAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetAttributes(attrs...)
	}
}

// AddLogFields adds fields to the structured request log without adding them to
// the active span.
func AddLogFields(ctx context.Context, fields ...zap.Field) {
	addTelemetryFields(ctx, fields...)
}

// AddSpanEvent records a named event on the active request span.
func AddSpanEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.AddEvent(name, trace.WithAttributes(attrs...))
	}
}

// RecordError records err on the active request span and mirrors error fields
// into the structured request log.
func RecordError(ctx context.Context, slug string, err error) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("exception.slug", slug),
			attribute.String("exception.type", fmt.Sprintf("%T", err)),
			attribute.Bool("error", true),
		)
	}
	addTelemetryFields(ctx,
		zap.String("exception.slug", slug),
		zap.String("exception.type", fmt.Sprintf("%T", err)),
		zap.String("exception.message", err.Error()),
		zap.Bool("error", true),
	)
}

func telemetryFields(ctx context.Context) []zap.Field {
	t, ok := ctx.Value(telemetryKey{}).(*telemetryContext)
	if !ok {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	fields := make([]zap.Field, len(t.fields))
	copy(fields, t.fields)
	return fields
}

func addTelemetryFields(ctx context.Context, fields ...zap.Field) {
	t, ok := ctx.Value(telemetryKey{}).(*telemetryContext)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, field := range fields {
		if idx, ok := t.indexes[field.Key]; ok {
			t.fields[idx] = field
			continue
		}
		t.indexes[field.Key] = len(t.fields)
		t.fields = append(t.fields, field)
	}
}
