// Package provider implements the dockercompose Terraform provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/attr"
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
	SSH   *sshModel   `tfsdk:"ssh"`
}

type localModel struct {
	Host types.String `tfsdk:"host"`
}

type sshModel struct {
	Host                  types.String `tfsdk:"host"`
	Port                  types.Int64  `tfsdk:"port"`
	User                  types.String `tfsdk:"user"`
	PrivateKey            types.String `tfsdk:"private_key"`
	PrivateKeyPassphrase  types.String `tfsdk:"private_key_passphrase"`
	Password              types.String `tfsdk:"password"`
	HostKey               types.String `tfsdk:"host_key"`
	InsecureIgnoreHostKey types.Bool   `tfsdk:"insecure_ignore_host_key"`
	DockerSocket          types.String `tfsdk:"docker_socket"`
}

func (m *sshModel) unknown() bool {
	for _, v := range []attr.Value{m.Host, m.Port, m.User, m.PrivateKey, m.PrivateKeyPassphrase,
		m.Password, m.HostKey, m.InsecureIgnoreHostKey, m.DockerSocket} {
		if v.IsUnknown() {
			return true
		}
	}
	return false
}

func (m *sshModel) config() connection.SSHConfig {
	return connection.SSHConfig{
		Host:                  m.Host.ValueString(),
		Port:                  int(m.Port.ValueInt64()),
		User:                  m.User.ValueString(),
		PrivateKey:            m.PrivateKey.ValueString(),
		PrivateKeyPassphrase:  m.PrivateKeyPassphrase.ValueString(),
		Password:              m.Password.ValueString(),
		HostKey:               m.HostKey.ValueString(),
		InsecureIgnoreHostKey: m.InsecureIgnoreHostKey.ValueBool(),
		DockerSocket:          m.DockerSocket.ValueString(),
	}
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
			"ssh": schema.SingleNestedBlock{
				MarkdownDescription: "Connect to a remote Docker host over SSH. The provider uses its own SSH client: " +
					"credentials stay in memory and no `ssh` binary is needed.\n\n" +
					"The Docker socket is reached through SSH stream-local forwarding, so the server must allow it " +
					"(`AllowTcpForwarding` set to `yes` or `local`, the OpenSSH default). Project files are written over SFTP.",
				Attributes: map[string]schema.Attribute{
					"host": schema.StringAttribute{
						MarkdownDescription: "Hostname or IP address of the SSH server.",
						Optional:            true,
					},
					"port": schema.Int64Attribute{
						MarkdownDescription: "SSH port. Defaults to `22`.",
						Optional:            true,
					},
					"user": schema.StringAttribute{
						MarkdownDescription: "SSH user. It needs access to the Docker socket and write access to the project directories.",
						Optional:            true,
					},
					"private_key": schema.StringAttribute{
						MarkdownDescription: "Private key in OpenSSH or PEM format, for example `tls_private_key.deploy.private_key_openssh`.",
						Optional:            true,
						Sensitive:           true,
					},
					"private_key_passphrase": schema.StringAttribute{
						MarkdownDescription: "Passphrase of an encrypted `private_key`.",
						Optional:            true,
						Sensitive:           true,
					},
					"password": schema.StringAttribute{
						MarkdownDescription: "Password authentication, used in addition to or instead of `private_key`.",
						Optional:            true,
						Sensitive:           true,
					},
					"host_key": schema.StringAttribute{
						MarkdownDescription: "Accepted host keys, one per line, in `authorized_keys` format (`ssh-ed25519 AAAA...`) " +
							"or `known_hosts` format. Required unless `insecure_ignore_host_key` is set.",
						Optional: true,
					},
					"insecure_ignore_host_key": schema.BoolAttribute{
						MarkdownDescription: "Accept any host key. This allows man-in-the-middle attacks; use it only for testing.",
						Optional:            true,
					},
					"docker_socket": schema.StringAttribute{
						MarkdownDescription: "Path of the Docker socket on the remote host. Defaults to `/var/run/docker.sock`.",
						Optional:            true,
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

	if cfg.Local != nil && cfg.SSH != nil {
		resp.Diagnostics.AddError("Conflicting connection blocks", "Configure either a local or an ssh block, not both.")
		return
	}

	var connect func(context.Context) (*connection.Connection, error)
	switch {
	case cfg.SSH != nil && cfg.SSH.unknown():
		// Planning new resources needs no connection, so this only fails if a
		// resource has to talk to the host before the values are known.
		connect = func(context.Context) (*connection.Connection, error) {
			return nil, errors.New("the ssh block depends on values that are not known yet")
		}
	case cfg.SSH != nil:
		sshCfg := cfg.SSH.config()
		connect = func(ctx context.Context) (*connection.Connection, error) {
			return connection.SSH(ctx, sshCfg)
		}
	default:
		local := connection.LocalConfig{}
		if cfg.Local != nil {
			local.Host = cfg.Local.Host.ValueString()
		}
		connect = func(ctx context.Context) (*connection.Connection, error) {
			return connection.Local(ctx, local)
		}
	}

	data := &providerData{connect: connect}
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
