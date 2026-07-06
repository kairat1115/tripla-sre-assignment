package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
