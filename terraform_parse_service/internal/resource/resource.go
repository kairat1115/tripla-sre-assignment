// Package resource defines the small contracts shared by resource-specific HTTP
// handlers and the Terraform renderer.
package resource

import (
	"context"
	"net/http"
)

// Locator identifies a generated resource in provider-specific storage.
type Locator interface {
	// Provider returns the cloud provider key, such as aws.
	Provider() string
	// StoragePath returns the provider-relative path for this resource.
	StoragePath() string
}

// Generator describes how to render and store one Terraform resource.
type Generator interface {
	Locator
	// TemplateName returns the provider-relative template name.
	TemplateName() string
	// TemplateData returns data passed to the selected Go template.
	TemplateData() any
}

// Terraform is the resource-handler-facing rendering and storage contract.
type Terraform interface {
	Generate(ctx context.Context, g Generator) (string, error)
	Read(ctx context.Context, l Locator) ([]byte, error)
	List(ctx context.Context, l Locator) ([]string, error)
	Delete(ctx context.Context, l Locator) error
}

// Router is the route registration surface resource handlers need.
type Router interface {
	Handle(method, pattern string, h http.Handler)
}

// HTTPResource registers all HTTP routes for one resource family.
type HTTPResource interface {
	RegisterRoutes(r Router)
}
