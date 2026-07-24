package s3_test

import (
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/server"
)

func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	storageDir := t.TempDir()
	service, err := server.New(config.Config{
		ListenAddr: ":0",
		Metrics:    config.MetricsConfig{Addr: ":0"},
		Providers: map[string]config.ProviderConfig{
			"aws": {
				TemplatesDir:          filepath.Join(moduleRoot(), "templates", "aws"),
				TemplatesPollInterval: "1h",
				StorageDir:            storageDir,
			},
		},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("server init: %v", err)
	}
	return httptest.NewServer(service.Handler()), storageDir
}
