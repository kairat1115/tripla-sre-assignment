package storage

import "context"

type Writer interface {
	Write(ctx context.Context, name string, content []byte) (string, error)
	Read(ctx context.Context, name string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, name string) error
}
