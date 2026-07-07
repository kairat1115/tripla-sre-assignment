package s3

import (
	"fmt"
	"regexp"
	"strings"
)

var bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9\-\.]*[a-z0-9]$`)

// BucketProperties mirrors payload.properties for the S3 bucket API.
type BucketProperties struct {
	Region     string `json:"aws-region"`
	ACL        string `json:"acl"`
	BucketName string `json:"bucket-name"`
}

// Validate checks required fields and S3 bucket naming rules.
func (p BucketProperties) Validate() error {
	switch {
	case p.Region == "":
		return fmt.Errorf("missing required property: aws-region")
	case p.ACL == "":
		return fmt.Errorf("missing required property: acl")
	default:
		return ValidateBucketName(p.BucketName)
	}
}

// ValidateBucketName checks the subset of S3 bucket naming rules required
// before using the name in storage paths and Terraform resource data.
func ValidateBucketName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("missing required property: bucket-name")
	case len(name) < 3 || len(name) > 63:
		return fmt.Errorf("invalid bucket-name: must be 3-63 characters")
	case !bucketNameRE.MatchString(name):
		return fmt.Errorf("invalid bucket-name: must contain only lowercase letters, digits, hyphens, and dots, and start/end with a letter or digit")
	case strings.Contains(name, ".."):
		return fmt.Errorf("invalid bucket-name: must not contain consecutive dots")
	default:
		return nil
	}
}

// BucketRequest is the JSON request shape accepted by create and update calls.
type BucketRequest struct {
	Payload struct {
		Properties BucketProperties `json:"properties"`
	} `json:"payload"`
}

// BucketResponse reports where the generated Terraform file was written.
type BucketResponse struct {
	OutputPath string `json:"output_path"`
}
