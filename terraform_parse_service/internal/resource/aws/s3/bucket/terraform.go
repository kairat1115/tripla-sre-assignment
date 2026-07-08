package bucket

import (
	"strings"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
)

// Generator adapts S3 bucket properties to the generic Terraform renderer.
type Generator struct {
	// Props contains the validated S3 bucket request properties.
	Props Properties
}

// Provider returns the provider key used to select templates and storage.
func (g Generator) Provider() string { return "aws" }

// TemplateName returns the provider-relative template name for S3 buckets.
func (g Generator) TemplateName() string { return "s3/bucket.tf.tmpl" }

// StoragePath returns the provider-relative output path for the bucket.
func (g Generator) StoragePath() string { return "s3/" + g.Props.BucketName }

// TemplateData returns the typed data consumed by bucket.tf.tmpl.
func (g Generator) TemplateData() any {
	return TemplateData{
		Region:       g.Props.Region,
		ACL:          g.Props.ACL,
		BucketName:   g.Props.BucketName,
		ResourceName: strings.ReplaceAll(g.Props.BucketName, "-", "_"),
	}
}

// TemplateData is the typed input passed to the S3 bucket Terraform template.
type TemplateData struct {
	// Region is the AWS region written to the provider block.
	Region string
	// ACL is the canned ACL assigned to the bucket.
	ACL string
	// BucketName is the AWS S3 bucket name.
	BucketName string
	// ResourceName is the Terraform-safe resource label derived from BucketName.
	ResourceName string
}

// resourceLocator identifies either the S3 bucket collection or one bucket in
// provider storage.
type resourceLocator struct {
	BucketName string
}

// Provider returns the provider key used to select storage.
func (l resourceLocator) Provider() string { return "aws" }

// StoragePath returns the provider-relative collection or bucket output path.
func (l resourceLocator) StoragePath() string {
	if l.BucketName == "" {
		return "s3/"
	}
	return "s3/" + l.BucketName
}

var _ resource.Generator = (*Generator)(nil)
