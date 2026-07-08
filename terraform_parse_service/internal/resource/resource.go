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
	// Generate renders and stores Terraform for g, returning the output path.
	Generate(ctx context.Context, g Generator) (string, error)
	// Read returns generated Terraform content for l.
	Read(ctx context.Context, l Locator) ([]byte, error)
	// List returns generated resource names below l's resource collection.
	List(ctx context.Context, l Locator) ([]string, error)
	// Delete removes generated Terraform content for l.
	Delete(ctx context.Context, l Locator) error
}

// Router is the route registration surface resource handlers need.
type Router interface {
	// Handle registers h for method and path pattern.
	Handle(method, pattern string, h http.Handler)
}

// HTTPResource registers all HTTP routes for one resource family.
type HTTPResource interface {
	// RegisterRoutes registers this resource family's routes on r.
	RegisterRoutes(r Router)
}
