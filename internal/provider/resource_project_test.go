package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
