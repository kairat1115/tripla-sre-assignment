// Package aws registers HTTP routes for AWS services.
package aws

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/aws/s3"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/terraform"
)

// RegisterRoutes registers every AWS service exposed by the application.
func RegisterRoutes(mux *http.ServeMux, terraformService *terraform.Service, log *zap.Logger) {
	s3.RegisterRoutes(mux, terraformService, log)
}
