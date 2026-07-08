package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "store.filesystem"

// FSStore stores each generated resource as a directory containing main.tf.
type FSStore struct {
	BaseDir string
}

// NewFSStore creates baseDir when needed and returns a filesystem-backed store
// rooted there.
func NewFSStore(baseDir string) (*FSStore, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}
	return &FSStore{BaseDir: baseDir}, nil
}

// Put stores content at BaseDir/key/main.tf using a temporary file and rename
// so readers do not observe a partially written file.
func (s *FSStore) Put(ctx context.Context, key string, content []byte) (string, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "store.put",
		trace.WithAttributes(
			attribute.String("store.backend", "filesystem"),
			attribute.String("store.operation", "put"),
			attribute.String("store.key", key),
			attribute.String("store.base_dir", s.BaseDir),
			attribute.Int("store.content.bytes", len(content)),
			attribute.Int("terraform.output.bytes", len(content)),
		),
	)
	defer span.End()

	dir, err := safeJoin(s.BaseDir, key)
	if err != nil {
		return "", markStoreError(span, "err-store-path-traversal", err)
	}
	span.SetAttributes(attribute.String("store.path", dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", markStoreError(span, "err-store-mkdir", fmt.Errorf("mkdir %s: %w", dir, err))
	}

	path := filepath.Join(dir, "main.tf")
	span.SetAttributes(attribute.String("terraform.provider.storage.output.path", path))
	span.AddEvent("store.write.start",
		trace.WithAttributes(
			attribute.String("terraform.provider.storage.output.path", path),
			attribute.Int("store.content.bytes", len(content)),
		),
	)
	if err := writeFileReplace(path, content); err != nil {
		return "", markStoreError(span, "err-store-write-file", fmt.Errorf("write %s: %w", path, err))
	}
	span.SetStatus(codes.Ok, "")
	span.AddEvent("store.write.success", trace.WithAttributes(attribute.String("terraform.provider.storage.output.path", path)))
	return path, nil
}

// Get reads BaseDir/key/main.tf after ensuring key cannot escape BaseDir.
func (s *FSStore) Get(ctx context.Context, key string) ([]byte, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "store.get",
		trace.WithAttributes(
			attribute.String("store.backend", "filesystem"),
			attribute.String("store.operation", "get"),
			attribute.String("store.key", key),
			attribute.String("store.base_dir", s.BaseDir),
		),
	)
	defer span.End()

	dir, err := safeJoin(s.BaseDir, key)
	if err != nil {
		return nil, markStoreError(span, "err-store-path-traversal", err)
	}
	path := filepath.Join(dir, "main.tf")
	span.SetAttributes(
		attribute.String("store.path", dir),
		attribute.String("terraform.provider.storage.output.path", path),
	)
	span.AddEvent("store.read.start", trace.WithAttributes(attribute.String("terraform.provider.storage.output.path", path)))
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, markStoreError(span, "err-store-read-file", fmt.Errorf("read %s: %w", path, err))
	}
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(
		attribute.Int("store.output.bytes", len(content)),
		attribute.Int("terraform.output.bytes", len(content)),
	)
	span.AddEvent("store.read.success",
		trace.WithAttributes(
			attribute.String("terraform.provider.storage.output.path", path),
			attribute.Int("store.output.bytes", len(content)),
		),
	)
	return content, nil
}

// List returns child directory names below BaseDir/prefix.
func (s *FSStore) List(ctx context.Context, prefix string) ([]string, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "store.list",
		trace.WithAttributes(
			attribute.String("store.backend", "filesystem"),
			attribute.String("store.operation", "list"),
			attribute.String("store.prefix", prefix),
			attribute.String("store.base_dir", s.BaseDir),
		),
	)
	defer span.End()

	dir, err := safeJoin(s.BaseDir, prefix)
	if err != nil {
		return nil, markStoreError(span, "err-store-path-traversal", err)
	}
	span.SetAttributes(attribute.String("store.path", dir))
	span.AddEvent("store.list.start", trace.WithAttributes(attribute.String("store.path", dir)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			span.SetStatus(codes.Ok, "")
			span.SetAttributes(attribute.Int("store.entry_count", 0))
			return []string{}, nil
		}
		return nil, markStoreError(span, "err-store-list-dir", fmt.Errorf("list %s: %w", dir, err))
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(
		attribute.Int("store.count", len(names)),
		attribute.Int("store.entry_count", len(names)),
	)
	span.AddEvent("store.list.success",
		trace.WithAttributes(
			attribute.String("store.path", dir),
			attribute.Int("store.entry_count", len(names)),
		),
	)
	return names, nil
}

// Delete removes BaseDir/key after ensuring key cannot escape BaseDir.
func (s *FSStore) Delete(ctx context.Context, key string) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "store.delete",
		trace.WithAttributes(
			attribute.String("store.backend", "filesystem"),
			attribute.String("store.operation", "delete"),
			attribute.String("store.key", key),
			attribute.String("store.base_dir", s.BaseDir),
		),
	)
	defer span.End()

	dir, err := safeJoin(s.BaseDir, key)
	if err != nil {
		return markStoreError(span, "err-store-path-traversal", err)
	}
	span.SetAttributes(attribute.String("store.path", dir))
	span.AddEvent("store.delete.start", trace.WithAttributes(attribute.String("store.path", dir)))
	if err := os.RemoveAll(dir); err != nil {
		return markStoreError(span, "err-store-delete", fmt.Errorf("delete %s: %w", dir, err))
	}
	span.SetStatus(codes.Ok, "")
	span.AddEvent("store.delete.success", trace.WithAttributes(attribute.String("store.path", dir)))
	return nil
}

func writeFileReplace(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".main.tf-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmpName, path)
}

func safeJoin(base string, parts ...string) (string, error) {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	allParts := append([]string{base}, parts...)
	target := filepath.Join(allParts...)
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("store path %q escapes base directory", filepath.Join(parts...))
	}
	return filepath.Clean(target), nil
}

func markStoreError(span trace.Span, slug string, err error) error {
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	span.SetAttributes(
		attribute.String("exception.slug", slug),
		attribute.String("exception.message", err.Error()),
		attribute.Bool("error", true),
	)
	return err
}
