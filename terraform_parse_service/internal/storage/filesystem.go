package storage

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

const tracerName = "storage.filesystem"

type FSWriter struct {
	BaseDir string
}

func NewFSWriter(baseDir string) (*FSWriter, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}
	return &FSWriter{BaseDir: baseDir}, nil
}

func (w *FSWriter) Write(ctx context.Context, name string, content []byte) (string, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "storage.write",
		trace.WithAttributes(
			attribute.String("storage.name", name),
			attribute.String("storage.base_dir", w.BaseDir),
		),
	)
	defer span.End()

	dir := filepath.Join(w.BaseDir, name)
	if !isWithinBase(w.BaseDir, dir) {
		err := fmt.Errorf("storage path %q escapes base directory", name)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(
			attribute.String("exception.slug", "err-storage-path-traversal"),
			attribute.Bool("error", true),
		)
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		err = fmt.Errorf("mkdir %s: %w", dir, err)
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("exception.slug", "err-storage-mkdir"),
			attribute.Bool("error", true),
		)
		return "", err
	}
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		err = fmt.Errorf("write %s: %w", path, err)
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("exception.slug", "err-storage-write-file"),
			attribute.Bool("error", true),
		)
		return "", err
	}
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(attribute.String("output.path", path))
	return path, nil
}

func (w *FSWriter) Read(ctx context.Context, name string) ([]byte, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "storage.read",
		trace.WithAttributes(
			attribute.String("storage.name", name),
			attribute.String("storage.base_dir", w.BaseDir),
		),
	)
	defer span.End()

	path := filepath.Join(w.BaseDir, name, "main.tf")
	if !isWithinBase(w.BaseDir, path) {
		err := fmt.Errorf("storage path %q escapes base directory", name)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(
			attribute.String("exception.slug", "err-storage-path-traversal"),
			attribute.Bool("error", true),
		)
		return nil, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("exception.slug", "err-storage-read-file"),
			attribute.Bool("error", true),
		)
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	span.SetStatus(codes.Ok, "")
	return content, nil
}

func (w *FSWriter) List(ctx context.Context, prefix string) ([]string, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "storage.list",
		trace.WithAttributes(
			attribute.String("storage.prefix", prefix),
			attribute.String("storage.base_dir", w.BaseDir),
		),
	)
	defer span.End()

	dir := filepath.Join(w.BaseDir, prefix)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			span.SetStatus(codes.Ok, "")
			return []string{}, nil
		}
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("exception.slug", "err-storage-list-dir"),
			attribute.Bool("error", true),
		)
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	span.SetStatus(codes.Ok, "")
	span.SetAttributes(attribute.Int("storage.count", len(names)))
	return names, nil
}

func (w *FSWriter) Delete(ctx context.Context, name string) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "storage.delete",
		trace.WithAttributes(
			attribute.String("storage.name", name),
			attribute.String("storage.base_dir", w.BaseDir),
		),
	)
	defer span.End()

	dir := filepath.Join(w.BaseDir, name)
	if !isWithinBase(w.BaseDir, dir) {
		err := fmt.Errorf("storage path %q escapes base directory", name)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(
			attribute.String("exception.slug", "err-storage-path-traversal"),
			attribute.Bool("error", true),
		)
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.SetAttributes(
			attribute.String("exception.slug", "err-storage-delete"),
			attribute.Bool("error", true),
		)
		return fmt.Errorf("delete %s: %w", dir, err)
	}
	span.SetStatus(codes.Ok, "")
	return nil
}

func isWithinBase(base, target string) bool {
	base = filepath.Clean(base) + string(filepath.Separator)
	return strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), base)
}
