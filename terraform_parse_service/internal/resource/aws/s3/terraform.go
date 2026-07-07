package s3

import (
	"strings"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
)

// BucketGenerator adapts S3 bucket properties to the generic Terraform renderer.
type BucketGenerator struct {
	Props BucketProperties
}

// Provider returns the provider key used to select templates and storage.
func (g BucketGenerator) Provider() string { return "aws" }

// TemplateName returns the provider-relative template name for S3 buckets.
func (g BucketGenerator) TemplateName() string { return "s3/bucket.tf.tmpl" }

// StoragePath returns the provider-relative output path for the bucket.
func (g BucketGenerator) StoragePath() string { return "s3/" + g.Props.BucketName }

// TemplateData returns the typed data consumed by bucket.tf.tmpl.
func (g BucketGenerator) TemplateData() any {
	return BucketTemplateData{
		Region:       g.Props.Region,
		ACL:          g.Props.ACL,
		BucketName:   g.Props.BucketName,
		ResourceName: strings.ReplaceAll(g.Props.BucketName, "-", "_"),
	}
}

// BucketTemplateData is the typed input passed to the S3 bucket Terraform
// template.
type BucketTemplateData struct {
	Region       string
	ACL          string
	BucketName   string
	ResourceName string
}

type resourceLocator struct {
	provider string
	path     string
}

func (l resourceLocator) Provider() string    { return l.provider }
func (l resourceLocator) StoragePath() string { return l.path }

var _ resource.Generator = (*BucketGenerator)(nil)
