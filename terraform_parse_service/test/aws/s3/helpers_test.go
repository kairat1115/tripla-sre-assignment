package s3_test

import (
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"text/template"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/httpapi"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/render"
	awsresource "github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource/aws"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/store"
)

func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	storageDir := t.TempDir()
	st, err := store.NewFSStore(storageDir)
	if err != nil {
		t.Fatalf("store init: %v", err)
	}
	tmpl, err := render.LoadTemplates(filepath.Join(moduleRoot(), "templates", "aws"))
	if err != nil {
		t.Fatalf("template load: %v", err)
	}
	m := metrics.New(prometheus.NewRegistry())
	tfSvc := render.New(
		map[string]store.Store{"aws": st},
		map[string]*template.Template{"aws": tmpl},
		m,
	)
	router := httpapi.NewRouter(m, zap.NewNop())
	awsresource.NewRouter(tfSvc).RegisterRoutes(router)
	return httptest.NewServer(router.Handler()), storageDir
}
