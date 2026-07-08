// Package bucket implements the AWS S3 bucket API and Terraform mapping.
package bucket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/httpapi"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
)

// Router serves CRUD endpoints for generated AWS S3 bucket Terraform.
type Router struct {
	svc resource.Terraform
	m   *metrics.Metrics
}

// NewRouter creates an S3 bucket router backed by a Terraform renderer.
func NewRouter(svc resource.Terraform, m *metrics.Metrics) *Router {
	return &Router{svc: svc, m: m}
}

// RegisterRoutes registers S3 bucket collection and item routes on r.
func (rt *Router) RegisterRoutes(r resource.Router) {
	r.Handle(http.MethodGet, "/api/aws/v1/s3/buckets", rt.List())
	r.Handle(http.MethodPost, "/api/aws/v1/s3/buckets", rt.Create())
	r.Handle(http.MethodGet, "/api/aws/v1/s3/buckets/{bucket_name}", rt.Get())
	r.Handle(http.MethodPut, "/api/aws/v1/s3/buckets/{bucket_name}", rt.Update())
	r.Handle(http.MethodDelete, "/api/aws/v1/s3/buckets/{bucket_name}", rt.Delete())
}

// Create handles POST /api/aws/v1/s3/buckets.
func (rt *Router) Create() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rt.save(w, r, "create", "", http.StatusCreated)
	}
}

// List handles GET /api/aws/v1/s3/buckets.
func (rt *Router) List() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()
		status := "error"
		defer func() { rt.recordOperation("list", status, start) }()
		annotateBucketRequest(ctx, "list", "")

		buckets, err := rt.svc.List(ctx, resourceLocator{})
		if err != nil {
			httpapi.RecordError(ctx, "err-handler-list", err)
			httpapi.Error(w, http.StatusInternalServerError, "list failed")
			return
		}
		status = "success"
		httpapi.AddSpanAttributes(ctx, attribute.Int("aws.s3.bucket_count", len(buckets)))
		httpapi.JSON(w, http.StatusOK, map[string][]string{"buckets": buckets})
	}
}

// Get handles GET /api/aws/v1/s3/buckets/{bucket_name}.
func (rt *Router) Get() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()
		status := "error"
		defer func() { rt.recordOperation("get", status, start) }()
		bucketName := r.PathValue("bucket_name")
		annotateBucketRequest(ctx, "get", bucketName)
		if err := ValidateName(bucketName); err != nil {
			httpapi.RecordError(ctx, "err-handler-validation", err)
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		content, err := rt.svc.Read(ctx, resourceLocator{BucketName: bucketName})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				httpapi.RecordError(ctx, "err-handler-bucket-not-found", err)
				httpapi.Error(w, http.StatusNotFound, "bucket not found")
				return
			}
			httpapi.RecordError(ctx, "err-handler-read", err)
			httpapi.Error(w, http.StatusInternalServerError, "read failed")
			return
		}
		status = "success"
		httpapi.AddSpanAttributes(ctx, attribute.Int("terraform.output.bytes", len(content)))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}
}

// Update handles PUT /api/aws/v1/s3/buckets/{bucket_name}.
func (rt *Router) Update() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rt.save(w, r, "update", r.PathValue("bucket_name"), http.StatusOK)
	}
}

