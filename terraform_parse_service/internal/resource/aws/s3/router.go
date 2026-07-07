// Package s3 registers AWS S3 resource routers.
package s3

import (
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource/aws/s3/bucket"
)

// Router registers all AWS S3 resource routes.
type Router struct {
	svc resource.Terraform
}

// NewRouter creates an S3 service router backed by the Terraform renderer.
func NewRouter(svc resource.Terraform) *Router {
	return &Router{svc: svc}
}

// RegisterRoutes registers S3 resource routers on r.
func (rt *Router) RegisterRoutes(r resource.Router) {
	bucket.NewRouter(rt.svc).RegisterRoutes(r)
}

var _ resource.HTTPResource = (*Router)(nil)
