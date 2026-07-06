package storage

import (
	"context"
	"testing"
)

func TestFSWriter_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write(context.Background(), "../../etc/passwd", []byte("x"))
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestFSWriter_ValidPath(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write(context.Background(), "s3/my-bucket", []byte("content"))
	if err != nil {
		t.Fatalf("unexpected error for valid path: %v", err)
	}
}