// save handles create and update requests that render bucket Terraform from a
// JSON payload.
func (rt *Router) save(w http.ResponseWriter, r *http.Request, operation, pathBucketName string, status int) {
	ctx := r.Context()
	start := time.Now()
	operationStatus := "error"
	defer func() { rt.recordOperation(operation, operationStatus, start) }()
	annotateBucketRequest(ctx, operation, pathBucketName)

	var req Request
	parseStart := time.Now()
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.AddSpanAttributes(ctx, attribute.Float64("payload_parse.duration", time.Since(parseStart).Seconds()))
		httpapi.RecordError(ctx, "err-handler-json-decode", err)
		httpapi.Error(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	httpapi.AddSpanAttributes(ctx, attribute.Float64("payload_parse.duration", time.Since(parseStart).Seconds()))

	props := req.Payload.Properties
	if pathBucketName != "" {
		if props.BucketName != "" && props.BucketName != pathBucketName {
			err := fmt.Errorf("bucket-name in body must match path parameter")
			httpapi.RecordError(ctx, "err-handler-bucket-name-mismatch", err)
			httpapi.Error(w, http.StatusUnprocessableEntity, "bucket-name in body must match path parameter")
			return
		}
		props.BucketName = pathBucketName
	}
	annotateBucketProperties(ctx, props)
	if err := props.Validate(); err != nil {
		httpapi.RecordError(ctx, "err-handler-validation", err)
		httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	httpapi.AddSpanEvent(ctx, "terraform.generate.start")
	outputPath, err := rt.svc.Generate(ctx, Generator{Props: props})
	if err != nil {
		httpapi.RecordError(ctx, "err-handler-generate", err)
		httpapi.Error(w, http.StatusInternalServerError, "generation failed")
		return
	}
	outputPathAttr := attribute.String("terraform.provider.storage.output.path", outputPath)
	httpapi.AddSpanAttributes(ctx, outputPathAttr)
	httpapi.AddLogFields(ctx, zap.String("terraform.provider.storage.output.path", outputPath))
	httpapi.AddSpanEvent(ctx, "terraform.generate.success", outputPathAttr)
	operationStatus = "success"
	httpapi.JSON(w, status, Response{OutputPath: outputPath})
}

// Delete handles DELETE /api/aws/v1/s3/buckets/{bucket_name}.
func (rt *Router) Delete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()
		status := "error"
		defer func() { rt.recordOperation("delete", status, start) }()
		bucketName := r.PathValue("bucket_name")
		annotateBucketRequest(ctx, "delete", bucketName)
		if err := ValidateName(bucketName); err != nil {
			httpapi.RecordError(ctx, "err-handler-validation", err)
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := rt.svc.Delete(ctx, resourceLocator{BucketName: bucketName}); err != nil {
			httpapi.RecordError(ctx, "err-handler-delete", err)
			httpapi.Error(w, http.StatusInternalServerError, "delete failed")
			return
		}
		status = "success"
		w.WriteHeader(http.StatusNoContent)
	}
}

var _ resource.HTTPResource = (*Router)(nil)

// recordOperation records the resource-level API metric for one bucket request.
func (rt *Router) recordOperation(operation, status string, start time.Time) {
	rt.m.ObserveResourceOperation("aws", "s3", "aws_s3_bucket", operation, status, time.Since(start))
}

// annotateBucketRequest adds stable request dimensions to the active span and
// final request log.
func annotateBucketRequest(ctx context.Context, operation, bucketName string) {
	attrs := []attribute.KeyValue{
		attribute.String("terraform.provider.name", "aws"),
		attribute.String("terraform.provider.service", "s3"),
		attribute.String("terraform.provider.resource.name", "aws_s3_bucket"),
		attribute.String("terraform.provider.resource.operation", operation),
	}
	logs := []zap.Field{
		zap.String("terraform.provider.name", "aws"),
		zap.String("terraform.provider.service", "s3"),
		zap.String("terraform.provider.resource.name", "aws_s3_bucket"),
		zap.String("terraform.provider.resource.operation", operation),
	}

	if bucketName != "" {
		storagePath := "s3/" + bucketName
		attrs = append(attrs,
			attribute.String("terraform.provider.aws.s3.bucket.bucket_name", bucketName),
			attribute.String("http.route.param.bucket_name", bucketName),
			attribute.String("terraform.provider.storage.path", storagePath),
		)
		logs = append(logs,
			zap.String("terraform.provider.aws.s3.bucket.bucket_name", bucketName),
			zap.String("terraform.provider.storage.path", storagePath),
		)
	}

	httpapi.AddSpanAttributes(ctx, attrs...)
	httpapi.AddLogFields(ctx, logs...)
	httpapi.AddSpanEvent(ctx, "resource.request.start", attrs...)
}

// annotateBucketProperties adds payload-derived bucket dimensions to the active
// span and final request log.
func annotateBucketProperties(ctx context.Context, props Properties) {
	attrs := []attribute.KeyValue{
		attribute.String("terraform.provider.aws.s3.bucket.region", props.Region),
		attribute.String("terraform.provider.aws.s3.bucket.acl", props.ACL),
		attribute.String("terraform.provider.aws.s3.bucket.bucket_name", props.BucketName),
	}
	logs := []zap.Field{
		zap.String("terraform.provider.aws.s3.bucket.region", props.Region),
		zap.String("terraform.provider.aws.s3.bucket.acl", props.ACL),
		zap.String("terraform.provider.aws.s3.bucket.bucket_name", props.BucketName),
	}

	if props.BucketName != "" {
		storagePath := "s3/" + props.BucketName
		attrs = append(attrs, attribute.String("terraform.provider.storage.path", storagePath))
		logs = append(logs, zap.String("terraform.provider.storage.path", storagePath))
	}

	httpapi.AddSpanAttributes(ctx, attrs...)
	httpapi.AddLogFields(ctx, logs...)
}
