package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/moby/moby/client"
)

func projectConfig(name, dir, greeting string) string {
	return fmt.Sprintf(`
provider "dockercompose" {
  local {}
}

resource "dockercompose_project" "test" {
  name        = %[1]q
  project_dir = %[2]q
  compose = yamlencode({
    services = {
      app = {
        image   = "busybox:1.37"
        command = ["sh", "-c", "echo $$GREETING; sleep 3600"]
        environment = { GREETING = "$${GREETING}" }
      }
    }
  })
  env = {
    GREETING = %[3]q
  }
}
`, name, dir, greeting)
}

func networksOnlyConfig(name, dir string, allResources bool) string {
	allResourcesArg := ""
	if allResources {
		allResourcesArg = "all_resources = true"
	}
	return fmt.Sprintf(`
provider "dockercompose" {
  local {}
}

resource "dockercompose_project" "test" {
  name        = %[1]q
  project_dir = %[2]q
  %[3]s
  compose = yamlencode({
    networks = {
      private = { driver = "bridge" }
    }
  })
}
`, name, dir, allResourcesArg)
}

func testProjectName() string {
	return fmt.Sprintf("tfacc-%d", time.Now().UnixNano())
}

func TestAccProject_lifecycle(t *testing.T) {
	name := testProjectName()
	dir := t.TempDir()
	address := "dockercompose_project.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: projectConfig(name, dir, "hello"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("id"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("drift"), knownvalue.Null()),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("services"), knownvalue.MapExact(map[string]knownvalue.Check{
						"app": knownvalue.StringExact("running"),
					})),
				},
				Check: func(*terraform.State) error {
					env, err := os.ReadFile(filepath.Join(dir, ".env"))
					if err != nil {
						return err
					}
					if string(env) != "GREETING='hello'\n" {
						return fmt.Errorf(".env = %q", env)
					}
					return nil
				},
			},
			// Changing env updates in place.
			{
				Config: projectConfig(name, dir, "changed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
					},
				},
			},
			// Stopping a container out of band shows up as drift.
			{
				PreConfig: func() {
					stopService(t, name, "app")
				},
				Config:             projectConfig(name, dir, "changed"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// Applying again repairs it.
			{
				Config: projectConfig(name, dir, "changed"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("services"), knownvalue.MapExact(map[string]knownvalue.Check{
						"app": knownvalue.StringExact("running"),
					})),
				},
			},
			// A hand-edited compose file shows the changed lines as drift.
			{
				PreConfig: func() {
					editComposeFile(t, filepath.Join(dir, "compose.yaml"), "busybox:1.37", "busybox:1.36")
				},
				Config:             projectConfig(name, dir, "changed"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue(address, tfjsonpath.New("drift")),
					},
				},
			},
			// Applying the unchanged config restores the file and clears drift.
			{
				Config: projectConfig(name, dir, "changed"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("drift"), knownvalue.Null()),
				},
			},
			// Import reads compose and env back from the mirrored files.
			{
				ResourceName:      address,
				ImportState:       true,
				ImportStateId:     name,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccProject_networksOnly(t *testing.T) {
	name := testProjectName()
	dir := t.TempDir()
	address := "dockercompose_project.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: networksOnlyConfig(name, dir, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("all_resources"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("services"), knownvalue.MapSizeExact(0)),
				},
				Check: func(*terraform.State) error {
					if got := projectNetworkNames(t, name); len(got) != 1 {
						return fmt.Errorf("project networks = %v, want one", got)
					}
					return nil
				},
			},
			// Removing the network out of band shows up in the next plan.
			{
				PreConfig:          func() { removeProjectNetwork(t, name, "private") },
				Config:             networksOnlyConfig(name, dir, true),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// Applying again recreates it.
			{
				Config: networksOnlyConfig(name, dir, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("services"), knownvalue.MapSizeExact(0)),
				},
			},
		},
	})
}

