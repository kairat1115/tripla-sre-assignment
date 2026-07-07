package s3

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
)

const tracerName = "handler.aws.s3"

var bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9\-\.]*[a-z0-9]$`)

type bucketProperties struct {
	Region     string `json:"aws-region"`
	ACL        string `json:"acl"`
	BucketName string `json:"bucket-name"`
}

func validateBucketName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("missing required property: bucket-name")
	case len(name) < 3 || len(name) > 63:
		return fmt.Errorf("invalid bucket-name: must be 3–63 characters")
	case !bucketNameRE.MatchString(name):
		return fmt.Errorf("invalid bucket-name: must contain only lowercase letters, digits, hyphens, and dots, and start/end with a letter or digit")
	case strings.Contains(name, ".."):
		return fmt.Errorf("invalid bucket-name: must not contain consecutive dots")
	default:
		return nil
	}
}

func (p bucketProperties) Validate() error {
	switch {
	case p.Region == "":
		return fmt.Errorf("missing required property: aws-region")
	case p.ACL == "":
		return fmt.Errorf("missing required property: acl")
	default:
		return validateBucketName(p.BucketName)
	}
}

type bucketRequest struct {
	Payload struct {
		Properties bucketProperties `json:"properties"`
	} `json:"payload"`
}

type bucketResponse struct {
	OutputPath string `json:"output_path"`
}

type bucketGenerator struct {
	props bucketProperties
}

func (g *bucketGenerator) Provider() string     { return "aws" }
func (g *bucketGenerator) TemplateName() string { return "s3/bucket.tf.tmpl" }
func (g *bucketGenerator) StoragePath() string  { return "s3/" + g.props.BucketName }
func (g *bucketGenerator) TemplateData() any {
	return struct {
		Properties map[string]string
	}{
		Properties: map[string]string{
			"aws-region":  g.props.Region,
			"acl":         g.props.ACL,
			"bucket-name": g.props.BucketName,
		},
	}
}

type resourceLocator struct {
	provider string
	path     string
}

func (l *resourceLocator) Provider() string    { return l.provider }
func (l *resourceLocator) StoragePath() string { return l.path }

type BucketHandler struct {
	svc    handler.Terraform
	logger *zap.Logger
	m      *metrics.Metrics
}

func NewBucketHandler(svc handler.Terraform, logger *zap.Logger, m *metrics.Metrics) *BucketHandler {
	return &BucketHandler{svc: svc, logger: logger, m: m}
}

func (h *BucketHandler) Create() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		method := r.Method
		path := r.URL.Path

		h.m.HTTPInFlight.WithLabelValues(method, path).Inc()
		defer func() {
			h.m.HTTPInFlight.WithLabelValues(method, path).Dec()
			h.m.HTTPDuration.WithLabelValues(method, path).Observe(time.Since(start).Seconds())
		}()

		respond := func(result handler.Result) {
			h.m.HTTPRequestsTotal.WithLabelValues(method, path, strconv.Itoa(result.Code)).Inc()
			handler.Respond(w, result)
		}

		ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", method),
				attribute.String("http.route", "POST /api/aws/v1/s3/buckets"),
				attribute.String("network.peer.address", r.RemoteAddr),
			),
		)
		defer span.End()

		base := []zap.Field{
			zap.String("http.request.method", method),
			zap.String("http.route", "POST /api/aws/v1/s3/buckets"),
			zap.String("network.peer.address", r.RemoteAddr),
			zap.String("trace_id", span.SpanContext().TraceID().String()),
		}

		readStart := time.Now()
		body, err := io.ReadAll(r.Body)
		span.SetAttributes(attribute.Float64("read_body.duration_ms", float64(time.Since(readStart).Milliseconds())))
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.Int("http.response.status_code", http.StatusBadRequest),
				attribute.String("exception.slug", "err-handler-body-read"),
				attribute.Bool("error", true),
			)
			h.logger.Info("request body read failed", append(base,
				zap.Int("http.response.status_code", http.StatusBadRequest),
				zap.String("exception.message", err.Error()),
				zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
			)...)
			respond(handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
			return
		}

		decodeStart := time.Now()
		var req bucketRequest
		if err := json.Unmarshal(body, &req); err != nil {
			span.SetAttributes(attribute.Float64("json_decode.duration_ms", float64(time.Since(decodeStart).Milliseconds())))
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.Int("http.response.status_code", http.StatusBadRequest),
				attribute.String("exception.slug", "err-handler-json-decode"),
				attribute.Bool("error", true),
			)
			h.logger.Info("request body decode failed", append(base,
				zap.Int("http.response.status_code", http.StatusBadRequest),
				zap.String("exception.message", err.Error()),
				zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
			)...)
			respond(handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
			return
		}
		span.SetAttributes(attribute.Float64("json_decode.duration_ms", float64(time.Since(decodeStart).Milliseconds())))

		p := req.Payload.Properties
		validateStart := time.Now()
		if err := p.Validate(); err != nil {
			span.SetAttributes(attribute.Float64("validate.duration_ms", float64(time.Since(validateStart).Milliseconds())))
			msg := err.Error()
			span.SetStatus(codes.Error, msg)
			span.SetAttributes(
				attribute.Int("http.response.status_code", http.StatusUnprocessableEntity),
				attribute.String("exception.slug", "err-handler-validation"),
				attribute.Bool("error", true),
				attribute.String("service.aws.s3.region", p.Region),
				attribute.String("service.aws.s3.acl", p.ACL),
				attribute.String("service.aws.s3.bucket_name", p.BucketName),
			)
			h.logger.Info(msg, append(base,
				zap.Int("http.response.status_code", http.StatusUnprocessableEntity),
				zap.String("service.aws.s3.region", p.Region),
				zap.String("service.aws.s3.acl", p.ACL),
				zap.String("service.aws.s3.bucket_name", p.BucketName),
				zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
			)...)
			respond(handler.Result{Code: http.StatusUnprocessableEntity, Msg: msg, Err: err})
			return
		}
		span.SetAttributes(attribute.Float64("validate.duration_ms", float64(time.Since(validateStart).Milliseconds())))
		span.SetAttributes(
			attribute.String("aws.s3.region", p.Region),
			attribute.String("aws.s3.acl", p.ACL),
			attribute.String("aws.s3.bucket_name", p.BucketName),
		)

		gen := &bucketGenerator{props: p}
		outputPath, err := h.svc.Generate(ctx, gen)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.Int("http.response.status_code", http.StatusInternalServerError),
				attribute.String("exception.slug", "err-handler-generate"),
				attribute.Bool("error", true),
			)
			h.logger.Error("generation failed", append(base,
				zap.Int("http.response.status_code", http.StatusInternalServerError),
				zap.String("aws.s3.region", p.Region),
				zap.String("aws.s3.acl", p.ACL),
				zap.String("aws.s3.bucket_name", p.BucketName),
				zap.String("exception.message", err.Error()),
				zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
			)...)
			respond(handler.Result{Code: http.StatusInternalServerError, Msg: "generation failed", Err: err})
			return
		}

		span.SetStatus(codes.Ok, "")
		span.SetAttributes(
			attribute.Int("http.response.status_code", http.StatusCreated),
			attribute.String("output.path", outputPath),
		)
		h.logger.Info("terraform config generated", append(base,
			zap.Int("http.response.status_code", http.StatusCreated),
			zap.String("aws.s3.region", p.Region),
			zap.String("aws.s3.acl", p.ACL),
			zap.String("aws.s3.bucket_name", p.BucketName),
			zap.String("output.path", outputPath),
			zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
		)...)
		respond(handler.Result{Code: http.StatusCreated, Data: bucketResponse{OutputPath: outputPath}})
	}
}

func (h *BucketHandler) List() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", "GET /api/aws/v1/s3/buckets"),
				attribute.String("network.peer.address", r.RemoteAddr),
			),
		)
		defer span.End()

		buckets, err := h.svc.List(ctx, &resourceLocator{provider: "aws", path: "s3/"})
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-list-buckets"),
				attribute.Bool("error", true),
			)
			handler.WriteError(w, http.StatusInternalServerError, "list failed")
			return
		}
		span.SetStatus(codes.Ok, "")
		span.SetAttributes(attribute.Int("aws.s3.bucket_count", len(buckets)))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string][]string{"buckets": buckets})
	}
}

func (h *BucketHandler) Get() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", "GET /api/aws/v1/s3/buckets/{bucket_name}"),
				attribute.String("network.peer.address", r.RemoteAddr),
				attribute.String("aws.s3.bucket_name", bucketName),
			),
		)
		defer span.End()

		if err := validateBucketName(bucketName); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-validation"),
				attribute.Bool("error", true),
			)
			handler.WriteError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}

		content, err := h.svc.Read(ctx, &resourceLocator{provider: "aws", path: "s3/" + bucketName})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				span.SetStatus(codes.Error, err.Error())
				span.SetAttributes(
					attribute.String("exception.slug", "err-handler-bucket-not-found"),
					attribute.Bool("error", true),
				)
				handler.WriteError(w, http.StatusNotFound, "bucket not found")
				return
			}
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-read-bucket"),
				attribute.Bool("error", true),
			)
			handler.WriteError(w, http.StatusInternalServerError, "read failed")
			return
		}
		span.SetStatus(codes.Ok, "")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}
}

func (h *BucketHandler) Update() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		start := time.Now()
		path := r.URL.Path

		h.m.HTTPInFlight.WithLabelValues(r.Method, path).Inc()
		defer func() {
			h.m.HTTPInFlight.WithLabelValues(r.Method, path).Dec()
			h.m.HTTPDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
		}()

		respond := func(result handler.Result) {
			h.m.HTTPRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(result.Code)).Inc()
			handler.Respond(w, result)
		}

		ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", "PUT /api/aws/v1/s3/buckets/{bucket_name}"),
				attribute.String("network.peer.address", r.RemoteAddr),
				attribute.String("aws.s3.bucket_name", bucketName),
			),
		)
		defer span.End()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-body-read"),
				attribute.Bool("error", true),
			)
			respond(handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
			return
		}

		var req bucketRequest
		if err := json.Unmarshal(body, &req); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-json-decode"),
				attribute.Bool("error", true),
			)
			respond(handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
			return
		}

		p := req.Payload.Properties
		if p.BucketName != "" && p.BucketName != bucketName {
			span.SetStatus(codes.Error, "bucket-name mismatch")
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-bucket-name-mismatch"),
				attribute.Bool("error", true),
			)
			respond(handler.Result{Code: http.StatusUnprocessableEntity, Msg: "bucket-name in body must match path parameter", Err: fmt.Errorf("bucket-name mismatch")})
			return
		}
		p.BucketName = bucketName

		if err := p.Validate(); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-validation"),
				attribute.Bool("error", true),
			)
			respond(handler.Result{Code: http.StatusUnprocessableEntity, Msg: err.Error(), Err: err})
			return
		}
		span.SetAttributes(
			attribute.String("aws.s3.region", p.Region),
			attribute.String("aws.s3.acl", p.ACL),
		)

		gen := &bucketGenerator{props: p}
		outputPath, err := h.svc.Generate(ctx, gen)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-generate"),
				attribute.Bool("error", true),
			)
			respond(handler.Result{Code: http.StatusInternalServerError, Msg: "generation failed", Err: err})
			return
		}
		span.SetStatus(codes.Ok, "")
		span.SetAttributes(attribute.String("output.path", outputPath))
		respond(handler.Result{Code: http.StatusOK, Data: bucketResponse{OutputPath: outputPath}})
	}
}

func (h *BucketHandler) Delete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", "DELETE /api/aws/v1/s3/buckets/{bucket_name}"),
				attribute.String("network.peer.address", r.RemoteAddr),
				attribute.String("aws.s3.bucket_name", bucketName),
			),
		)
		defer span.End()

		if err := validateBucketName(bucketName); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-validation"),
				attribute.Bool("error", true),
			)
			handler.WriteError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}

		if err := h.svc.Delete(ctx, &resourceLocator{provider: "aws", path: "s3/" + bucketName}); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			span.SetAttributes(
				attribute.String("exception.slug", "err-handler-delete-bucket"),
				attribute.Bool("error", true),
			)
			handler.WriteError(w, http.StatusInternalServerError, "delete failed")
			return
		}
		span.SetStatus(codes.Ok, "")
		w.WriteHeader(http.StatusNoContent)
	}
}

var _ service.Generator = (*bucketGenerator)(nil)
