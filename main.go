// Command terraform-provider-dockercompose serves the dockercompose Terraform provider.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/provider"
)

// Set by GoReleaser via ldflags.
var version = "dev"

//go:generate go tool tfplugindocs generate --provider-name dockercompose

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/simonhauck/dockercompose",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
