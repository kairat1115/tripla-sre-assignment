// Package s3 implements the AWS S3 bucket API and Terraform mapping.
package s3

import (
	"errors"
	"net/http"
	"os"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/httpapi"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
)

// BucketHandler serves CRUD endpoints for generated AWS S3 bucket Terraform.
type BucketHandler struct {
	svc resource.Terraform
}

// NewBucketHandler creates an S3 bucket handler backed by a Terraform renderer.
func NewBucketHandler(svc resource.Terraform) *BucketHandler {
	return &BucketHandler{svc: svc}
}

// RegisterRoutes registers all S3 bucket routes on r.
func (h *BucketHandler) RegisterRoutes(r resource.Router) {
	r.Handle(http.MethodGet, "/api/aws/v1/s3/buckets", h.List())
	r.Handle(http.MethodPost, "/api/aws/v1/s3/buckets", h.Create())
	r.Handle(http.MethodGet, "/api/aws/v1/s3/buckets/{bucket_name}", h.Get())
	r.Handle(http.MethodPut, "/api/aws/v1/s3/buckets/{bucket_name}", h.Update())
	r.Handle(http.MethodDelete, "/api/aws/v1/s3/buckets/{bucket_name}", h.Delete())
}

// Create handles POST /api/aws/v1/s3/buckets.
func (h *BucketHandler) Create() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req BucketRequest
		if err := httpapi.DecodeJSON(r, &req); err != nil {
			httpapi.Error(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		props := req.Payload.Properties
		if err := props.Validate(); err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		outputPath, err := h.svc.Generate(r.Context(), BucketGenerator{Props: props})
		if err != nil {
			httpapi.Error(w, http.StatusInternalServerError, "generation failed")
			return
		}
		httpapi.JSON(w, http.StatusCreated, BucketResponse{OutputPath: outputPath})
	}
}

// List handles GET /api/aws/v1/s3/buckets.
func (h *BucketHandler) List() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		buckets, err := h.svc.List(r.Context(), resourceLocator{provider: "aws", path: "s3/"})
		if err != nil {
			httpapi.Error(w, http.StatusInternalServerError, "list failed")
			return
		}
		httpapi.JSON(w, http.StatusOK, map[string][]string{"buckets": buckets})
	}
}

// Get handles GET /api/aws/v1/s3/buckets/{bucket_name}.
func (h *BucketHandler) Get() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		if err := ValidateBucketName(bucketName); err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		content, err := h.svc.Read(r.Context(), resourceLocator{provider: "aws", path: "s3/" + bucketName})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				httpapi.Error(w, http.StatusNotFound, "bucket not found")
				return
			}
			httpapi.Error(w, http.StatusInternalServerError, "read failed")
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}
}

// Update handles PUT /api/aws/v1/s3/buckets/{bucket_name}.
func (h *BucketHandler) Update() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		var req BucketRequest
		if err := httpapi.DecodeJSON(r, &req); err != nil {
			httpapi.Error(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		props := req.Payload.Properties
		if props.BucketName != "" && props.BucketName != bucketName {
			httpapi.Error(w, http.StatusUnprocessableEntity, "bucket-name in body must match path parameter")
			return
		}
		props.BucketName = bucketName
		if err := props.Validate(); err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		outputPath, err := h.svc.Generate(r.Context(), BucketGenerator{Props: props})
		if err != nil {
			httpapi.Error(w, http.StatusInternalServerError, "generation failed")
			return
		}
		httpapi.JSON(w, http.StatusOK, BucketResponse{OutputPath: outputPath})
	}
}

// Delete handles DELETE /api/aws/v1/s3/buckets/{bucket_name}.
func (h *BucketHandler) Delete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		if err := ValidateBucketName(bucketName); err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := h.svc.Delete(r.Context(), resourceLocator{provider: "aws", path: "s3/" + bucketName}); err != nil {
			httpapi.Error(w, http.StatusInternalServerError, "delete failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

var _ resource.HTTPResource = (*BucketHandler)(nil)
