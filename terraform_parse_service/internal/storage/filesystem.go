package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

type FSWriter struct {
	BaseDir string
}

func NewFSWriter(baseDir string) (*FSWriter, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}
	return &FSWriter{BaseDir: baseDir}, nil
}

func (w *FSWriter) Write(name string, content []byte) (string, error) {
	dir := filepath.Join(w.BaseDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
