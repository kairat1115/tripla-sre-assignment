package integration_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/config"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/server"
)

func TestIntegration_TemplateChangesAreLoadedWithoutRestart(t *testing.T) {
	templatesDir := t.TempDir()
	templatePath := filepath.Join(templatesDir, "s3", "bucket.tf.tmpl")
	if err := os.MkdirAll(filepath.Dir(templatePath), 0o755); err != nil {
		t.Fatalf("create template directory: %v", err)
	}
	if err := os.WriteFile(templatePath, []byte("before: {{ .BucketName }}"), 0o644); err != nil {
		t.Fatalf("write initial template: %v", err)
	}

	service, err := server.New(config.Config{
		ListenAddr: ":0",
		Metrics:    config.MetricsConfig{Addr: ":0"},
		Providers: map[string]config.ProviderConfig{
			"aws": {
				TemplatesDir:          templatesDir,
				TemplatesPollInterval: "10ms",
				StorageDir:            t.TempDir(),
			},
		},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() {
		runDone <- service.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("stop server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})

	api := httptest.NewServer(service.Handler())
	t.Cleanup(api.Close)

	// Give the polling worker time to record the initial template signature.
	time.Sleep(50 * time.Millisecond)
	if got := putAndGetReloadBucket(t, api.URL, "reload-test"); got != "before: reload-test" {
		t.Fatalf("want initial template output, got %q", got)
	}

	if err := os.WriteFile(templatePath, []byte("after: {{ .BucketName }}"), 0o644); err != nil {
		t.Fatalf("update template: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := putAndGetReloadBucket(t, api.URL, "reload-test"); got == "after: reload-test" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("updated template was not served before the deadline")
}

func putAndGetReloadBucket(t *testing.T, serverURL, name string) string {
	t.Helper()
	body := strings.NewReader(`{"payload":{"properties":{"aws-region":"eu-west-1","acl":"private"}}}`)
	request, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/api/aws/v1/s3/buckets/%s", serverURL, name), body)
	if err != nil {
		t.Fatalf("create update request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("update bucket: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update bucket: want 200, got %d", response.StatusCode)
	}

	response, err = http.Get(fmt.Sprintf("%s/api/aws/v1/s3/buckets/%s", serverURL, name))
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get bucket: want 200, got %d", response.StatusCode)
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read bucket: %v", err)
	}
	return string(content)
}