func TestAccProject_networksOnlyNeedsAllResources(t *testing.T) {
	name := testProjectName()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      networksOnlyConfig(name, t.TempDir(), false),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`all_resources`),
			},
		},
	})
}

func TestAccProject_unknownEnvAtPlan(t *testing.T) {
	name := testProjectName()
	dir := t.TempDir()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The secret only becomes known during apply, as with a
				// password generated in the same run.
				Config: fmt.Sprintf(`
provider "dockercompose" {
  local {}
}

resource "terraform_data" "secret" {
  input = "generated"
}

resource "dockercompose_project" "test" {
  name        = %[1]q
  project_dir = %[2]q
  compose = yamlencode({
    services = {
      app = {
        image       = "busybox:1.37"
        command     = ["sleep", "3600"]
        environment = { SECRET = "$${SECRET}" }
      }
    }
  })
  env = {
    SECRET = terraform_data.secret.output
  }
}
`, name, dir),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue("dockercompose_project.test", tfjsonpath.New("config_hash")),
					},
				},
			},
		},
	})
}

func TestAccProject_invalidComposeFailsAtPlan(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "dockercompose" {
  local {}
}

resource "dockercompose_project" "test" {
  name        = "invalid"
  project_dir = "/tmp/invalid"
  compose     = "services:\n  app:\n    imagee: busybox\n"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Invalid compose project`),
			},
		},
	})
}

func TestDriftSummary(t *testing.T) {
	got := driftSummary([]string{
		"service app has no container",
		"/srv/app/compose.yaml was modified:\n--- /srv/app/compose.yaml (host)\n+++ /srv/app/compose.yaml (desired)",
	})
	want := "service app has no container; /srv/app/compose.yaml was modified"
	if got != want {
		t.Errorf("driftSummary = %q, want %q", got, want)
	}
}

func stopService(t *testing.T, project, service string) {
	t.Helper()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close() //nolint:errcheck // test helper
	res, err := docker.ContainerList(t.Context(), client.ContainerListOptions{
		Filters: client.Filters{}.
			Add("label", "com.docker.compose.project="+project).
			Add("label", "com.docker.compose.service="+service),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Items {
		if _, err := docker.ContainerStop(t.Context(), c.ID, client.ContainerStopOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func editComposeFile(t *testing.T, path, old, replacement string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(data), old, replacement, 1)
	if modified == string(data) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	if err := os.WriteFile(path, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}
}

func projectNetworkNames(t *testing.T, project string) []string {
	t.Helper()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close() //nolint:errcheck // test helper
	res, err := docker.NetworkList(t.Context(), client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", "com.docker.compose.project="+project),
	})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Items))
	for _, n := range res.Items {
		names = append(names, n.Name)
	}
	return names
}

func removeProjectNetwork(t *testing.T, project, network string) {
	t.Helper()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close() //nolint:errcheck // test helper
	res, err := docker.NetworkList(t.Context(), client.NetworkListOptions{
		Filters: client.Filters{}.
			Add("label", "com.docker.compose.project="+project).
			Add("label", "com.docker.compose.network="+network),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("want one network for %s/%s, got %d", project, network, len(res.Items))
	}
	if _, err := docker.NetworkRemove(t.Context(), res.Items[0].ID, client.NetworkRemoveOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestAccProject_exampleComposeFile plans the documented compose-file example,
// so the example keeps loading as the provider evolves.
func TestAccProject_exampleComposeFile(t *testing.T) {
	composeFile, err := filepath.Abs("../../examples/resources/dockercompose_project/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "dockercompose" {
  local {}
}

resource "dockercompose_project" "app" {
  name    = "app"
  compose = file(%q)
  env = {
    DB_PASSWORD = "secret"
  }
}
`, composeFile),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue("dockercompose_project.app", tfjsonpath.New("project_dir"), knownvalue.StringExact("/opt/dockercompose/app")),
					},
				},
			},
		},
	})
}
