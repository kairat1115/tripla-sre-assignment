package s3

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
)

const tracerName = "handler.aws.s3"

type bucketProperties struct {
	Region     string `json:"aws-region"`
	ACL        string `json:"acl"`
	BucketName string `json:"bucket-name"`
}

func (p bucketProperties) Validate() error {
	switch {
	case p.Region == "":
		return fmt.Errorf("missing required property: aws-region")
	case p.ACL == "":
		return fmt.Errorf("missing required property: acl")
	case p.BucketName == "":
		return fmt.Errorf("missing required property: bucket-name")
	default:
		return nil
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
	ctx   context.Context
}

func (g *bucketGenerator) Provider() string     { return "aws" }
func (g *bucketGenerator) TemplateName() string { return "s3/bucket.tf.tmpl" }
func (g *bucketGenerator) StoragePath() string  { return "s3/" + g.props.BucketName }
func (g *bucketGenerator) Context() context.Context { return g.ctx }
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

type BucketHandler struct {
	svc    handler.Terraform
	logger *zap.Logger
}

func NewBucketHandler(svc handler.Terraform, logger *zap.Logger) *BucketHandler {
	return &BucketHandler{svc: svc, logger: logger}
}

func (h *BucketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx, span := otel.Tracer(tracerName).Start(r.Context(), "http.request",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("url.path", r.URL.Path),
			attribute.String("network.peer.address", r.RemoteAddr),
		),
	)
	defer span.End()

	base := []zap.Field{
		zap.String("http.request.method", r.Method),
		zap.String("url.path", r.URL.Path),
		zap.String("network.peer.address", r.RemoteAddr),
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(attribute.Int("http.response.status_code", http.StatusBadRequest))
		h.logger.Info("request body read failed", append(base,
			zap.Int("http.response.status_code", http.StatusBadRequest),
			zap.String("exception.message", err.Error()),
			zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
		)...)
		handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
		return
	}

	var req bucketRequest
	if err := json.Unmarshal(body, &req); err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(attribute.Int("http.response.status_code", http.StatusBadRequest))
		h.logger.Info("request body decode failed", append(base,
			zap.Int("http.response.status_code", http.StatusBadRequest),
			zap.String("exception.message", err.Error()),
			zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
		)...)
		handler.Respond(w, handler.Result{Code: http.StatusBadRequest, Msg: "invalid JSON", Err: err})
		return
	}

	p := req.Payload.Properties
	if err := p.Validate(); err != nil {
		msg := err.Error()
		span.SetStatus(codes.Error, msg)
		span.SetAttributes(
			attribute.Int("http.response.status_code", http.StatusUnprocessableEntity),
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
		handler.Respond(w, handler.Result{Code: http.StatusUnprocessableEntity, Msg: msg, Err: err})
		return
	}

	gen := &bucketGenerator{props: p, ctx: ctx}
	outputPath, err := h.svc.Generate(gen)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(
			attribute.Int("http.response.status_code", http.StatusInternalServerError),
			attribute.String("service.aws.s3.region", p.Region),
			attribute.String("service.aws.s3.acl", p.ACL),
			attribute.String("service.aws.s3.bucket_name", p.BucketName),
		)
		h.logger.Error("generation failed", append(base,
			zap.Int("http.response.status_code", http.StatusInternalServerError),
			zap.String("service.aws.s3.region", p.Region),
			zap.String("service.aws.s3.acl", p.ACL),
			zap.String("service.aws.s3.bucket_name", p.BucketName),
			zap.String("exception.message", err.Error()),
			zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
		)...)
		handler.Respond(w, handler.Result{Code: http.StatusInternalServerError, Msg: "generation failed", Err: err})
		return
	}

	span.SetStatus(codes.Ok, "")
	span.SetAttributes(
		attribute.Int("http.response.status_code", http.StatusCreated),
		attribute.String("service.aws.s3.region", p.Region),
		attribute.String("service.aws.s3.acl", p.ACL),
		attribute.String("service.aws.s3.bucket_name", p.BucketName),
		attribute.String("output.path", outputPath),
	)
	h.logger.Info("terraform config generated", append(base,
		zap.Int("http.response.status_code", http.StatusCreated),
		zap.String("service.aws.s3.region", p.Region),
		zap.String("service.aws.s3.acl", p.ACL),
		zap.String("service.aws.s3.bucket_name", p.BucketName),
		zap.String("output.path", outputPath),
		zap.Float64("http.server.request.duration", time.Since(start).Seconds()),
	)...)
	handler.Respond(w, handler.Result{Code: http.StatusCreated, Data: bucketResponse{OutputPath: outputPath}})
}

var _ service.Generator = (*bucketGenerator)(nil)
