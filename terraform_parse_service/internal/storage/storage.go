// Package storage defines persistence backends for generated Terraform output.
package storage

import "context"

// Store persists generated Terraform files under provider-relative keys.
type Store interface {
	Put(ctx context.Context, key string, content []byte) (location string, err error)
	Get(ctx context.Context, key string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
}
