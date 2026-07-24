// Package bucket implements the HTTP API for Terraform-backed S3 buckets.
package bucket

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/terraform"
)

const (
	collectionPath = "/api/aws/v1/s3/buckets"
	itemPath       = "/api/aws/v1/s3/buckets/{bucket_name}"
	maxRequestBody = 1 << 20
)

// Handler serves the AWS S3 bucket API.
type Handler struct {
	terraform *terraform.Service
	log       *zap.Logger
}

// NewHandler creates an S3 bucket handler.
func NewHandler(terraformService *terraform.Service, log *zap.Logger) *Handler {
	return &Handler{terraform: terraformService, log: log}
}

// RegisterRoutes registers the S3 bucket routes on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+collectionPath, h.list)
	mux.HandleFunc("POST "+collectionPath, h.create)
	mux.HandleFunc("GET "+itemPath, h.get)
	mux.HandleFunc("PUT "+itemPath, h.update)
	mux.HandleFunc("DELETE "+itemPath, h.delete)

	// Methodless patterns are the explicit fallback for otherwise valid paths.
	mux.HandleFunc(collectionPath, methodNotAllowed)
	mux.HandleFunc(itemPath, methodNotAllowed)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	h.save(w, r, "", http.StatusCreated)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	span := annotateBucketSpan(r, "list", "")
	buckets, err := h.terraform.ListOutputs(r.Context(), "aws", "s3")
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		writeJSON(w, http.StatusInternalServerError, errorResponse(errListFailed))
		return
	}

	span.SetAttributes(attribute.Int("terraform.provider.aws.s3.bucket.count", len(buckets)))
	writeJSON(w, http.StatusOK, ListResponse{Buckets: buckets})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	bucketName := r.PathValue("bucket_name")
	span := annotateBucketSpan(r, "get", bucketName)

	if err := ValidateName(bucketName); err != nil {
		span.RecordError(err)
		span.AddEvent("request.rejected",
			trace.WithAttributes(attribute.String("request.rejection.reason", "validation")),
		)
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse(err))
		return
	}

	content, err := h.terraform.ReadOutput(r.Context(), "aws", bucketStoragePath(bucketName))
	if errors.Is(err, os.ErrNotExist) {
		span.AddEvent("terraform.resource.not_found")
		writeJSON(w, http.StatusNotFound, errorResponse(errBucketNotFound))
		return
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		writeJSON(w, http.StatusInternalServerError, errorResponse(errReadFailed))
		return
	}

	span.SetAttributes(attribute.Int("terraform.provider.storage.output.bytes", len(content)))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	h.save(w, r, r.PathValue("bucket_name"), http.StatusOK)
}

