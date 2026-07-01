package storage

import "context"

type Writer interface {
	Write(ctx context.Context, name string, content []byte) (string, error)
}
