package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFSStore_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Put(context.Background(), "../../etc/passwd", []byte("x"))
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestFSStore_ValidPath(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Put(context.Background(), "s3/my-bucket", []byte("content"))
	if err != nil {
		t.Fatalf("unexpected error for valid path: %v", err)
	}
}

func TestFSStore_Get_Valid(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("terraform content")
	if _, err := st.Put(context.Background(), "s3/my-bucket", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := st.Get(context.Background(), "s3/my-bucket")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestFSStore_Get_NotFound(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Get(context.Background(), "s3/nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestFSStore_Get_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Get(context.Background(), "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestFSStore_List_Empty(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	names, err := st.List(context.Background(), "s3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("want empty slice, got %v", names)
	}
}

func TestFSStore_List_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.List(context.Background(), "../../etc")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestFSStore_List_AfterPut(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"s3/bucket-a", "s3/bucket-b"} {
		if _, err := st.Put(context.Background(), name, []byte("x")); err != nil {
			t.Fatalf("put %s: %v", name, err)
		}
	}
	names, err := st.List(context.Background(), "s3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("want 2 entries, got %d: %v", len(names), names)
	}
}

func TestFSStore_Delete_Valid(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(context.Background(), "s3/my-bucket", []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := st.Delete(context.Background(), "s3/my-bucket"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	bucketDir := filepath.Join(dir, "s3", "my-bucket")
	if _, err := os.Stat(bucketDir); !os.IsNotExist(err) {
		t.Fatalf("expected dir to be deleted, stat err: %v", err)
	}
}

func TestFSStore_Delete_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = st.Delete(context.Background(), "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}
