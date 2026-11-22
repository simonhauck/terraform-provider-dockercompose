package composeops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/connection"
)

// Engine runs compose operations against one Docker host.
type Engine struct {
	conn    *connection.Connection
	compose api.Compose
}

// NewEngine wraps a connection with the compose SDK. Compose output is discarded;
// failures come back as errors.
func NewEngine(conn *connection.Connection) (*Engine, error) {
	cli, err := command.NewDockerCli(command.WithAPIClient(conn.Docker))
	if err != nil {
		return nil, fmt.Errorf("creating docker CLI: %w", err)
	}
	if err := cli.Initialize(flags.NewClientOptions(), command.WithAPIClient(conn.Docker)); err != nil {
		return nil, fmt.Errorf("initializing docker CLI: %w", err)
	}
	svc, err := compose.NewComposeService(cli,
		compose.WithStreams(io.Discard, io.Discard, strings.NewReader("")),
		// Compose only prompts for destructive choices; never agree implicitly.
		compose.WithPrompt(func(string, bool) (bool, error) { return false, nil }),
	)
	if err != nil {
		return nil, fmt.Errorf("creating compose service: %w", err)
	}
	return &Engine{conn: conn, compose: svc}, nil
}

// ApplyOptions tune how a project is brought up.
type ApplyOptions struct {
	// WriteFiles mirrors the compose file and .env into the project directory.
	WriteFiles    bool
	RemoveOrphans bool
	// Wait blocks until all services are running or healthy, up to WaitTimeout.
	Wait        bool
	WaitTimeout time.Duration
}

// Apply converges the Docker host to the spec, like `docker compose up -d`.
// Only services whose configuration changed are recreated.
func (e *Engine) Apply(ctx context.Context, spec Spec, opts ApplyOptions) error {
	project, err := Load(ctx, spec)
	if err != nil {
		return err
	}
	if opts.WriteFiles {
		if err := e.writeFiles(ctx, spec); err != nil {
			return err
		}
	}
	err = e.compose.Up(ctx, project, api.UpOptions{
		Create: api.CreateOptions{
			RemoveOrphans:        opts.RemoveOrphans,
			Recreate:             api.RecreateDiverged,
			RecreateDependencies: api.RecreateDiverged,
			QuietPull:            true,
		},
		Start: api.StartOptions{
			Project:     project,
			Wait:        opts.Wait,
			WaitTimeout: opts.WaitTimeout,
		},
	})
	if err != nil {
		return fmt.Errorf("compose up %s: %w", spec.Name, err)
	}
	return nil
}

func (e *Engine) writeFiles(ctx context.Context, spec Spec) error {
	if err := e.conn.FS.WriteFile(ctx, spec.ComposePath(), []byte(spec.Compose), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", spec.ComposePath(), err)
	}
	if err := e.conn.FS.WriteFile(ctx, spec.EnvPath(), spec.EnvFile(), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", spec.EnvPath(), err)
	}
	return nil
}

// DestroyOptions tune how a project is removed.
type DestroyOptions struct {
	// RemoveFiles deletes the mirrored compose file and .env. The rest of the
	// project directory, for example bind-mounted data, is left alone.
	RemoveFiles   bool
	RemoveVolumes bool
}

// Destroy removes the project's containers and networks, like `docker compose down`.
func (e *Engine) Destroy(ctx context.Context, spec Spec, opts DestroyOptions) error {
	err := e.compose.Down(ctx, spec.Name, api.DownOptions{
		RemoveOrphans: true,
		Volumes:       opts.RemoveVolumes,
	})
	if err != nil {
		return fmt.Errorf("compose down %s: %w", spec.Name, err)
	}
	if opts.RemoveFiles {
		return e.RemoveFiles(ctx, spec)
	}
	return nil
}

// RemoveFiles deletes the mirrored compose file and .env from the project
// directory, and nothing else in it.
func (e *Engine) RemoveFiles(ctx context.Context, spec Spec) error {
	for _, p := range []string{spec.ComposePath(), spec.EnvPath()} {
		if err := e.conn.FS.RemoveAll(ctx, p); err != nil {
			return fmt.Errorf("removing %s: %w", p, err)
		}
	}
	return nil
}

