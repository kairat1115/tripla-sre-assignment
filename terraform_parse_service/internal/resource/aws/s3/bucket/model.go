package bucket

import (
	"fmt"
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
		return fmt.Errorf("missing required property: aws-region")
	case p.ACL == "":
		return fmt.Errorf("missing required property: acl")
	default:
		return ValidateName(p.BucketName)
	}
}

// ValidateName checks the subset of S3 bucket naming rules required before
// using the name in storage paths and Terraform resource data.
func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("missing required property: bucket-name")
	case len(name) < 3 || len(name) > 63:
		return fmt.Errorf("invalid bucket-name: must be 3-63 characters")
	case !nameRE.MatchString(name):
		return fmt.Errorf("invalid bucket-name: must contain only lowercase letters, digits, hyphens, and dots, and start/end with a letter or digit")
	case strings.Contains(name, ".."):
		return fmt.Errorf("invalid bucket-name: must not contain consecutive dots")
	default:
		return nil
	}
}

// Request is the JSON request shape accepted by create and update calls.
type Request struct {
	Payload struct {
		Properties Properties `json:"properties"`
	} `json:"payload"`
}

// Response reports where the generated Terraform file was written.
type Response struct {
	// OutputPath is the filesystem path to the rendered main.tf file.
	OutputPath string `json:"output_path"`
}
