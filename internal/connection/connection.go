// Package connection opens the channels the provider needs on a Docker host:
// the Engine API and a file system for the project directory. It knows nothing
// about Terraform.
package connection

import (
	"context"
	"errors"
	"io/fs"
)

// Connection is an open link to one Docker host.
type Connection struct {
	// Docker is an Engine API client. Its concrete type is *client.Client, which
	// the compose SDK needs to build a docker CLI around it.
	Docker DockerClient
	// FS reads and writes files on the Docker host.
	FS FS

	close func() error
}

// Close releases the Docker client and any transport underneath it.
func (c *Connection) Close() error {
	if c.close == nil {
		return nil
	}
	return c.close()
}

// FS is the subset of file operations the provider performs on the Docker
// host. Paths are absolute, slash-separated paths on that host.
type FS interface {
	// ReadFile returns an error wrapping fs.ErrNotExist when path does not exist.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// WriteFile creates missing parent directories and replaces path atomically,
	// so a reader never sees a partially written file.
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	// RemoveAll removes path and everything below it. A missing path is not an error.
	RemoveAll(ctx context.Context, path string) error
}

// IsNotExist reports whether err means that a file does not exist.
func IsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
