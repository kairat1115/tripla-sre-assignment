// Package bucket implements the AWS S3 bucket API and Terraform mapping.
package bucket

import (
	"errors"
	"net/http"
	"os"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/httpapi"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
)

// Router serves CRUD endpoints for generated AWS S3 bucket Terraform.
type Router struct {
	svc resource.Terraform
}

// NewRouter creates an S3 bucket router backed by a Terraform renderer.
func NewRouter(svc resource.Terraform) *Router {
	return &Router{svc: svc}
}

// RegisterRoutes registers all S3 bucket routes on r.
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
		var req Request
		if err := httpapi.DecodeJSON(r, &req); err != nil {
			httpapi.Error(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		props := req.Payload.Properties
		if err := props.Validate(); err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		outputPath, err := rt.svc.Generate(r.Context(), Generator{Props: props})
		if err != nil {
			httpapi.Error(w, http.StatusInternalServerError, "generation failed")
			return
		}
		httpapi.JSON(w, http.StatusCreated, Response{OutputPath: outputPath})
	}
}

// List handles GET /api/aws/v1/s3/buckets.
func (rt *Router) List() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		buckets, err := rt.svc.List(r.Context(), resourceLocator{provider: "aws", path: "s3/"})
		if err != nil {
			httpapi.Error(w, http.StatusInternalServerError, "list failed")
			return
		}
		httpapi.JSON(w, http.StatusOK, map[string][]string{"buckets": buckets})
	}
}

// Get handles GET /api/aws/v1/s3/buckets/{bucket_name}.
func (rt *Router) Get() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		if err := ValidateName(bucketName); err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		content, err := rt.svc.Read(r.Context(), resourceLocator{provider: "aws", path: "s3/" + bucketName})
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
func (rt *Router) Update() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		var req Request
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
		outputPath, err := rt.svc.Generate(r.Context(), Generator{Props: props})
		if err != nil {
			httpapi.Error(w, http.StatusInternalServerError, "generation failed")
			return
		}
		httpapi.JSON(w, http.StatusOK, Response{OutputPath: outputPath})
	}
}

// Delete handles DELETE /api/aws/v1/s3/buckets/{bucket_name}.
func (rt *Router) Delete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucketName := r.PathValue("bucket_name")
		if err := ValidateName(bucketName); err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := rt.svc.Delete(r.Context(), resourceLocator{provider: "aws", path: "s3/" + bucketName}); err != nil {
			httpapi.Error(w, http.StatusInternalServerError, "delete failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

var _ resource.HTTPResource = (*Router)(nil)
