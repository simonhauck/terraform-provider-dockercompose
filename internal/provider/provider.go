// Package provider implements the dockercompose Terraform provider.
package provider

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/composeops"
	"github.com/simonhauck/terraform-provider-dockercompose/internal/connection"
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

type providerModel struct {
	Local *localModel `tfsdk:"local"`
}

type localModel struct {
	Host types.String `tfsdk:"host"`
}

func (p *dockerComposeProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "dockercompose"
	resp.Version = p.version
}

func (p *dockerComposeProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Deploys Docker Compose projects to local or remote (SSH) Docker hosts. " +
			"Compose runs in-process, so the machine running Terraform needs no `docker`, `docker compose` or `ssh` binary.\n\n" +
			"Without a connection block, the provider connects like `local {}`.",
		Blocks: map[string]schema.Block{
			"local": schema.SingleNestedBlock{
				MarkdownDescription: "Connect to a Docker daemon reachable from the machine running Terraform.",
				Attributes: map[string]schema.Attribute{
					"host": schema.StringAttribute{
						MarkdownDescription: "Docker host URL, for example `unix:///var/run/docker.sock` or `tcp://127.0.0.1:2375`. " +
							"Defaults to `DOCKER_HOST`, or the platform default when that is unset.",
						Optional: true,
					},
				},
			},
		},
	}
}

func (p *dockerComposeProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	local := connection.LocalConfig{}
	if cfg.Local != nil {
		local.Host = cfg.Local.Host.ValueString()
	}
	data := &providerData{
		connect: func(ctx context.Context) (*connection.Connection, error) {
			return connection.Local(ctx, local)
		},
	}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *dockerComposeProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newProjectResource,
	}
}

func (p *dockerComposeProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// providerData is shared by all resources of one provider instance. The
// connection opens on first use, so planning new resources needs no host.
type providerData struct {
	connect func(context.Context) (*connection.Connection, error)

	once   sync.Once
	engine *composeops.Engine
	err    error
}

func (d *providerData) Engine(ctx context.Context) (*composeops.Engine, error) {
	d.once.Do(func() {
		conn, err := d.connect(ctx)
		if err != nil {
			d.err = fmt.Errorf("connecting to Docker host: %w", err)
			return
		}
		d.engine, d.err = composeops.NewEngine(conn)
	})
	return d.engine, d.err
}
