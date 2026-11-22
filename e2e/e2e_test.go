// Package e2e drives the built provider binary with the real terraform CLI,
// the way a user does, against a Docker host reached over SSH.
//
// Unlike the acceptance tests in internal/provider, which serve the provider
// in-process through terraform-plugin-testing, this covers the release-style
// binary, `terraform init` installing it, the plan output a user reads, and
// the files left on the server.
//
// It needs TF_ACC=1, a Docker daemon, and the terraform and docker CLIs.
package e2e

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/sshtest"
)

const address = "dockercompose_project.this"

func TestE2E_projectLifecycle(t *testing.T) {
	srv := sshtest.Start(t) // skips unless TF_ACC is set

	name := fmt.Sprintf("tfe2e-%d", time.Now().UnixNano())
	projectDir := "/srv/" + name
	composePath := path.Join(projectDir, "compose.yaml")
	envPath := path.Join(projectDir, ".env")
	// A file the user keeps next to the mirrored ones; destroy must not touch it.
	userFile := path.Join(projectDir, "notes", "keep.txt")
	// Written into the named volume out of band; must survive update and destroy.
	volumeToken := fmt.Sprintf("volume-data-%d", time.Now().UnixNano())
	const planFile = "e2e.tfplan"

	tf := newTerraformCLI(t, map[string]string{
		"ssh_host":        srv.Host,
		"ssh_port":        strconv.Itoa(srv.Port),
		"ssh_user":        srv.User,
		"ssh_private_key": srv.PrivateKey,
		"ssh_host_key":    srv.HostKey,
		"name":            name,
		"project_dir":     projectDir,
		"greeting":        "hello",
	})
	files := dialSFTP(t, srv)

	// Registered after sshtest.Start, so it runs while the SSH server is still up.
	destroyed := false
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if !destroyed {
			if _, code, err := tf.invoke(ctx, t, false, "destroy", "-auto-approve", "-no-color"); err != nil || code != 0 {
				t.Logf("cleanup: terraform destroy exited with %d: %v", code, err)
			}
		}
		// The volume is deliberately kept by destroy; remove it and anything a
		// failed run left behind.
		if err := removeProject(ctx, name); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		for what, list := range map[string]func(context.Context, string) ([]string, error){
			"containers": containerIDs, "networks": networkIDs, "volumes": volumeNames,
		} {
			if left, err := list(ctx, name); err != nil || len(left) > 0 {
				t.Errorf("cleanup: %s left behind: %v (err: %v)", what, left, err)
			}
		}
	})

	if !t.Run("init installs the built provider", func(t *testing.T) {
		out := tf.mustRun(t, "init", "-no-color")
		if want := "simonhauck/dockercompose v" + providerVersion; !strings.Contains(out, want) {
			t.Errorf("init output does not mention %q", want)
		}
	}) {
		return
	}

	var appID, storeID string

	if !t.Run("1 apply creates the project", func(t *testing.T) {
		tf.mustRun(t, "apply", "-auto-approve", "-no-color")

		assertServices(t, tf, name)
		appID = serviceContainer(t, name, "app")
		storeID = serviceContainer(t, name, "store")

		if got := readRemote(t, files, composePath); !strings.Contains(got, "busybox:1.37") {
			t.Errorf("%s does not hold the compose file:\n%s", composePath, got)
		}
		if got, want := readRemote(t, files, envPath), "GREETING='hello'\n"; got != want {
			t.Errorf("%s = %q, want %q", envPath, got, want)
		}
		if fi, err := files.Stat(envPath); err != nil {
			t.Error(err)
		} else if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", envPath, fi.Mode().Perm())
		}

		writeRemote(t, files, userFile, volumeToken)
		mustDocker(t, "exec", storeID, "sh", "-c", `printf %s "$1" > /data/marker`, "sh", volumeToken)
	}) {
		return
	}

	if !t.Run("2 changing env updates in place and recreates only the affected service", func(t *testing.T) {
		tf.setVar("greeting", "changed")

		out, code := tf.run(t, "plan", "-no-color", "-detailed-exitcode", "-out="+planFile)
		if code != 2 {
			t.Fatalf("plan exited with %d, want 2 (changes present)", code)
		}
		for _, want := range []string{
			address + " will be updated in-place",
			"Plan: 0 to add, 1 to change, 0 to destroy.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("plan output does not contain %q", want)
			}
		}
		assertActions(t, tf.planActions(t, planFile), "update")

		tf.mustRun(t, "apply", "-no-color", planFile)

		assertServices(t, tf, name)
		if got := serviceContainer(t, name, "app"); got == appID {
			t.Error("app was not recreated although its environment changed")
		}
		if got := serviceContainer(t, name, "store"); got != storeID {
			t.Errorf("store was recreated (%s -> %s) although nothing it uses changed", storeID, got)
		}
		app := serviceContainer(t, name, "app")
		if got := mustDocker(t, "exec", app, "printenv", "GREETING"); got != "changed" {
			t.Errorf("GREETING in app = %q, want %q", got, "changed")
		}
		if got := mustDocker(t, "exec", storeID, "cat", "/data/marker"); got != volumeToken {
			t.Errorf("volume data after update = %q, want %q", got, volumeToken)
		}
		if got, want := readRemote(t, files, envPath), "GREETING='changed'\n"; got != want {
			t.Errorf("%s = %q, want %q", envPath, got, want)
		}
	}) {
		return
	}

	if !t.Run("3 plan after apply has no changes", func(t *testing.T) {
		out, code := tf.run(t, "plan", "-no-color", "-detailed-exitcode")
		if code != 0 {
			t.Fatalf("plan exited with %d, want 0 (no changes)", code)
		}
		if !strings.Contains(out, "No changes.") {
			t.Error(`plan output does not contain "No changes."`)
		}
	}) {
		return
	}

	if !t.Run("4 a container stopped out of band shows as drift with its reason", func(t *testing.T) {
		mustDocker(t, "kill", serviceContainer(t, name, "app"))

		out, code := tf.run(t, "plan", "-no-color", "-detailed-exitcode", "-out="+planFile)
		if code != 2 {
			t.Fatalf("plan exited with %d, want 2 (changes present)", code)
		}
		for _, want := range []string{
			address + " will be updated in-place",
			"drifted: service app container",
			"-app-1 is exited (",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("plan output does not contain %q", want)
			}
		}
		assertActions(t, tf.planActions(t, planFile), "update")
	}) {
		return
	}

	t.Run("5 a hand-edited compose file shows the changed lines as drift", func(t *testing.T) {
		compose := readRemote(t, files, composePath)
		edited := strings.Replace(compose, `"image": "busybox:1.37"`, `"image": "busybox:1.36"`, 1)
		if edited == compose {
			t.Fatalf("%s does not contain the expected image tag:\n%s", composePath, compose)
		}
		writeRemote(t, files, composePath, edited)

		out, code := tf.run(t, "plan", "-no-color", "-detailed-exitcode", "-out="+planFile)
		if code != 2 {
			t.Fatalf("plan exited with %d, want 2 (changes present)", code)
		}
		for _, want := range []string{
			address + " will be updated in-place",
			"/srv/" + name + "/compose.yaml was modified:",
			`-    "image": "busybox:1.36"`,
			`+    "image": "busybox:1.37"`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("plan output does not contain %q", want)
			}
		}
		assertActions(t, tf.planActions(t, planFile), "update")
	})

	t.Run("6 destroy removes containers and mirrored files but keeps volume data and other files", func(t *testing.T) {
		tf.mustRun(t, "destroy", "-auto-approve", "-no-color")
		destroyed = true

		if left, err := containerIDs(t.Context(), name); err != nil || len(left) > 0 {
			t.Errorf("containers left after destroy: %v (err: %v)", left, err)
		}
		if left, err := networkIDs(t.Context(), name); err != nil || len(left) > 0 {
			t.Errorf("networks left after destroy: %v (err: %v)", left, err)
		}
		if out := tf.mustRun(t, "state", "list"); strings.TrimSpace(out) != "" {
			t.Errorf("state still lists resources:\n%s", out)
		}

		assertRemoteMissing(t, files, composePath)
		assertRemoteMissing(t, files, envPath)
		if got := readRemote(t, files, userFile); got != volumeToken {
			t.Errorf("%s = %q after destroy, want it untouched", userFile, got)
		}

		// remove_volumes_on_destroy defaults to false: the volume and its data stay.
		volume := projectVolume(t, name, "data")
		got := mustDocker(t, "run", "--rm", "--volume", volume+":/data:ro", "busybox:1.37", "cat", "/data/marker")
		if got != volumeToken {
			t.Errorf("volume data after destroy = %q, want %q", got, volumeToken)
		}
	})
}

// assertServices checks that every service is running, both as Terraform
// reports it and on the Docker daemon.
func assertServices(t *testing.T, tf *terraformCLI, project string) {
	t.Helper()
	want := map[string]string{"app": "running", "store": "running"}
	if got := tf.servicesOutput(t); !maps.Equal(got, want) {
		t.Errorf("services output = %v, want %v", got, want)
	}
	for service := range want {
		id := serviceContainer(t, project, service)
		if state := mustDocker(t, "inspect", "--format", "{{.State.Status}}", id); state != "running" {
			t.Errorf("container of %s is %s, want running", service, state)
		}
	}
}

// assertActions checks that the plan changes only the project, with exactly
// the given actions (so an update is never a replace).
func assertActions(t *testing.T, got map[string][]string, want ...string) {
	t.Helper()
	if len(got) != 1 || !slices.Equal(got[address], want) {
		t.Errorf("planned actions = %v, want only %s: %v", got, address, want)
	}
}
