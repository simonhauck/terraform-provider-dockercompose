// Package provider implements the dockercompose Terraform provider.
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ provider.Provider = (*dockerComposeProvider)(nil)

type dockerComposeProvider struct {
	version string
}

// New returns a constructor for the provider, as expected by providerserver.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &dockerComposeProvider{version: version}
	}
}

func (p *dockerComposeProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "dockercompose"
	resp.Version = p.version
}

func (p *dockerComposeProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Deploys Docker Compose projects to local or remote (SSH) Docker hosts without requiring a local docker or ssh binary.",
	}
}

func (p *dockerComposeProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

func (p *dockerComposeProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *dockerComposeProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
