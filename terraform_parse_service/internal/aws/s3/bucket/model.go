package bucket

import (
	"regexp"
	"strings"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9\-\.]*[a-z0-9]$`)

// Properties mirrors payload.properties for the S3 bucket API.
type Properties struct {
	// Region is the requested AWS region from payload.properties.aws-region.
	Region string `json:"aws-region"`
	// ACL is the requested canned ACL from payload.properties.acl.
	ACL string `json:"acl"`
	// BucketName is the requested bucket name from payload.properties.bucket-name.
	BucketName string `json:"bucket-name"`
}

// Validate checks required fields and S3 bucket naming rules.
func (p Properties) Validate() error {
	switch {
	case p.Region == "":
		return errMissingRegion
	case p.ACL == "":
		return errMissingACL
	default:
		return ValidateName(p.BucketName)
	}
}

// ValidateName checks the subset of S3 bucket naming rules required before
// using the name in storage paths and Terraform resource data.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errMissingBucketName
	case len(name) < 3 || len(name) > 63:
		return errInvalidBucketNameLength
	case !nameRE.MatchString(name):
		return errInvalidBucketNameChars
	case strings.Contains(name, ".."):
		return errConsecutiveDots
	default:
		return nil
	}
}

// Request is the JSON request shape accepted by create and update calls.
type Request struct {
	// Payload contains the requested resource properties.
	Payload struct {
		// Properties contains the S3 bucket settings.
		Properties Properties `json:"properties"`
	} `json:"payload"`
}

// ErrorResponse is the standard error schema returned by bucket endpoints.
type ErrorResponse struct {
	// Error describes why the request failed.
	Error string `json:"error"`
}

func errorResponse(err error) ErrorResponse {
	return ErrorResponse{Error: err.Error()}
}

// SaveResponse reports where generated Terraform output was stored.
type SaveResponse struct {
	// OutputPath is the backend-specific location of the rendered output.
	OutputPath string `json:"output_path"`
}

// ListResponse contains the configured S3 bucket names.
type ListResponse struct {
	// Buckets contains one entry per generated bucket configuration.
	Buckets []string `json:"buckets"`
}
