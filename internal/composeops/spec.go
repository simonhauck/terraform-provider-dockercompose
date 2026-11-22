// Package composeops deploys a compose project described entirely in memory
// to a Docker host, using the compose SDK in-process.
package composeops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
)

const (
	// ComposeFileName is the name of the compose file inside the project directory.
	ComposeFileName = "compose.yaml"
	// EnvFileName is the name of the interpolation file inside the project directory.
	EnvFileName = ".env"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Spec is everything that defines a deployed compose project.
type Spec struct {
	// Name is the compose project name.
	Name string
	// Compose is the compose file content (YAML or JSON).
	Compose string
	// Env holds the interpolation variables, the in-memory equivalent of a .env file.
	Env map[string]string
	// Profiles are the compose profiles to enable.
	Profiles []string
	// AllResources keeps networks, volumes and other resources that no service
	// references, like `docker compose --all-resources`.
	AllResources bool
	// ProjectDir is the absolute, slash-separated project directory on the Docker host.
	// Relative paths in the compose file resolve against it.
	ProjectDir string
}

// ComposePath is where the compose file lives on the Docker host.
func (s Spec) ComposePath() string { return path.Join(s.ProjectDir, ComposeFileName) }

// EnvPath is where the .env file lives on the Docker host.
func (s Spec) EnvPath() string { return path.Join(s.ProjectDir, EnvFileName) }

// Validate checks the parts of a spec that compose itself would not reject clearly.
func (s Spec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("project name must not be empty")
	}
	if !path.IsAbs(s.ProjectDir) {
		return fmt.Errorf("project directory %q must be an absolute path", s.ProjectDir)
	}
	for key := range s.Env {
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid env variable name %q: use letters, digits and underscores, not starting with a digit", key)
		}
	}
	return nil
}

// Hash identifies the spec's inputs. Two specs with the same hash deploy the same project.
func (s Spec) Hash() string {
	input := struct {
		Name         string
		Compose      string
		Env          [][2]string
		Profiles     []string
		AllResources bool
		ProjectDir   string
	}{s.Name, s.Compose, sortedEnv(s.Env), s.Profiles, s.AllResources, s.ProjectDir}
	b, err := json.Marshal(input)
	if err != nil {
		panic(err) // only strings: cannot fail
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// EnvFile renders Env in .env syntax that compose parses back to the same values.
func (s Spec) EnvFile() []byte {
	var b strings.Builder
	for _, kv := range sortedEnv(s.Env) {
		b.WriteString(kv[0])
		b.WriteByte('=')
		b.WriteString(quoteEnvValue(kv[1]))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// quoteEnvValue single-quotes a value (taken literally by compose) when it can,
// and falls back to a double-quoted string with escapes otherwise.
func quoteEnvValue(v string) string {
	if !strings.ContainsAny(v, "'\n\r") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(v) + `"`
}

func sortedEnv(env map[string]string) [][2]string {
	out := make([][2]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, [2]string{k, env[k]})
	}
	return out
}

// Load parses and validates the spec into a compose project, without touching
// the file system or the process environment.
func Load(ctx context.Context, s Spec) (*types.Project, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	details := types.ConfigDetails{
		WorkingDir: s.ProjectDir,
		ConfigFiles: []types.ConfigFile{{
			Filename: s.ComposePath(),
			Content:  []byte(s.Compose),
		}},
		Environment: types.Mapping(maps.Clone(s.Env)),
	}
	project, err := loader.LoadWithContext(ctx, details, func(o *loader.Options) {
		o.SetProjectName(s.Name, true)
		o.Profiles = s.Profiles
		o.ResolvePaths = true
		o.SkipInclude = true
		o.ResourceLoaders = nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading compose project: %w", err)
	}
	if project.Name == "" {
		project.Name = s.Name
	}
	if !s.AllResources && len(project.Services) == 0 && len(project.Jobs) == 0 {
		return nil, fmt.Errorf("compose project %q has no active services; set all_resources = true to manage resources that are not used by services", s.Name)
	}

	// The labels compose's own loader adds. They let compose find the project's
	// containers, and point the docker CLI at the mirrored files on the host.
	for name, svc := range project.Services {
		svc.CustomLabels = map[string]string{
			api.ProjectLabel:     project.Name,
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  s.ProjectDir,
			api.ConfigFilesLabel: s.ComposePath(),
			api.OneoffLabel:      "False",
		}
		project.Services[name] = svc
	}
	if !s.AllResources {
		project = project.WithoutUnnecessaryResources()
	}
	return project, nil
}
