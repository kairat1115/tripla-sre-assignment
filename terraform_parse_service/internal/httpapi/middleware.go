// Package httpapi contains HTTP routing, response, middleware, and health check
// helpers shared by resource handlers.
package httpapi

import (
	"bytes"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
)

const tracerName = "httpapi"

// Router wraps http.ServeMux and automatically applies route-level
// instrumentation to registered handlers.
type Router struct {
	mux     *http.ServeMux
	metrics *metrics.Metrics
	logger  *zap.Logger
}

// NewRouter creates an instrumented router using stable route labels for
// metrics and traces.
func NewRouter(m *metrics.Metrics, logger *zap.Logger) *Router {
	return &Router{
		mux:     http.NewServeMux(),
		metrics: m,
		logger:  logger,
	}
}

// Handle registers a method and path pattern with shared HTTP instrumentation.
func (r *Router) Handle(method, pattern string, h http.Handler) {
	r.mux.Handle(method+" "+pattern, Instrument(r.metrics, r.logger, method, pattern, h))
}

// HandleUninstrumented registers a method and path pattern without request
// logs, request metrics, or a server span. Use it only for high-volume
// machine endpoints such as health checks.
func (r *Router) HandleUninstrumented(method, pattern string, h http.Handler) {
	r.mux.Handle(method+" "+pattern, h)
}

// Handler returns the complete HTTP handler with normalized JSON error output.
func (r *Router) Handler() http.Handler {
	return NormalizeErrors(r.mux)
}

// Instrument wraps a handler with request duration, in-flight, total request
// metrics, request logging, and an OpenTelemetry server span.
func Instrument(m *metrics.Metrics, logger *zap.Logger, method, route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		requestBytes := r.ContentLength
		if requestBytes < 0 {
			requestBytes = 0
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		m.HTTPInFlight.WithLabelValues(method, route).Inc()
		defer func() {
			m.HTTPInFlight.WithLabelValues(method, route).Dec()
			m.HTTPDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
			m.HTTPRequestsTotal.WithLabelValues(method, route, strconv.Itoa(rec.status)).Inc()
		}()

		parentCtx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer(tracerName).Start(parentCtx, "http.request",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", method),
				attribute.String("http.route", route),
				attribute.String("url.scheme", scheme),
				attribute.String("server.address", r.Host),
				attribute.String("network.peer.address", r.RemoteAddr),
				attribute.String("http.request.header.content_type", r.Header.Get("Content-Type")),
				attribute.Int64("http.request.body.size", requestBytes),
			),
		)
		defer span.End()
		ctx = withTelemetry(ctx)

		next.ServeHTTP(rec, r.WithContext(ctx))

		duration := time.Since(start)
		span.SetAttributes(attribute.Int("http.response.status_code", rec.status))
		span.SetAttributes(attribute.Int64("http.response.body.size", rec.bytesWritten))
		if rec.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(rec.status))
			span.SetAttributes(attribute.Bool("error", true))
		} else {
			span.SetStatus(codes.Ok, "")
		}
		fields := []zap.Field{
			zap.String("http.request.method", method),
			zap.String("http.route", route),
			zap.String("url.scheme", scheme),
			zap.String("server.address", r.Host),
			zap.Int("http.response.status_code", rec.status),
			zap.Int64("http.request.body.size", requestBytes),
			zap.Int64("http.response.body.size", rec.bytesWritten),
			zap.String("http.request.header.content_type", r.Header.Get("Content-Type")),
			zap.String("network.peer.address", r.RemoteAddr),
			zap.String("trace_id", span.SpanContext().TraceID().String()),
			zap.String("span_id", span.SpanContext().SpanID().String()),
			zap.Float64("http.server.request.duration", duration.Seconds()),
		}
		fields = append(fields, telemetryFields(ctx)...)
		if rec.status >= 500 {
			logger.Error("request handled", fields...)
			return
		}
		logger.Info("request handled", fields...)
	})
}

// NormalizeErrors converts standard library 404/405 responses into the same
// JSON error shape used by explicit resource handlers.
func NormalizeErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &bufferedRecorder{header: w.Header().Clone(), status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status >= 400 && rec.header.Get("Content-Type") != "application/json" {
			Error(w, rec.status, http.StatusText(rec.status))
			return
		}
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(r.status)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += int64(n)
	return n, err
}

type bufferedRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *bufferedRecorder) Header() http.Header         { return r.header }
func (r *bufferedRecorder) WriteHeader(code int)        { r.status = code }
func (r *bufferedRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
