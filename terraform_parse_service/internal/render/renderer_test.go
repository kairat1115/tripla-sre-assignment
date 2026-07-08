package render

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/store"
)

type memoryStore struct {
	content []byte
}

func (s *memoryStore) Put(_ context.Context, _ string, content []byte) (string, error) {
	s.content = append([]byte(nil), content...)
	return "/memory/main.tf", nil
}

func (s *memoryStore) Get(context.Context, string) ([]byte, error) {
	return s.content, nil
}

func (s *memoryStore) List(context.Context, string) ([]string, error) {
	return nil, nil
}

func (s *memoryStore) Delete(context.Context, string) error {
	return nil
}

type testGenerator struct {
	value string
}

func (g testGenerator) Provider() string     { return "test" }
func (g testGenerator) StoragePath() string  { return "thing" }
func (g testGenerator) TemplateName() string { return "thing.tmpl" }
func (g testGenerator) TemplateData() any {
	return struct {
		Value string
	}{Value: g.value}
}

func TestRendererReloadTemplates(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "first: {{ .Value }}")

	tmpl, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("load templates: %v", err)
	}
	st := &memoryStore{}
	renderer := New(
		map[string]store.Store{"test": st},
		map[string]*template.Template{"test": tmpl},
		metrics.New(prometheus.NewRegistry()),
	)

	if _, err := renderer.Generate(context.Background(), testGenerator{value: "one"}); err != nil {
		t.Fatalf("generate first: %v", err)
	}
	if got := string(st.content); got != "first: one" {
		t.Fatalf("want first template output, got %q", got)
	}

	writeTemplate(t, dir, "second: {{ .Value }}")
	if err := renderer.ReloadTemplates("test", dir); err != nil {
		t.Fatalf("reload templates: %v", err)
	}
	if _, err := renderer.Generate(context.Background(), testGenerator{value: "two"}); err != nil {
		t.Fatalf("generate second: %v", err)
	}
	if got := string(st.content); got != "second: two" {
		t.Fatalf("want reloaded template output, got %q", got)
	}
}

func TestTemplateSignatureChangesOnContentUpdate(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "first")
	first, err := TemplateSignature(dir)
	if err != nil {
		t.Fatalf("first signature: %v", err)
	}

	writeTemplate(t, dir, "second")
	second, err := TemplateSignature(dir)
	if err != nil {
		t.Fatalf("second signature: %v", err)
	}
	if first == second {
		t.Fatal("expected signature to change after template content update")
	}
}

func writeTemplate(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "thing.tmpl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
}