// Status is the observed state of a project on the Docker host.
type Status struct {
	// Exists is false when the host has none of the project's containers,
	// networks or volumes at all.
	Exists bool
	// Services maps each service to the state of its containers, e.g. "running"
	// or "exited (1)". Multiple replicas are comma-separated.
	Services map[string]string
	// Drift lists why the host differs from the spec. Empty means in sync.
	Drift []string
}

// StatusOptions tune what Status compares.
type StatusOptions struct {
	// CheckFiles compares the mirrored files with the spec.
	CheckFiles bool
}

// Status inspects the project's containers and resources and reports drift
// from the spec. It has no side effects.
func (e *Engine) Status(ctx context.Context, spec Spec, opts StatusOptions) (Status, error) {
	containers, err := e.compose.Ps(ctx, spec.Name, api.PsOptions{All: true})
	if err != nil {
		return Status{}, fmt.Errorf("listing containers of %s: %w", spec.Name, err)
	}
	project, err := Load(ctx, spec)
	if err != nil {
		return Status{}, err
	}
	networks, err := e.projectNetworks(ctx, spec.Name)
	if err != nil {
		return Status{}, err
	}
	volumes, err := e.projectVolumes(ctx, spec.Name)
	if err != nil {
		return Status{}, err
	}

	status := Status{Exists: len(containers) > 0, Services: map[string]string{}}
	for _, key := range project.NetworkNames() {
		network := project.Networks[key]
		if network.External {
			continue
		}
		if _, ok := networks[key]; !ok {
			status.Drift = append(status.Drift, fmt.Sprintf("network %s is missing", network.Name))
			continue
		}
		status.Exists = true
	}
	for _, key := range project.VolumeNames() {
		volume := project.Volumes[key]
		if volume.External {
			continue
		}
		if _, ok := volumes[key]; !ok {
			status.Drift = append(status.Drift, fmt.Sprintf("volume %s is missing", volume.Name))
			continue
		}
		status.Exists = true
	}
	if !status.Exists {
		return status, nil
	}

	byService := map[string][]api.ContainerSummary{}
	for _, c := range containers {
		byService[c.Service] = append(byService[c.Service], c)
	}
	for name, cs := range byService {
		states := make([]string, 0, len(cs))
		for _, c := range cs {
			states = append(states, describeState(c))
		}
		slices.Sort(states)
		status.Services[name] = strings.Join(states, ", ")
	}

	for _, name := range project.ServiceNames() {
		svc := project.Services[name]
		cs := byService[name]
		if len(cs) == 0 {
			status.Drift = append(status.Drift, fmt.Sprintf("service %s has no container", name))
			continue
		}
		for _, c := range cs {
			if !healthyState(svc, c) {
				status.Drift = append(status.Drift, fmt.Sprintf("service %s container %s is %s", name, c.Name, describeState(c)))
			}
		}
	}

	if opts.CheckFiles {
		drift, err := e.fileDrift(ctx, spec)
		if err != nil {
			return Status{}, err
		}
		status.Drift = append(status.Drift, drift...)
	}
	return status, nil
}

// projectNetworks returns the compose keys of the project's networks, as
// recorded in the labels Compose puts on the resources it creates.
func (e *Engine) projectNetworks(ctx context.Context, project string) (map[string]struct{}, error) {
	res, err := e.conn.Docker.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", api.ProjectLabel+"="+project),
	})
	if err != nil {
		return nil, fmt.Errorf("listing networks of %s: %w", project, err)
	}
	keys := make(map[string]struct{}, len(res.Items))
	for _, network := range res.Items {
		if _, relay := network.Labels[api.RelayNetworkLabel]; relay {
			continue
		}
		if key := network.Labels[api.NetworkLabel]; key != "" {
			keys[key] = struct{}{}
		}
	}
	return keys, nil
}

