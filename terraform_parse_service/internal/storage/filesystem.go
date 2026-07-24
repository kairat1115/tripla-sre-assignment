package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Filesystem stores each generated resource as a directory containing main.tf.
type Filesystem struct {
	baseDir string
}

// NewFilesystem creates a filesystem store rooted at baseDir.
func NewFilesystem(baseDir string) (*Filesystem, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create base directory: %w", err)
	}
	return &Filesystem{baseDir: baseDir}, nil
}

// Put stores content as main.tf below key and returns its filesystem path.
func (f *Filesystem) Put(ctx context.Context, key string, content []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir, err := safeJoin(f.baseDir, key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}

	outputPath := filepath.Join(dir, "main.tf")
	if err := replaceFile(outputPath, content); err != nil {
		return "", fmt.Errorf("write %s: %w", outputPath, err)
	}
	return outputPath, nil
}

// Get returns the generated main.tf below key.
func (f *Filesystem) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := safeJoin(f.baseDir, key)
	if err != nil {
		return nil, err
	}
	outputPath := filepath.Join(dir, "main.tf")
	content, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", outputPath, err)
	}
	return content, nil
}

// List returns generated resource names below prefix.
func (f *Filesystem) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := safeJoin(f.baseDir, prefix)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// Delete removes the generated resource below key.
func (f *Filesystem) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := safeJoin(f.baseDir, key)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete %s: %w", dir, err)
	}
	return nil
}

func replaceFile(outputPath string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(outputPath), ".main.tf-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

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
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmpPath, outputPath)
}

func safeJoin(baseDir string, parts ...string) (string, error) {
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(append([]string{baseDir}, parts...)...))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path %q escapes storage directory", filepath.Join(parts...))
	}
	return target, nil
}
