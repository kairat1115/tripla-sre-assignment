// Package terraform renders provider templates and persists generated files.
package terraform

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
)

type providerRuntime struct {
	templatesDir string
	outputs      storage.Store

	mu        sync.RWMutex
	templates *template.Template
}

// Service owns the loaded templates and generated files for each provider.
type Service struct {
	providers map[string]*providerRuntime
	metrics   *metrics.Metrics
}

// New creates an empty Terraform service.
func New(m *metrics.Metrics) *Service {
	return &Service{
		providers: make(map[string]*providerRuntime),
		metrics:   m,
	}
}

// AddProvider loads a provider's templates and associates its output store.
// Providers must be added before the service starts handling requests.
func (s *Service) AddProvider(name, templatesDir string, outputs storage.Store) error {
	if _, exists := s.providers[name]; exists {
		return fmt.Errorf("provider %q is already configured", name)
	}
	if outputs == nil {
		return fmt.Errorf("provider %q storage is required", name)
	}

	templates, count, err := loadTemplates(templatesDir)
	if err != nil {
		return fmt.Errorf("load %s templates: %w", name, err)
	}

	s.providers[name] = &providerRuntime{
		templatesDir: templatesDir,
		outputs:      outputs,
		templates:    templates,
	}
	s.metrics.SetTemplatesLoaded(name, count)
	return nil
}

// TemplateSignature returns a hash of the provider's template files.
func (s *Service) TemplateSignature(name string) (string, error) {
	provider, err := s.getProvider(name)
	if err != nil {
		return "", err
	}
	return templateSignature(provider.templatesDir)
}

// Reload reparses and atomically replaces a provider's active templates. It
// returns the number of templates in the new active set.
func (s *Service) Reload(name string) (int, error) {
	provider, err := s.getProvider(name)
	if err != nil {
		return 0, err
	}
	templates, count, err := loadTemplates(provider.templatesDir)
	if err != nil {
		return 0, err
	}

	provider.mu.Lock()
	provider.templates = templates
	provider.mu.Unlock()

	s.metrics.SetTemplatesLoaded(name, count)
	return count, nil
}

// Generate renders one template and persists it below storagePath.
func (s *Service) Generate(ctx context.Context, providerName, templateName, storagePath string, data any) (string, error) {
	start := time.Now()
	resourceName := metricResourceName(templateName)
	status := "error"
	defer func() {
		s.metrics.ObserveGeneration(providerName, resourceName, status, time.Since(start))
	}()

	provider, err := s.getProvider(providerName)
	if err != nil {
		return "", err
	}
	provider.mu.RLock()
	templates := provider.templates
	provider.mu.RUnlock()

	var output bytes.Buffer
	if err := templates.ExecuteTemplate(&output, templateName, data); err != nil {
		return "", fmt.Errorf("render template %s: %w", templateName, err)
	}
	outputPath, err := provider.outputs.Put(ctx, storagePath, output.Bytes())
	if err != nil {
		return "", fmt.Errorf("store rendered template: %w", err)
	}

	status = "success"
	s.metrics.ObserveRenderedBytes(providerName, resourceName, output.Len())
	return outputPath, nil
}

// ReadOutput returns the generated main.tf below storagePath.
func (s *Service) ReadOutput(ctx context.Context, providerName, storagePath string) ([]byte, error) {
	provider, err := s.getProvider(providerName)
	if err != nil {
		return nil, err
	}
	return provider.outputs.Get(ctx, storagePath)
}

// ListOutputs returns generated directory names below storagePath.
func (s *Service) ListOutputs(ctx context.Context, providerName, storagePath string) ([]string, error) {
	provider, err := s.getProvider(providerName)
	if err != nil {
		return nil, err
	}
	return provider.outputs.List(ctx, storagePath)
}

// DeleteOutput removes generated files below storagePath.
func (s *Service) DeleteOutput(ctx context.Context, providerName, storagePath string) error {
	provider, err := s.getProvider(providerName)
	if err != nil {
		return err
	}
	return provider.outputs.Delete(ctx, storagePath)
}

func (s *Service) getProvider(name string) (*providerRuntime, error) {
	provider, ok := s.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", name)
	}
	return provider, nil
}

func metricResourceName(templateName string) string {
	name := strings.TrimSuffix(templateName, ".tf.tmpl")
	return strings.ReplaceAll(name, "/", "_")
}
