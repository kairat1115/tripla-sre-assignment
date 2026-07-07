// Package storage defines persistence backends for generated Terraform files.
package storage

import "context"

// Writer stores generated Terraform files under provider-specific resource
// paths. Implementations decide where those paths live.
type Writer interface {
	// Write stores content as the resource's main.tf and returns the final path.
	Write(ctx context.Context, name string, content []byte) (string, error)
	// Read returns the resource's generated main.tf.
	Read(ctx context.Context, name string) ([]byte, error)
	// List returns generated resource names below prefix.
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete removes the generated resource path.
	Delete(ctx context.Context, name string) error
}
