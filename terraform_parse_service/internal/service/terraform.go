// Package service renders Terraform templates and delegates persistence to a
// provider-specific storage backend.
package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
)

const tracerName = "service.terraform"

// Locator identifies a generated resource in provider-specific storage.
type Locator interface {
	Provider() string
	StoragePath() string
}

// Generator provides everything required to render and store one Terraform
// configuration.
type Generator interface {
	Locator
	TemplateName() string
	TemplateData() any
}

// TerraformService renders templates and manages generated Terraform files.
type TerraformService struct {
	writers   map[string]storage.Writer
	templates map[string]*template.Template
	m         *metrics.Metrics
}

// NewTerraformService creates a renderer with provider-keyed storage writers
// and parsed template trees.
func NewTerraformService(writers map[string]storage.Writer, templates map[string]*template.Template, m *metrics.Metrics) *TerraformService {
	return &TerraformService{writers: writers, templates: templates, m: m}
}

// LoadTemplates parses every .tmpl file below dir into a single template tree.
// Template names are their slash-separated relative paths, for example
// s3/bucket.tf.tmpl.
func LoadTemplates(dir string) (*template.Template, error) {
	tmpl := template.New("").Funcs(sprig.TxtFuncMap())
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read template %s: %w", path, readErr)
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if _, parseErr := tmpl.New(rel).Parse(string(content)); parseErr != nil {
			return fmt.Errorf("parse template %s: %w", rel, parseErr)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load templates from %s: %w", dir, err)
	}
	return tmpl, nil
}

// Generate renders the template selected by g and writes the resulting main.tf
// through the provider's storage writer.
func (s *TerraformService) Generate(ctx context.Context, g Generator) (string, error) {
	start := time.Now()
	resource := resourceLabel(g.TemplateName())

	ctx, span := otel.Tracer(tracerName).Start(ctx, "service.generate",
		trace.WithAttributes(
			attribute.String("template.name", g.TemplateName()),
			attribute.String("provider", g.Provider()),
			attribute.String("storage.path", g.StoragePath()),
		),
	)
	defer span.End()

	recordErr := func(slug string, err error) (string, error) {
		span.SetAttributes(
			attribute.String("exception.slug", slug),
			attribute.Bool("error", true),
		)
		s.m.GenerationTotal.WithLabelValues(g.Provider(), resource, "error").Inc()
		s.m.GenerationDuration.WithLabelValues(g.Provider(), resource).Observe(time.Since(start).Seconds())
		return "", err
	}

	tmpl, ok := s.templates[g.Provider()]
	if !ok {
		err := fmt.Errorf("no templates registered for provider %s", g.Provider())
		span.SetStatus(codes.Error, err.Error())
		return recordErr("err-service-no-template", err)
	}
	writer, ok := s.writers[g.Provider()]
	if !ok {
		err := fmt.Errorf("no writer registered for provider %s", g.Provider())
		span.SetStatus(codes.Error, err.Error())
		return recordErr("err-service-no-writer", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, g.TemplateName(), g.TemplateData()); err != nil {
		err = fmt.Errorf("render template %s: %w", g.TemplateName(), err)
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return recordErr("err-service-render-template", err)
	}
	path, err := writer.Write(ctx, g.StoragePath(), buf.Bytes())
	if err != nil {
		err = fmt.Errorf("write storage: %w", err)
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return recordErr("err-service-storage-write", err)
	}
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(attribute.String("output.path", path))
	s.m.GenerationTotal.WithLabelValues(g.Provider(), resource, "success").Inc()
	s.m.GenerationDuration.WithLabelValues(g.Provider(), resource).Observe(time.Since(start).Seconds())
	return path, nil
}

// Read returns the generated Terraform file for the located resource.
func (s *TerraformService) Read(ctx context.Context, l Locator) ([]byte, error) {
	writer, ok := s.writers[l.Provider()]
	if !ok {
		return nil, fmt.Errorf("no writer registered for provider %s", l.Provider())
	}
	return writer.Read(ctx, l.StoragePath())
}

// List returns generated resource names below the located storage path.
func (s *TerraformService) List(ctx context.Context, l Locator) ([]string, error) {
	writer, ok := s.writers[l.Provider()]
	if !ok {
		return nil, fmt.Errorf("no writer registered for provider %s", l.Provider())
	}
	return writer.List(ctx, path.Dir(l.StoragePath()))
}

// Delete removes the generated Terraform files for the located resource.
func (s *TerraformService) Delete(ctx context.Context, l Locator) error {
	writer, ok := s.writers[l.Provider()]
	if !ok {
		return fmt.Errorf("no writer registered for provider %s", l.Provider())
	}
	return writer.Delete(ctx, l.StoragePath())
}

func resourceLabel(templateName string) string {
	name := strings.TrimSuffix(templateName, ".tf.tmpl")
	return strings.ReplaceAll(name, "/", "_")
}
