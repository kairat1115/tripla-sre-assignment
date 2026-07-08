// Package s3 registers AWS S3 resource routers.
package s3

import (
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource/aws/s3/bucket"
)

// Router registers all AWS S3 resource routes.
type Router struct {
	svc resource.Terraform
	m   *metrics.Metrics
}

// NewRouter creates an S3 service router backed by the Terraform renderer.
func NewRouter(svc resource.Terraform, m ...*metrics.Metrics) *Router {
	rt := &Router{svc: svc}
	if len(m) > 0 {
		rt.m = m[0]
	}
	return rt
}

// RegisterRoutes registers S3 resource routers on r.
func (rt *Router) RegisterRoutes(r resource.Router) {
	bucket.NewRouter(rt.svc, rt.m).RegisterRoutes(r)
}

var _ resource.HTTPResource = (*Router)(nil)
