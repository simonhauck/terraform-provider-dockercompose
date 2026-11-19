package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/sshtest"
)

const (
	// providerSource is the provider address the fixture module requires.
	providerSource = "registry.terraform.io/simonhauck/dockercompose"
	// providerVersion is the version of the test build in the filesystem mirror.
	providerVersion = "0.0.1"
)

// buildProvider compiles the provider binary the way the release build does
// (see .goreleaser.yml) and lays it out as a Terraform filesystem mirror. It
// returns the mirror directory.
func buildProvider(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(filepath.Dir(file))

	mirror := t.TempDir()
	target := filepath.Join(mirror, filepath.FromSlash(providerSource), providerVersion, runtime.GOOS+"_"+runtime.GOARCH)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(target, "terraform-provider-dockercompose_v"+providerVersion)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	cmd := exec.CommandContext(t.Context(), "go", "build",
		"-trimpath",
		"-ldflags", "-s -w -X main.version="+providerVersion,
		"-o", bin, ".")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building provider: %v\n%s", err, out)
	}
	return mirror
}

// terraformCLI runs the real terraform binary in a working directory that
// holds a copy of the fixture module.
type terraformCLI struct {
	bin  string
	dir  string
	env  []string
	vars map[string]string
}

// newTerraformCLI builds the provider, writes a CLI config that installs it
// from a filesystem mirror, and copies the fixture module into a temp dir.
func newTerraformCLI(t *testing.T, vars map[string]string) *terraformCLI {
	t.Helper()
	bin := os.Getenv("TF_ACC_TERRAFORM_PATH")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("terraform"); err != nil {
			t.Fatal("terraform CLI not found on PATH; install it or set TF_ACC_TERRAFORM_PATH")
		}
	}

	mirror := buildProvider(t)
	// Only the mirror may supply this provider, so init cannot silently pick
	// up a published release instead of the build under test.
	cliConfig := filepath.Join(t.TempDir(), "terraform.rc")
	writeLocalFile(t, cliConfig, fmt.Sprintf(`provider_installation {
  filesystem_mirror {
    path    = %[1]q
    include = [%[2]q]
  }
  direct {
    exclude = [%[2]q]
  }
}
`, mirror, providerSource))

	dir := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "project", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	writeLocalFile(t, filepath.Join(dir, "main.tf"), string(fixture))

	return &terraformCLI{
		bin: bin,
		dir: dir,
		env: append(os.Environ(),
			"TF_CLI_CONFIG_FILE="+cliConfig,
			"TF_IN_AUTOMATION=1",
			"TF_INPUT=0",
			"CHECKPOINT_DISABLE=1",
		),
		vars: vars,
	}
}

// setVar changes an input variable for all following commands.
func (tf *terraformCLI) setVar(name, value string) {
	tf.vars[name] = value
}

// invoke runs terraform and returns its combined output and exit code. err is
// set only when terraform could not be run at all. Output is logged unless
// quiet is set, for commands such as `show -json` that print sensitive values.
func (tf *terraformCLI) invoke(ctx context.Context, t testing.TB, quiet bool, args ...string) (out string, code int, err error) {
	cmd := exec.CommandContext(ctx, tf.bin, args...)
	cmd.Dir = tf.dir
	cmd.Env = append([]string{}, tf.env...)
	for k, v := range tf.vars {
		cmd.Env = append(cmd.Env, "TF_VAR_"+k+"="+v)
	}
	b, runErr := cmd.CombinedOutput()
	out = string(b)
	if quiet {
		t.Logf("$ terraform %s (output not logged)", strings.Join(args, " "))
	} else {
		t.Logf("$ terraform %s\n%s", strings.Join(args, " "), out)
	}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return out, 0, nil
	case errors.As(runErr, &exitErr):
		return out, exitErr.ExitCode(), nil
	default:
		return out, -1, runErr
	}
}

// run runs terraform and returns its output and exit code, whatever it is.
func (tf *terraformCLI) run(t testing.TB, args ...string) (string, int) {
	t.Helper()
	out, code, err := tf.invoke(t.Context(), t, false, args...)
	if err != nil {
		t.Fatalf("running terraform %s: %v", strings.Join(args, " "), err)
	}
	return out, code
}

// mustRun runs terraform and fails the test unless it exits with 0.
func (tf *terraformCLI) mustRun(t testing.TB, args ...string) string {
	t.Helper()
	out, code := tf.run(t, args...)
	if code != 0 {
		t.Fatalf("terraform %s exited with %d", strings.Join(args, " "), code)
	}
	return out
}

// mustRunQuiet is mustRun without logging the output.
func (tf *terraformCLI) mustRunQuiet(t testing.TB, args ...string) string {
	t.Helper()
	out, code, err := tf.invoke(t.Context(), t, true, args...)
	if err != nil || code != 0 {
		t.Fatalf("terraform %s exited with %d: %v", strings.Join(args, " "), code, err)
	}
	return out
}

// planActions returns the planned actions of each resource in a saved plan,
// for example {"dockercompose_project.this": ["update"]}.
func (tf *terraformCLI) planActions(t testing.TB, planFile string) map[string][]string {
	t.Helper()
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal([]byte(tf.mustRunQuiet(t, "show", "-json", planFile)), &plan); err != nil {
		t.Fatalf("parsing plan JSON: %v", err)
	}
	actions := map[string][]string{}
	for _, rc := range plan.ResourceChanges {
		actions[rc.Address] = rc.Change.Actions
	}
	return actions
}

