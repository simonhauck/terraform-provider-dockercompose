package connection

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moby/moby/client"
)

// DockerClient is the Engine API client type handed to the compose SDK.
type DockerClient = *client.Client

// LocalConfig configures a connection to a Docker daemon reachable from the
// machine running Terraform.
type LocalConfig struct {
	// Host is a Docker host URL such as unix:///var/run/docker.sock. Empty means
	// DOCKER_HOST, or the platform default when that is unset.
	Host string
}

// Local connects to a Docker daemon on, or directly reachable from, this machine.
// Project files are written to the local file system.
func Local(_ context.Context, cfg LocalConfig) (*Connection, error) {
	opts := []client.Opt{client.FromEnv}
	if cfg.Host != "" {
		opts = append(opts, client.WithHost(cfg.Host))
	}
	docker, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating Docker client: %w", err)
	}
	return &Connection{
		Docker: docker,
		FS:     localFS{},
		close:  docker.Close,
	}, nil
}

type localFS struct{}

func (localFS) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (localFS) WriteFile(_ context.Context, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the write error is the one to report
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (localFS) RemoveAll(_ context.Context, path string) error {
	return os.RemoveAll(path)
}