func (h *Handler) save(w http.ResponseWriter, r *http.Request, pathBucketName string, successStatus int) {
	operation := "create"
	successMessage := "bucket created"
	if pathBucketName != "" {
		operation = "update"
		successMessage = "bucket updated"
	}
	span := annotateBucketSpan(r, operation, pathBucketName)

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	parseStart := time.Now()
	var request Request
	decodeErr := decoder.Decode(&request)
	if decodeErr == nil {
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			decodeErr = fmt.Errorf("request body must contain one JSON value")
		}
	}
	span.SetAttributes(attribute.Float64("payload_parse.duration", time.Since(parseStart).Seconds()))
	if decodeErr != nil {
		span.RecordError(decodeErr)
		span.AddEvent("request.rejected",
			trace.WithAttributes(attribute.String("request.rejection.reason", "invalid_json")),
		)
		writeJSON(w, http.StatusBadRequest, errorResponse(errInvalidJSON))
		return
	}

	properties := request.Payload.Properties
	if pathBucketName != "" {
		if properties.BucketName != "" && properties.BucketName != pathBucketName {
			err := errBucketNameMismatch
			span.RecordError(err)
			span.AddEvent("request.rejected",
				trace.WithAttributes(attribute.String("request.rejection.reason", "bucket_name_mismatch")),
			)
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse(err))
			return
		}
		properties.BucketName = pathBucketName
	}

	annotateBucketProperties(span, properties)
	if err := properties.Validate(); err != nil {
		span.RecordError(err)
		span.AddEvent("request.rejected",
			trace.WithAttributes(attribute.String("request.rejection.reason", "validation")),
		)
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse(err))
		return
	}

	span.AddEvent("terraform.generate.start")
	outputPath, err := h.terraform.Generate(
		r.Context(),
		"aws",
		"s3/bucket.tf.tmpl",
		bucketStoragePath(properties.BucketName),
		templateData{
			Region:       properties.Region,
			ACL:          properties.ACL,
			BucketName:   properties.BucketName,
			ResourceName: strings.ReplaceAll(properties.BucketName, "-", "_"),
		},
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		writeJSON(w, http.StatusInternalServerError, errorResponse(errGenerationFailed))
		return
	}

	outputPathAttribute := attribute.String("terraform.provider.storage.output.path", outputPath)
	span.SetAttributes(outputPathAttribute)
	span.AddEvent("terraform.generate.success", trace.WithAttributes(outputPathAttribute))
	h.log.Info(successMessage,
		zap.String("audit.action", operation),
		zap.String("audit.outcome", "success"),
		zap.String("terraform.provider.name", "aws"),
		zap.String("terraform.provider.service", "s3"),
		zap.String("terraform.provider.resource.name", "aws_s3_bucket"),
		zap.String("terraform.provider.aws.s3.bucket.region", properties.Region),
		zap.String("terraform.provider.aws.s3.bucket.acl", properties.ACL),
		zap.String("terraform.provider.aws.s3.bucket.bucket_name", properties.BucketName),
		zap.String("terraform.provider.storage.output.path", outputPath),
	)
	writeJSON(w, successStatus, SaveResponse{OutputPath: outputPath})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	bucketName := r.PathValue("bucket_name")
	span := annotateBucketSpan(r, "delete", bucketName)

	if err := ValidateName(bucketName); err != nil {
		span.RecordError(err)
		span.AddEvent("request.rejected",
			trace.WithAttributes(attribute.String("request.rejection.reason", "validation")),
		)
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse(err))
		return
	}
	if err := h.terraform.DeleteOutput(r.Context(), "aws", bucketStoragePath(bucketName)); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		writeJSON(w, http.StatusInternalServerError, errorResponse(errDeleteFailed))
		return
	}

	h.log.Info("bucket deleted",
		zap.String("audit.action", "delete"),
		zap.String("audit.outcome", "success"),
		zap.String("terraform.provider.name", "aws"),
		zap.String("terraform.provider.service", "s3"),
		zap.String("terraform.provider.resource.name", "aws_s3_bucket"),
		zap.String("terraform.provider.aws.s3.bucket.bucket_name", bucketName),
		zap.String("terraform.provider.storage.path", bucketStoragePath(bucketName)),
	)
	w.WriteHeader(http.StatusNoContent)
}

func methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusMethodNotAllowed, errorResponse(errMethodNotAllowed))
}

func bucketStoragePath(bucketName string) string {
	return "s3/" + bucketName
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func annotateBucketSpan(r *http.Request, operation, bucketName string) trace.Span {
	span := trace.SpanFromContext(r.Context())
	attributes := []attribute.KeyValue{
		attribute.String("terraform.provider.name", "aws"),
		attribute.String("terraform.provider.service", "s3"),
		attribute.String("terraform.provider.resource.name", "aws_s3_bucket"),
		attribute.String("terraform.provider.resource.operation", operation),
	}
	if bucketName != "" {
		attributes = append(attributes,
			attribute.String("terraform.provider.aws.s3.bucket.bucket_name", bucketName),
			attribute.String("http.route.param.bucket_name", bucketName),
			attribute.String("terraform.provider.storage.path", bucketStoragePath(bucketName)),
		)
	}
	span.SetAttributes(attributes...)
	return span
}

func annotateBucketProperties(span trace.Span, properties Properties) {
	attributes := []attribute.KeyValue{
		attribute.String("terraform.provider.aws.s3.bucket.region", properties.Region),
		attribute.String("terraform.provider.aws.s3.bucket.acl", properties.ACL),
		attribute.String("terraform.provider.aws.s3.bucket.bucket_name", properties.BucketName),
	}
	if properties.BucketName != "" {
		attributes = append(
			attributes,
			attribute.String("terraform.provider.storage.path", bucketStoragePath(properties.BucketName)),
		)
	}
	span.SetAttributes(attributes...)
}

type templateData struct {
	Region       string
	ACL          string
	BucketName   string
	ResourceName string
}
