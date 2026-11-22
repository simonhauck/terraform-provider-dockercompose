package composeops

import (
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/docker/compose/v5/pkg/api"
)

func testSpec() Spec {
	return Spec{
		Name: "demo",
		Compose: `
services:
  web:
    image: nginx:${NGINX_TAG}
    environment:
      PASSWORD: ${PASSWORD}
    volumes:
      - ./html:/usr/share/nginx/html:ro
`,
		Env:        map[string]string{"NGINX_TAG": "alpine", "PASSWORD": "s3cr$t"},
		ProjectDir: "/opt/dockercompose/demo",
	}
}

func TestLoad_interpolatesFromEnvMapOnly(t *testing.T) {
	t.Setenv("NGINX_TAG", "from-process-env")
	project, err := Load(t.Context(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	web := project.Services["web"]
	if web.Image != "nginx:alpine" {
		t.Errorf("image = %q, want nginx:alpine", web.Image)
	}
	if got := *web.Environment["PASSWORD"]; got != "s3cr$t" {
		t.Errorf("PASSWORD = %q, want s3cr$t", got)
	}
}

func TestLoad_resolvesRelativePathsAgainstProjectDir(t *testing.T) {
	project, err := Load(t.Context(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if src := project.Services["web"].Volumes[0].Source; src != "/opt/dockercompose/demo/html" {
		t.Errorf("bind source = %q, want /opt/dockercompose/demo/html", src)
	}
}

func TestLoad_setsComposeLabels(t *testing.T) {
	project, err := Load(t.Context(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	labels := project.Services["web"].CustomLabels
	want := map[string]string{
		api.ProjectLabel:     "demo",
		api.ServiceLabel:     "web",
		api.WorkingDirLabel:  "/opt/dockercompose/demo",
		api.ConfigFilesLabel: "/opt/dockercompose/demo/compose.yaml",
		api.OneoffLabel:      "False",
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}
}

func TestLoad_profiles(t *testing.T) {
	s := testSpec()
	s.Compose = `
services:
  web: {image: nginx}
  debug: {image: busybox, profiles: [debug]}
`
	project, err := Load(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := project.Services["debug"]; ok {
		t.Error("debug service enabled without its profile")
	}
	s.Profiles = []string{"debug"}
	project, err = Load(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := project.Services["debug"]; !ok {
		t.Error("debug service missing with its profile enabled")
	}
}

func TestLoad_allResources(t *testing.T) {
	s := testSpec()
	s.Compose = `
services:
  web: {image: nginx}
networks:
  unused: {driver: bridge}
volumes:
  unused:
`
	project, err := Load(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := project.Networks["unused"]; ok {
		t.Error("unused network kept without AllResources")
	}
	if _, ok := project.Volumes["unused"]; ok {
		t.Error("unused volume kept without AllResources")
	}

	s.AllResources = true
	project, err = Load(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := project.Networks["unused"]; !ok {
		t.Error("unused network missing with AllResources")
	}
	if _, ok := project.Volumes["unused"]; !ok {
		t.Error("unused volume missing with AllResources")
	}
}

func TestLoad_networksOnlyRequiresAllResources(t *testing.T) {
	s := testSpec()
	s.Compose = `
networks:
  private: {driver: bridge}
`
	_, err := Load(t.Context(), s)
	if err == nil || !strings.Contains(err.Error(), "all_resources") {
		t.Fatalf("want error mentioning all_resources, got %v", err)
	}

	s.AllResources = true
	project, err := Load(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Services) != 0 {
		t.Errorf("services = %v, want none", project.ServiceNames())
	}
	if _, ok := project.Networks["private"]; !ok {
		t.Error("private network missing with AllResources")
	}
}

func TestLoad_rejectsInvalidInput(t *testing.T) {
	tests := map[string]func(*Spec){
		"invalid yaml":     func(s *Spec) { s.Compose = "services: [" },
		"unknown property": func(s *Spec) { s.Compose = "services:\n  web:\n    image: nginx\n    imagee: x\n" },
		"relative dir":     func(s *Spec) { s.ProjectDir = "relative/dir" },
		"bad env key":      func(s *Spec) { s.Env = map[string]string{"NOT-VALID": "x"} },
		"empty name":       func(s *Spec) { s.Name = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := testSpec()
			mutate(&s)
			if _, err := Load(t.Context(), s); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

func TestEnvFile_roundTripsThroughComposeParser(t *testing.T) {
	values := map[string]string{
		"PLAIN":     "value",
		"EMPTY":     "",
		"DOLLAR":    "pa$$word ${NOT_A_VAR} $X",
		"SINGLE":    "it's",
		"DOUBLE":    `say "hi"`,
		"BACKSLASH": `C:\path\n`,
		"NEWLINE":   "line1\nline2",
		"MIXED":     "it's a \"$quote\"\\\n",
		"SPACES":    "  padded  ",
		"HASH":      "value # not a comment",
	}
	file := Spec{Env: values}.EnvFile()
	parsed, err := dotenv.UnmarshalBytesWithLookup(file, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, file)
	}
	for k, want := range values {
		if parsed[k] != want {
			t.Errorf("%s: got %q, want %q\nfile:\n%s", k, parsed[k], want, file)
		}
	}
}

func TestHash(t *testing.T) {
	a := testSpec()
	b := testSpec()
	if a.Hash() != b.Hash() {
		t.Error("equal specs hash differently")
	}
	b.Env["PASSWORD"] = "other"
	if a.Hash() == b.Hash() {
		t.Error("env change does not change hash")
	}
	b = testSpec()
	b.AllResources = true
	if a.Hash() == b.Hash() {
		t.Error("all_resources change does not change hash")
	}
	if !strings.HasPrefix(a.Hash(), a.Hash()[:8]) || len(a.Hash()) != 64 {
		t.Errorf("unexpected hash format %q", a.Hash())
	}
}
