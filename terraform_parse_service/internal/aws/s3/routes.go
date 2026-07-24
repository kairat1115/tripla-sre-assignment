// Package s3 registers HTTP routes for supported Amazon S3 resources.
package s3

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/aws/s3/bucket"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/terraform"
)

// RegisterRoutes registers every supported S3 resource.
func RegisterRoutes(mux *http.ServeMux, terraformService *terraform.Service, log *zap.Logger) {
	bucket.NewHandler(terraformService, log).RegisterRoutes(mux)
}
