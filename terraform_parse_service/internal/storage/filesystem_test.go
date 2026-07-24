package storage

import (
	"errors"
	"os"
	"slices"
	"testing"
)

func TestFilesystemStoreLifecycle(t *testing.T) {
	files, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := t.Context()
	if _, err := files.Put(ctx, "s3/bucket-b", []byte("bucket-b")); err != nil {
		t.Fatalf("put bucket-b: %v", err)
	}
	if _, err := files.Put(ctx, "s3/bucket-a", []byte("bucket-a")); err != nil {
		t.Fatalf("put bucket-a: %v", err)
	}

	content, err := files.Get(ctx, "s3/bucket-a")
	if err != nil {
		t.Fatalf("get bucket-a: %v", err)
	}
	if string(content) != "bucket-a" {
		t.Fatalf("want bucket-a content, got %q", content)
	}

	names, err := files.List(ctx, "s3")
	if err != nil {
		t.Fatalf("list buckets: %v", err)
	}
	if !slices.Equal(names, []string{"bucket-a", "bucket-b"}) {
		t.Fatalf("want [bucket-a bucket-b], got %v", names)
	}

	if err := files.Delete(ctx, "s3/bucket-a"); err != nil {
		t.Fatalf("delete bucket-a: %v", err)
	}
	if _, err := files.Get(ctx, "s3/bucket-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted bucket: want os.ErrNotExist, got %v", err)
	}
}

func TestFilesystemStoreRejectsKeysOutsideRoot(t *testing.T) {
	files, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := t.Context()
	operations := map[string]func() error{
		"put": func() error {
			_, err := files.Put(ctx, "../../outside", []byte("content"))
			return err
		},
		"get": func() error {
			_, err := files.Get(ctx, "../../outside")
			return err
		},
		"list": func() error {
			_, err := files.List(ctx, "../../outside")
			return err
		},
		"delete": func() error {
			return files.Delete(ctx, "../../outside")
		},
	}

	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil {
				t.Fatal("want key outside storage root to be rejected")
			}
		})
	}
}
