package connection_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"golang.org/x/crypto/ssh"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/connection"
	"github.com/simonhauck/terraform-provider-dockercompose/internal/sshtest"
)

func sshConfig(srv sshtest.Server) connection.SSHConfig {
	return connection.SSHConfig{
		Host:       srv.Host,
		Port:       srv.Port,
		User:       srv.User,
		PrivateKey: srv.PrivateKey,
		HostKey:    srv.HostKey,
	}
}

func TestSSH_dockerAndFiles(t *testing.T) {
	srv := sshtest.Start(t)
	ctx := t.Context()

	conn, err := connection.SSH(ctx, sshConfig(srv))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck // test cleanup

	if _, err := conn.Docker.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("docker ping through ssh: %v", err)
	}

	path := "/srv/project/compose.yaml"
	if _, err := conn.FS.ReadFile(ctx, path); !connection.IsNotExist(err) {
		t.Fatalf("ReadFile on missing file: want not-exist error, got %v", err)
	}
	for _, content := range []string{"first", "second"} {
		if err := conn.FS.WriteFile(ctx, path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := conn.FS.ReadFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("content = %q, want second", got)
	}
	if err := conn.FS.RemoveAll(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := conn.FS.RemoveAll(ctx, path); err != nil {
		t.Errorf("RemoveAll on missing file: %v", err)
	}
}

func TestSSH_rejectsWrongHostKey(t *testing.T) {
	srv := sshtest.Start(t)
	cfg := sshConfig(srv)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	cfg.HostKey = string(ssh.MarshalAuthorizedKey(other))

	_, err = connection.SSH(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("want host key mismatch, got %v", err)
	}
}

func TestSSH_acceptsKnownHostsFormat(t *testing.T) {
	srv := sshtest.Start(t)
	cfg := sshConfig(srv)
	cfg.HostKey = "# comment\n[" + srv.Host + "]:22 " + srv.HostKey + "\n"

	conn, err := connection.SSH(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close() //nolint:errcheck,gosec // test
}

func TestSSHConfig_validation(t *testing.T) {
	tests := map[string]connection.SSHConfig{
		"no auth":     {Host: "h", User: "u", HostKey: "x"},
		"no host key": {Host: "h", User: "u", Password: "p"},
		"bad key":     {Host: "h", User: "u", PrivateKey: "not a key", InsecureIgnoreHostKey: true},
		"no user":     {Host: "h", Password: "p", InsecureIgnoreHostKey: true},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := connection.SSH(t.Context(), cfg); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}
