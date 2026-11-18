package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/sshtest"
)

// TestAccProject_overSSH deploys through the in-memory SSH connection. The
// Terraform process has no ssh key file and calls no ssh or docker binary.
func TestAccProject_overSSH(t *testing.T) {
	srv := sshtest.Start(t)
	name := testProjectName()
	address := "dockercompose_project.test"

	cfg := fmt.Sprintf(`
variable "private_key" {
  type      = string
  sensitive = true
}

variable "host_key" {
  type = string
}

provider "dockercompose" {
  ssh {
    host        = %[1]q
    port        = %[2]d
    user        = %[3]q
    private_key = var.private_key
    host_key    = var.host_key
  }
}

resource "dockercompose_project" "test" {
  name        = %[4]q
  project_dir = "/srv/%[4]s"
  compose = yamlencode({
    services = {
      app = {
        image   = "busybox:1.37"
        command = ["sleep", "3600"]
      }
    }
  })
  env = {
    GREETING = "over ssh"
  }
}
`, srv.Host, srv.Port, srv.User, name)
	vars := config.Variables{
		"private_key": config.StringVariable(srv.PrivateKey),
		"host_key":    config.StringVariable(srv.HostKey),
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:          cfg,
				ConfigVariables: vars,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("services"), knownvalue.MapExact(map[string]knownvalue.Check{
						"app": knownvalue.StringExact("running"),
					})),
				},
			},
			// Import reads the files back over SFTP.
			{
				Config:            cfg,
				ConfigVariables:   vars,
				ResourceName:      address,
				ImportState:       true,
				ImportStateId:     name,
				ImportStateVerify: true,
			},
		},
	})
}
