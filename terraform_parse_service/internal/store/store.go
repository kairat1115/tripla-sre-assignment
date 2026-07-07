// Package store defines persistence backends for generated Terraform files.
package store

import "context"

// Store stores generated Terraform files under resource keys.
type Store interface {
	// Put stores content as the resource's main.tf and returns the final path.
	Put(ctx context.Context, key string, content []byte) (string, error)
	// Get returns the resource's generated main.tf.
	Get(ctx context.Context, key string) ([]byte, error)
	// List returns generated resource names below prefix.
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete removes the generated resource path.
	Delete(ctx context.Context, key string) error
}