// servicesOutput returns the `services` output of the fixture module.
func (tf *terraformCLI) servicesOutput(t testing.TB) map[string]string {
	t.Helper()
	var services map[string]string
	if err := json.Unmarshal([]byte(tf.mustRun(t, "output", "-json", "services")), &services); err != nil {
		t.Fatalf("parsing services output: %v", err)
	}
	return services
}

// docker runs the docker CLI against the same daemon the SSH test server
// forwards to, for checks and changes made out of band.
func docker(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, exitErr.Stderr)
	}
	if err != nil {
		return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func mustDocker(t testing.TB, args ...string) string {
	t.Helper()
	out, err := docker(t.Context(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// projectLabel filters Docker objects that compose created for a project.
func projectLabel(project string) string {
	return "label=com.docker.compose.project=" + project
}

// containerIDs lists all containers of a project, running or not.
func containerIDs(ctx context.Context, project string) ([]string, error) {
	out, err := docker(ctx, "ps", "--all", "--quiet", "--no-trunc", "--filter", projectLabel(project))
	return strings.Fields(out), err
}

// networkIDs lists the networks of a project.
func networkIDs(ctx context.Context, project string) ([]string, error) {
	out, err := docker(ctx, "network", "ls", "--quiet", "--filter", projectLabel(project))
	return strings.Fields(out), err
}

// volumeNames lists the volumes of a project.
func volumeNames(ctx context.Context, project string) ([]string, error) {
	out, err := docker(ctx, "volume", "ls", "--quiet", "--filter", projectLabel(project))
	return strings.Fields(out), err
}

// serviceContainer returns the ID of the single container of a service.
func serviceContainer(t testing.TB, project, service string) string {
	t.Helper()
	ids := strings.Fields(mustDocker(t, "ps", "--all", "--quiet", "--no-trunc",
		"--filter", projectLabel(project),
		"--filter", "label=com.docker.compose.service="+service))
	if len(ids) != 1 {
		t.Fatalf("service %s has %d containers, want 1: %v", service, len(ids), ids)
	}
	return ids[0]
}

// projectVolume returns the name of a named volume of a project.
func projectVolume(t testing.TB, project, volume string) string {
	t.Helper()
	names := strings.Fields(mustDocker(t, "volume", "ls", "--quiet",
		"--filter", projectLabel(project),
		"--filter", "label=com.docker.compose.volume="+volume))
	if len(names) != 1 {
		t.Fatalf("volume %s has %d matches, want 1: %v", volume, len(names), names)
	}
	return names[0]
}

// removeProject removes everything compose created for a project on the
// daemon, including volumes. It is best effort and reports what it could not
// remove.
func removeProject(ctx context.Context, project string) error {
	var errs []error
	if ids, err := containerIDs(ctx, project); err != nil {
		errs = append(errs, err)
	} else if len(ids) > 0 {
		_, err := docker(ctx, append([]string{"rm", "--force", "--volumes"}, ids...)...)
		errs = append(errs, err)
	}
	if ids, err := networkIDs(ctx, project); err != nil {
		errs = append(errs, err)
	} else if len(ids) > 0 {
		_, err := docker(ctx, append([]string{"network", "rm"}, ids...)...)
		errs = append(errs, err)
	}
	if names, err := volumeNames(ctx, project); err != nil {
		errs = append(errs, err)
	} else if len(names) > 0 {
		_, err := docker(ctx, append([]string{"volume", "rm", "--force"}, names...)...)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// dialSFTP opens an SFTP session to the SSH test server, verifying its host
// key, to inspect project files the way they exist on the server.
func dialSFTP(t *testing.T, srv sshtest.Server) *sftp.Client {
	t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(srv.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(srv.HostKey))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)), &ssh.ClientConfig{
		User:            srv.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		// The server has several host keys; negotiate the one we can verify.
		HostKeyAlgorithms: []string{hostKey.Type()},
		Timeout:           30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close() //nolint:errcheck // already failing
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close() //nolint:errcheck // test cleanup
		conn.Close()   //nolint:errcheck // test cleanup
	})
	return client
}

// readRemote returns the content of a file on the server.
func readRemote(t testing.TB, client *sftp.Client, p string) string {
	t.Helper()
	f, err := client.Open(p)
	if err != nil {
		t.Fatalf("opening %s on the server: %v", p, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading %s on the server: %v", p, err)
	}
	return string(b)
}

// writeRemote creates a file and its parent directories on the server.
func writeRemote(t testing.TB, client *sftp.Client, p, content string) {
	t.Helper()
	if err := client.MkdirAll(path.Dir(p)); err != nil {
		t.Fatalf("creating %s on the server: %v", path.Dir(p), err)
	}
	f, err := client.Create(p)
	if err != nil {
		t.Fatalf("creating %s on the server: %v", p, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		f.Close() //nolint:errcheck // already failing
		t.Fatalf("writing %s on the server: %v", p, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing %s on the server: %v", p, err)
	}
}

// assertRemoteMissing fails unless a path is absent on the server.
func assertRemoteMissing(t testing.TB, client *sftp.Client, p string) {
	t.Helper()
	_, err := client.Stat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		t.Errorf("checking %s on the server: %v", p, err)
	default:
		t.Errorf("%s still exists on the server", p)
	}
}

func writeLocalFile(t testing.TB, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
