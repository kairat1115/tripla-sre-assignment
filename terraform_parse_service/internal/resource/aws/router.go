// Package aws registers AWS service routers.
package aws

import (
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource/aws/s3"
)

// Router registers all AWS service routes.
type Router struct {
	svc resource.Terraform
}

// NewRouter creates an AWS provider router backed by the Terraform renderer.
func NewRouter(svc resource.Terraform) *Router {
	return &Router{svc: svc}
}

// RegisterRoutes registers AWS service routers on r.
func (rt *Router) RegisterRoutes(r resource.Router) {
	s3.NewRouter(rt.svc).RegisterRoutes(r)
}

var _ resource.HTTPResource = (*Router)(nil)
