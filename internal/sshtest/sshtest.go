// Package sshtest starts an SSH server in a container for tests. The server
// forwards to the test host's Docker socket, so deployments made through it
// land on the same daemon the tests can inspect.
package sshtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

// Server is a running SSH server container.
type Server struct {
	Host string
	Port int
	User string
	// PrivateKey authenticates User, in OpenSSH PEM format.
	PrivateKey string
	// HostKey is the server's ed25519 host key in authorized_keys format.
	HostKey string
}

// Start builds and runs the SSH server container and stops it when the test ends.
// It skips the test unless TF_ACC is set, because it needs a Docker daemon.
func Start(t *testing.T) Server {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run tests against a Docker daemon")
	}
	ctx := t.Context()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privPEM, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	_, file, _, _ := runtime.Caller(0)
	ctr, err := testcontainers.Run(ctx, "",
		testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context: filepath.Join(filepath.Dir(file), "testdata"),
		}),
		testcontainers.WithEnv(map[string]string{
			"AUTHORIZED_KEY": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))),
		}),
		testcontainers.WithExposedPorts("22/tcp"),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Mounts = append(hc.Mounts, mount.Mount{
				Type:   mount.TypeBind,
				Source: dockerSocketPath(),
				Target: "/var/run/docker.sock",
			})
		}),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("22/tcp")),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, "22/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return Server{
		Host:       host,
		Port:       int(port.Num()),
		User:       "root",
		PrivateKey: string(pem.EncodeToMemory(privPEM)),
		HostKey:    strings.TrimSpace(readFile(ctx, t, ctr, "/etc/ssh/ssh_host_ed25519_key.pub")),
	}
}

func readFile(ctx context.Context, t *testing.T, ctr testcontainers.Container, path string) string {
	t.Helper()
	rc, err := ctr.CopyFileFromContainer(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close() //nolint:errcheck // read-only
	var b strings.Builder
	if _, err := io.Copy(&b, rc); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// dockerSocketPath is the daemon socket on the test host, as seen by the daemon.
func dockerSocketPath() string {
	if host := os.Getenv("DOCKER_HOST"); strings.HasPrefix(host, "unix://") {
		return strings.TrimPrefix(host, "unix://")
	}
	return "/var/run/docker.sock"
}
