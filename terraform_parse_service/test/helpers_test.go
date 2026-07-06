package integration_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"text/template"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler"
	s3handler "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/handler/aws/s3"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/service"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
)

func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..")
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	writer, err := storage.NewFSWriter(t.TempDir())
	if err != nil {
		t.Fatalf("storage init: %v", err)
	}
	tmpl, err := service.LoadTemplates(filepath.Join(moduleRoot(), "templates", "aws"))
	if err != nil {
		t.Fatalf("template load: %v", err)
	}
	m := metrics.New(prometheus.NewRegistry())
	tfSvc := service.NewTerraformService(
		map[string]storage.Writer{"aws": writer},
		map[string]*template.Template{"aws": tmpl},
		m,
	)
	mux := http.NewServeMux()
	mux.Handle("POST /api/aws/v1/s3/buckets", s3handler.NewBucketHandler(tfSvc, zap.NewNop(), m))
	return httptest.NewServer(handler.Middleware(mux))
}