// projectVolumes returns the compose keys of the project's volumes.
func (e *Engine) projectVolumes(ctx context.Context, project string) (map[string]struct{}, error) {
	res, err := e.conn.Docker.VolumeList(ctx, client.VolumeListOptions{
		Filters: client.Filters{}.Add("label", api.ProjectLabel+"="+project),
	})
	if err != nil {
		return nil, fmt.Errorf("listing volumes of %s: %w", project, err)
	}
	keys := make(map[string]struct{}, len(res.Items))
	for _, volume := range res.Items {
		if key := volume.Labels[api.VolumeLabel]; key != "" {
			keys[key] = struct{}{}
		}
	}
	return keys, nil
}

func (e *Engine) fileDrift(ctx context.Context, spec Spec) ([]string, error) {
	var drift []string
	compose, err := e.conn.FS.ReadFile(ctx, spec.ComposePath())
	switch {
	case connection.IsNotExist(err):
		drift = append(drift, fmt.Sprintf("%s is missing", spec.ComposePath()))
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", spec.ComposePath(), err)
	case !bytes.Equal(compose, []byte(spec.Compose)):
		drift = append(drift, describeFileChange(spec.ComposePath(), string(compose), spec.Compose))
	}

	env, err := e.conn.FS.ReadFile(ctx, spec.EnvPath())
	switch {
	case connection.IsNotExist(err):
		drift = append(drift, fmt.Sprintf("%s is missing", spec.EnvPath()))
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", spec.EnvPath(), err)
	case !bytes.Equal(env, spec.EnvFile()):
		// Never include the content: env holds sensitive values.
		drift = append(drift, fmt.Sprintf("%s was modified (contents are sensitive)", spec.EnvPath()))
	}
	return drift, nil
}

func describeState(c api.ContainerSummary) string {
	if c.State == container.StateExited {
		return fmt.Sprintf("exited (%d)", c.ExitCode)
	}
	return string(c.State)
}

// healthyState reports whether a container is where `compose up` leaves it:
// running, or, for services that are not restarted, finished successfully.
func healthyState(svc types.ServiceConfig, c api.ContainerSummary) bool {
	switch c.State {
	case container.StateRunning:
		return true
	case container.StateExited:
		oneShot := svc.Restart == "" || svc.Restart == types.RestartPolicyNo || strings.HasPrefix(svc.Restart, types.RestartPolicyOnFailure)
		return oneShot && c.ExitCode == 0
	default:
		return false
	}
}

// ErrNotFound means the Docker host has no container of the project.
var ErrNotFound = errors.New("compose project not found")

// Discover reconstructs a deployed project's spec from its mirrored files,
// which the compose labels on its containers point to. Profiles are not
// recorded on the host and come back empty.
func (e *Engine) Discover(ctx context.Context, name string) (Spec, error) {
	containers, err := e.compose.Ps(ctx, name, api.PsOptions{All: true})
	if err != nil {
		return Spec{}, fmt.Errorf("listing containers of %s: %w", name, err)
	}
	if len(containers) == 0 {
		return Spec{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	labels := containers[0].Labels
	spec := Spec{Name: name, ProjectDir: labels[api.WorkingDirLabel]}

	composePath, _, _ := strings.Cut(labels[api.ConfigFilesLabel], ",")
	content, err := e.conn.FS.ReadFile(ctx, composePath)
	if err != nil {
		return Spec{}, fmt.Errorf("reading compose file of %s: %w", name, err)
	}
	spec.Compose = string(content)

	envContent, err := e.conn.FS.ReadFile(ctx, spec.EnvPath())
	switch {
	case connection.IsNotExist(err):
	case err != nil:
		return Spec{}, fmt.Errorf("reading %s: %w", spec.EnvPath(), err)
	default:
		spec.Env, err = dotenv.UnmarshalBytesWithLookup(envContent, func(string) (string, bool) { return "", false })
		if err != nil {
			return Spec{}, fmt.Errorf("parsing %s: %w", spec.EnvPath(), err)
		}
	}
	return spec, nil
}
