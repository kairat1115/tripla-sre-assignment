// Package render loads Terraform templates, renders resource data into HCL, and
// delegates persistence to provider-specific stores.
package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/metrics"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/resource"
	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/store"
)

const tracerName = "render"

// Renderer coordinates provider template sets with provider stores.
type Renderer struct {
	stores      map[string]store.Store
	templates   map[string]*template.Template
	templatesMu sync.RWMutex
	metrics     *metrics.Metrics
}

// New creates a renderer with provider-keyed stores and parsed template sets.
func New(stores map[string]store.Store, templates map[string]*template.Template, m *metrics.Metrics) *Renderer {
	return &Renderer{stores: stores, templates: templates, metrics: m}
}

// LoadTemplates parses every .tmpl file under dir into one template tree. Each
// template is registered under its slash-separated path relative to dir.
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

// TemplateSignature returns a content hash for every .tmpl file below dir. It
// changes when a template is added, removed, renamed, or edited.
func TemplateSignature(dir string) (string, error) {
	files := make([]string, 0)
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return err
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return "", fmt.Errorf("scan templates from %s: %w", dir, err)
	}
	sort.Strings(files)

	hash := sha256.New()
	for _, file := range files {
		rel, _ := filepath.Rel(dir, file)
		rel = filepath.ToSlash(rel)
		content, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read template %s: %w", file, err)
		}
		_, _ = hash.Write([]byte(rel))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ReloadTemplates reparses a provider's template directory and atomically swaps
// the provider to the new template tree. Existing templates remain active if
// parsing fails.
func (r *Renderer) ReloadTemplates(provider, dir string) error {
	tmpl, err := LoadTemplates(dir)
	if err != nil {
		return err
	}
	r.templatesMu.Lock()
	defer r.templatesMu.Unlock()
	r.templates[provider] = tmpl
	return nil
}

// TemplateCounts returns a provider-to-template-count snapshot used by health
// checks. The returned map is safe for callers to read without locking.
func (r *Renderer) TemplateCounts() map[string]int {
	r.templatesMu.RLock()
	defer r.templatesMu.RUnlock()

	counts := make(map[string]int, len(r.templates))
	for provider, tmpl := range r.templates {
		counts[provider] = len(tmpl.Templates())
	}
	return counts
}

// Generate renders the template selected by g and stores the resulting main.tf.
func (r *Renderer) Generate(ctx context.Context, g resource.Generator) (string, error) {
	start := time.Now()
	resourceName := resourceLabel(g.TemplateName())

	ctx, span := otel.Tracer(tracerName).Start(ctx, "render.generate",
		trace.WithAttributes(
			attribute.String("template.name", g.TemplateName()),
			attribute.String("provider", g.Provider()),
			attribute.String("store.path", g.StoragePath()),
		),
	)
	defer span.End()

	recordErr := func(slug string, err error) (string, error) {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("exception.slug", slug),
			attribute.Bool("error", true),
		)
		r.metrics.GenerationTotal.WithLabelValues(g.Provider(), resourceName, "error").Inc()
		r.metrics.GenerationDuration.WithLabelValues(g.Provider(), resourceName).Observe(time.Since(start).Seconds())
		return "", err
	}

	r.templatesMu.RLock()
	tmpl, ok := r.templates[g.Provider()]
	r.templatesMu.RUnlock()
	if !ok {
		return recordErr("err-render-no-template", fmt.Errorf("no templates registered for provider %s", g.Provider()))
	}
	st, ok := r.stores[g.Provider()]
	if !ok {
		return recordErr("err-render-no-store", fmt.Errorf("no store registered for provider %s", g.Provider()))
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, g.TemplateName(), g.TemplateData()); err != nil {
		return recordErr("err-render-template", fmt.Errorf("render template %s: %w", g.TemplateName(), err))
	}
	outputPath, err := st.Put(ctx, g.StoragePath(), buf.Bytes())
	if err != nil {
		return recordErr("err-render-store-put", fmt.Errorf("write store: %w", err))
	}
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(attribute.String("output.path", outputPath))
	r.metrics.GenerationTotal.WithLabelValues(g.Provider(), resourceName, "success").Inc()
	r.metrics.GenerationDuration.WithLabelValues(g.Provider(), resourceName).Observe(time.Since(start).Seconds())
	return outputPath, nil
}

// Read returns the rendered main.tf for the located resource.
func (r *Renderer) Read(ctx context.Context, l resource.Locator) ([]byte, error) {
	st, ok := r.stores[l.Provider()]
	if !ok {
		return nil, fmt.Errorf("no store registered for provider %s", l.Provider())
	}
	return st.Get(ctx, l.StoragePath())
}

// List returns resource names under the located resource prefix.
func (r *Renderer) List(ctx context.Context, l resource.Locator) ([]string, error) {
	st, ok := r.stores[l.Provider()]
	if !ok {
		return nil, fmt.Errorf("no store registered for provider %s", l.Provider())
	}
	return st.List(ctx, path.Dir(l.StoragePath()))
}

// Delete removes the rendered files for the located resource.
func (r *Renderer) Delete(ctx context.Context, l resource.Locator) error {
	st, ok := r.stores[l.Provider()]
	if !ok {
		return fmt.Errorf("no store registered for provider %s", l.Provider())
	}
	return st.Delete(ctx, l.StoragePath())
}

func resourceLabel(templateName string) string {
	name := strings.TrimSuffix(templateName, ".tf.tmpl")
	return strings.ReplaceAll(name, "/", "_")
}

var _ resource.Terraform = (*Renderer)(nil)
