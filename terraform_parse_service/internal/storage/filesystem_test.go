package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestFSWriter_Read_Valid(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("terraform content")
	if _, err := w.Write(context.Background(), "s3/my-bucket", want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := w.Read(context.Background(), "s3/my-bucket")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestFSWriter_Read_NotFound(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Read(context.Background(), "s3/nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestFSWriter_Read_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Read(context.Background(), "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestFSWriter_List_Empty(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	names, err := w.List(context.Background(), "s3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("want empty slice, got %v", names)
	}
}

func TestFSWriter_List_AfterWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"s3/bucket-a", "s3/bucket-b"} {
		if _, err := w.Write(context.Background(), name, []byte("x")); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	names, err := w.List(context.Background(), "s3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("want 2 entries, got %d: %v", len(names), names)
	}
}

func TestFSWriter_Delete_Valid(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(context.Background(), "s3/my-bucket", []byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Delete(context.Background(), "s3/my-bucket"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	bucketDir := filepath.Join(dir, "s3", "my-bucket")
	if _, err := os.Stat(bucketDir); !os.IsNotExist(err) {
		t.Fatalf("expected dir to be deleted, stat err: %v", err)
	}
}

func TestFSWriter_Delete_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = w.Delete(context.Background(), "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}
