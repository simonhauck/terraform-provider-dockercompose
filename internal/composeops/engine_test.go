package composeops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/connection"
)

// newTestEngine connects to the local Docker daemon. Like acceptance tests,
// these tests only run with TF_ACC set, because they need a daemon.
func newTestEngine(t *testing.T) (*Engine, *connection.Connection) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run tests against a Docker daemon")
	}
	conn, err := connection.Local(t.Context(), connection.LocalConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck // test cleanup
	engine, err := NewEngine(conn)
	if err != nil {
		t.Fatal(err)
	}
	return engine, conn
}

func engineSpec(t *testing.T) Spec {
	name := fmt.Sprintf("tfdc-engine-%d", time.Now().UnixNano())
	return Spec{
		Name: name,
		Compose: `
services:
  app:
    image: busybox:1.37
    command: ["sh", "-c", "echo $$GREETING; sleep 3600"]
    environment:
      GREETING: ${GREETING}
    depends_on:
      init:
        condition: service_completed_successfully
  init:
    image: busybox:1.37
    command: ["true"]
`,
		Env:        map[string]string{"GREETING": "hello"},
		ProjectDir: t.TempDir(),
	}
}

func TestEngine_lifecycle(t *testing.T) {
	engine, conn := newTestEngine(t)
	ctx := t.Context()
	spec := engineSpec(t)
	t.Cleanup(func() {
		// t.Context() is already cancelled when cleanups run.
		_ = engine.Destroy(context.Background(), spec, DestroyOptions{RemoveVolumes: true}) //nolint:usetesting

	})
	opts := ApplyOptions{WriteFiles: true, RemoveOrphans: true, Wait: true, WaitTimeout: 2 * time.Minute}

	if err := engine.Apply(ctx, spec, opts); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Status(ctx, spec, StatusOptions{CheckFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exists || len(status.Drift) != 0 {
		t.Fatalf("after apply: want in sync, got %+v", status)
	}
	if status.Services["app"] != "running" || status.Services["init"] != "exited (0)" {
		t.Errorf("services = %v", status.Services)
	}
	appID := containerID(t, conn, spec.Name, "app")

	// Re-applying an unchanged spec must not recreate anything.
	if err := engine.Apply(ctx, spec, opts); err != nil {
		t.Fatal(err)
	}
	if got := containerID(t, conn, spec.Name, "app"); got != appID {
		t.Error("unchanged apply recreated the app container")
	}

	// Changing an env var recreates the service that uses it.
	spec.Env["GREETING"] = "changed"
	if err := engine.Apply(ctx, spec, opts); err != nil {
		t.Fatal(err)
	}
	if got := containerID(t, conn, spec.Name, "app"); got == appID {
		t.Error("env change did not recreate the app container")
	}

	// Out-of-band changes show up as drift.
	if _, err := conn.Docker.ContainerStop(ctx, containerID(t, conn, spec.Name, "app"), client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.ComposePath(), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err = engine.Status(ctx, spec, StatusOptions{CheckFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Drift) != 2 {
		t.Errorf("want 2 drift entries (stopped container, edited file), got %q", status.Drift)
	}

	if err := engine.Destroy(ctx, spec, DestroyOptions{RemoveFiles: true}); err != nil {
		t.Fatal(err)
	}
	status, err = engine.Status(ctx, spec, StatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Exists {
		t.Errorf("after destroy: project still exists: %+v", status)
	}
	if _, err := os.Stat(spec.ComposePath()); !os.IsNotExist(err) {
		t.Errorf("compose file not removed: %v", err)
	}
	if _, err := os.Stat(spec.ProjectDir); err != nil {
		t.Errorf("project dir itself must be kept: %v", err)
	}
}

func containerID(t *testing.T, conn *connection.Connection, project, service string) string {
	t.Helper()
	res, err := conn.Docker.ContainerList(t.Context(), client.ContainerListOptions{
		All: true,
		Filters: client.Filters{}.
			Add("label", "com.docker.compose.project="+project).
			Add("label", "com.docker.compose.service="+service),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		names := make([]string, 0, len(res.Items))
		for _, c := range res.Items {
			names = append(names, strings.Join(c.Names, "/"))
		}
		t.Fatalf("want 1 container for %s/%s, got %v", project, service, names)
	}
	return res.Items[0].ID
}
