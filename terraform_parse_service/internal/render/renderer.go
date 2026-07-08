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
	"reflect"
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
	for provider, tmpl := range templates {
		m.SetTemplatesLoaded(provider, len(tmpl.Templates()))
	}
	return &Renderer{stores: stores, templates: templates, metrics: m}
}

// LoadTemplates parses every .tmpl file under dir into one Sprig-enabled
// template tree. Each template is registered under its slash-separated path
// relative to dir.
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

// TemplateSignature returns a hash of every .tmpl file's relative path and
// content below dir. It changes when a template is added, removed, renamed, or
// edited.
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
	r.metrics.SetTemplatesLoaded(provider, len(tmpl.Templates()))
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
	data := g.TemplateData()
	dataType := "<nil>"
	if data != nil {
		dataType = reflect.TypeOf(data).String()
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "render.generate",
		trace.WithAttributes(
			attribute.String("template.name", g.TemplateName()),
			attribute.String("template.resource", resourceName),
			attribute.String("template.data.type", dataType),
			attribute.String("terraform.provider.name", g.Provider()),
			attribute.String("store.path", g.StoragePath()),
			attribute.String("terraform.provider.storage.path", g.StoragePath()),
		),
	)
	defer span.End()

	recordErr := func(slug string, err error) (string, error) {
		recordSpanError(span, slug, err)
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
	span.AddEvent("template.render.start",
		trace.WithAttributes(
			attribute.String("template.name", g.TemplateName()),
			attribute.String("template.resource", resourceName),
		),
	)
	if err := tmpl.ExecuteTemplate(&buf, g.TemplateName(), data); err != nil {
		return recordErr("err-render-template", fmt.Errorf("render template %s: %w", g.TemplateName(), err))
	}
	span.AddEvent("template.render.success",
		trace.WithAttributes(attribute.Int("terraform.rendered.bytes", buf.Len())),
	)
	span.SetAttributes(attribute.Int("terraform.rendered.bytes", buf.Len()))
	span.AddEvent("store.put.start", trace.WithAttributes(attribute.String("store.path", g.StoragePath())))
	storeStart := time.Now()
	outputPath, err := st.Put(ctx, g.StoragePath(), buf.Bytes())
	if err != nil {
		r.metrics.ObserveStorageOperation(g.Provider(), "put", "error", time.Since(storeStart))
		return recordErr("err-render-store-put", fmt.Errorf("write store: %w", err))
	}
	r.metrics.ObserveStorageOperation(g.Provider(), "put", "success", time.Since(storeStart))
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(attribute.String("terraform.provider.storage.output.path", outputPath))
	span.AddEvent("store.put.success", trace.WithAttributes(attribute.String("terraform.provider.storage.output.path", outputPath)))
	r.metrics.GenerationTotal.WithLabelValues(g.Provider(), resourceName, "success").Inc()
	r.metrics.GenerationDuration.WithLabelValues(g.Provider(), resourceName).Observe(time.Since(start).Seconds())
	r.metrics.ObserveRenderedBytes(g.Provider(), resourceName, buf.Len())
	return outputPath, nil
}

// Read returns the rendered main.tf for the located resource.
func (r *Renderer) Read(ctx context.Context, l resource.Locator) ([]byte, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "render.read",
		trace.WithAttributes(
			attribute.String("terraform.provider.name", l.Provider()),
			attribute.String("store.path", l.StoragePath()),
			attribute.String("terraform.provider.storage.path", l.StoragePath()),
		),
	)
	defer span.End()

	st, ok := r.stores[l.Provider()]
	if !ok {
		err := fmt.Errorf("no store registered for provider %s", l.Provider())
		recordSpanError(span, "err-render-no-store", err)
		return nil, err
	}
	start := time.Now()
	content, err := st.Get(ctx, l.StoragePath())
	if err != nil {
		r.metrics.ObserveStorageOperation(l.Provider(), "get", "error", time.Since(start))
		recordSpanError(span, "err-render-store-get", err)
		return nil, err
	}
	r.metrics.ObserveStorageOperation(l.Provider(), "get", "success", time.Since(start))
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(attribute.Int("terraform.output.bytes", len(content)))
	return content, nil
}

// List returns generated resource names under the directory containing the
// located resource path.
func (r *Renderer) List(ctx context.Context, l resource.Locator) ([]string, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "render.list",
		trace.WithAttributes(
			attribute.String("terraform.provider.name", l.Provider()),
			attribute.String("store.path", path.Dir(l.StoragePath())),
			attribute.String("terraform.provider.storage.path", path.Dir(l.StoragePath())),
		),
	)
	defer span.End()

	st, ok := r.stores[l.Provider()]
	if !ok {
		err := fmt.Errorf("no store registered for provider %s", l.Provider())
		recordSpanError(span, "err-render-no-store", err)
		return nil, err
	}
	start := time.Now()
	items, err := st.List(ctx, path.Dir(l.StoragePath()))
	if err != nil {
		r.metrics.ObserveStorageOperation(l.Provider(), "list", "error", time.Since(start))
		recordSpanError(span, "err-render-store-list", err)
		return nil, err
	}
	r.metrics.ObserveStorageOperation(l.Provider(), "list", "success", time.Since(start))
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(attribute.Int("terraform.resource.count", len(items)))
	return items, nil
}

// Delete removes the rendered files for the located resource.
func (r *Renderer) Delete(ctx context.Context, l resource.Locator) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "render.delete",
		trace.WithAttributes(
			attribute.String("terraform.provider.name", l.Provider()),
			attribute.String("store.path", l.StoragePath()),
			attribute.String("terraform.provider.storage.path", l.StoragePath()),
		),
	)
	defer span.End()

	st, ok := r.stores[l.Provider()]
	if !ok {
		err := fmt.Errorf("no store registered for provider %s", l.Provider())
		recordSpanError(span, "err-render-no-store", err)
		return err
	}
	start := time.Now()
	if err := st.Delete(ctx, l.StoragePath()); err != nil {
		r.metrics.ObserveStorageOperation(l.Provider(), "delete", "error", time.Since(start))
		recordSpanError(span, "err-render-store-delete", err)
		return err
	}
	r.metrics.ObserveStorageOperation(l.Provider(), "delete", "success", time.Since(start))
	span.SetStatus(codes.Ok, "")
	return nil
}

// recordSpanError records err on span using the service's structured exception
// attributes.
func recordSpanError(span trace.Span, slug string, err error) {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	span.SetAttributes(
		attribute.String("exception.slug", slug),
		attribute.String("exception.message", err.Error()),
		attribute.Bool("error", true),
	)
}

// resourceLabel converts a provider-relative template name into a stable metric
// label.
func resourceLabel(templateName string) string {
	name := strings.TrimSuffix(templateName, ".tf.tmpl")
	return strings.ReplaceAll(name, "/", "_")
}

var _ resource.Terraform = (*Renderer)(nil)
