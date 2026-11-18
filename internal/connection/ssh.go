package connection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// DefaultDockerSocket is the Docker socket path on the remote host.
const DefaultDockerSocket = "/var/run/docker.sock"

// SSHConfig configures a connection to a remote Docker host over SSH. All
// credentials are held in memory; nothing is written to disk and no ssh
// binary is involved.
type SSHConfig struct {
	Host string
	// Port defaults to 22.
	Port int
	User string

	// PrivateKey is a PEM or OpenSSH-format private key.
	PrivateKey string
	// PrivateKeyPassphrase decrypts PrivateKey, if it is encrypted.
	PrivateKeyPassphrase string
	Password             string

	// HostKey lists the accepted host keys, one per line, in authorized_keys
	// ("ssh-ed25519 AAAA...") or known_hosts ("host ssh-ed25519 AAAA...") format.
	HostKey string
	// InsecureIgnoreHostKey accepts any host key. Only meant for tests.
	InsecureIgnoreHostKey bool

	// DockerSocket defaults to DefaultDockerSocket.
	DockerSocket string
	// Timeout bounds establishing the TCP connection and SSH handshake. Defaults to 30s.
	Timeout time.Duration
}

// SSH connects to a remote host and reaches its Docker daemon through an SSH
// stream-local channel. Project files are written over SFTP on the same
// connection.
func SSH(ctx context.Context, cfg SSHConfig) (*Connection, error) {
	clientConfig, err := cfg.clientConfig()
	if err != nil {
		return nil, err
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	sshClient, err := dialSSH(ctx, addr, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}

	socket := cfg.DockerSocket
	if socket == "" {
		socket = DefaultDockerSocket
	}
	docker, err := client.New(
		// The host only shapes request URLs; every connection goes through the dialer.
		client.WithHost("http://docker"),
		client.WithDialContext(func(ctx context.Context, _, _ string) (net.Conn, error) {
			return sshClient.DialContext(ctx, "unix", socket)
		}),
	)
	if err != nil {
		sshClient.Close() //nolint:errcheck,gosec // reporting the original error
		return nil, fmt.Errorf("creating Docker client: %w", err)
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		docker.Close()    //nolint:errcheck,gosec // reporting the original error
		sshClient.Close() //nolint:errcheck,gosec
		return nil, fmt.Errorf("starting SFTP on %s (is the sftp subsystem enabled?): %w", addr, err)
	}

	return &Connection{
		Docker: docker,
		FS:     sftpFS{sftpClient},
		close: func() error {
			return errors.Join(docker.Close(), sftpClient.Close(), sshClient.Close())
		},
	}, nil
}

func dialSSH(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline) //nolint:errcheck,gosec // best effort for the handshake
	} else {
		conn.SetDeadline(time.Now().Add(cfg.Timeout)) //nolint:errcheck,gosec
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close() //nolint:errcheck,gosec // reporting the handshake error
		return nil, err
	}
	conn.SetDeadline(time.Time{}) //nolint:errcheck,gosec // the session itself has no deadline
	return ssh.NewClient(c, chans, reqs), nil
}

func (cfg SSHConfig) clientConfig() (*ssh.ClientConfig, error) {
	if cfg.Host == "" || cfg.User == "" {
		return nil, errors.New("ssh host and user are required")
	}

	var auth []ssh.AuthMethod
	if cfg.PrivateKey != "" {
		var signer ssh.Signer
		var err error
		if cfg.PrivateKeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.PrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("parsing ssh private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auth = append(auth, ssh.Password(cfg.Password))
	}
	if len(auth) == 0 {
		return nil, errors.New("ssh needs a private key or a password")
	}

	var hostKeyCallback ssh.HostKeyCallback
	var hostKeyAlgorithms []string
	switch {
	case cfg.InsecureIgnoreHostKey:
		hostKeyCallback = ssh.InsecureIgnoreHostKey() //nolint:gosec // explicitly requested
	case cfg.HostKey != "":
		keys, err := parseHostKeys(cfg.HostKey)
		if err != nil {
			return nil, err
		}
		hostKeyCallback = fixedHostKeys(keys)
		// Servers usually have several host keys. Negotiate one we can verify,
		// as OpenSSH does for hosts in known_hosts.
		hostKeyAlgorithms = algorithmsFor(keys)
	default:
		return nil, errors.New("ssh host_key is required to verify the server; set insecure_ignore_host_key only for testing")
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &ssh.ClientConfig{
		User:              cfg.User,
		Auth:              auth,
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: hostKeyAlgorithms,
		Timeout:           timeout,
	}, nil
}

// parseHostKeys accepts authorized_keys and known_hosts lines. Host patterns in
// known_hosts lines are ignored: the configured host is the one being checked.
func parseHostKeys(text string) ([]ssh.PublicKey, error) {
	var keys []ssh.PublicKey
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			_, _, key, _, _, err = ssh.ParseKnownHosts([]byte(line))
		}
		if err != nil {
			return nil, fmt.Errorf("parsing ssh host_key line %d: %w", i+1, err)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("ssh host_key contains no keys")
	}
	return keys, nil
}

// algorithmsFor lists the host key algorithms that can produce the given keys.
func algorithmsFor(keys []ssh.PublicKey) []string {
	var algos []string
	for _, k := range keys {
		candidates := []string{k.Type()}
		if k.Type() == ssh.KeyAlgoRSA {
			candidates = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		}
		for _, a := range candidates {
			if !slices.Contains(algos, a) {
				algos = append(algos, a)
			}
		}
	}
	return algos
}

func fixedHostKeys(keys []ssh.PublicKey) ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, presented ssh.PublicKey) error {
		for _, k := range keys {
			if k.Type() == presented.Type() && bytes.Equal(k.Marshal(), presented.Marshal()) {
				return nil
			}
		}
		return fmt.Errorf("ssh host key mismatch for %s: server presented %s %s, which is not in host_key",
			hostname, presented.Type(), ssh.FingerprintSHA256(presented))
	}
}

type sftpFS struct {
	c *sftp.Client
}

func (f sftpFS) ReadFile(_ context.Context, p string) ([]byte, error) {
	file, err := f.c.Open(p)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck // read-only
	return io.ReadAll(file)
}

func (f sftpFS) WriteFile(_ context.Context, p string, data []byte, perm os.FileMode) error {
	dir := path.Dir(p)
	if err := f.c.MkdirAll(dir); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp := path.Join(dir, fmt.Sprintf(".%s.tmp-%d", path.Base(p), time.Now().UnixNano()))
	file, err := f.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	if err := file.Chmod(perm); err != nil {
		file.Close()    //nolint:errcheck,gosec // reporting the original error
		f.c.Remove(tmp) //nolint:errcheck,gosec
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()    //nolint:errcheck,gosec
		f.c.Remove(tmp) //nolint:errcheck,gosec
		return err
	}
	if err := file.Close(); err != nil {
		f.c.Remove(tmp) //nolint:errcheck,gosec
		return err
	}
	if err := f.c.PosixRename(tmp, p); err != nil {
		f.c.Remove(tmp) //nolint:errcheck,gosec
		return fmt.Errorf("renaming into place (the server must support posix-rename@openssh.com): %w", err)
	}
	return nil
}

func (f sftpFS) RemoveAll(_ context.Context, p string) error {
	err := f.c.RemoveAll(p)
	if IsNotExist(err) {
		return nil
	}
	return err
}
