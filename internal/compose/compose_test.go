package compose_test

import (
	"strings"
	"testing"

	"github.com/inful/dockdeps/internal/compose"
)

func TestParse_MinimalService(t *testing.T) {
	src := `services:
  web:
    image: nginx:1.27
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(got.Services))
	}
	svc := got.Services[0]
	if svc.Name != "web" {
		t.Errorf("Name = %q, want web", svc.Name)
	}
	// Images are normalised to include the registry so the
	// dependency graph can dedupe across "alpine" and
	// "docker.io/library/alpine:latest". "nginx:1.27" becomes
	// "docker.io/library/nginx:1.27".
	if svc.Image != "docker.io/library/nginx:1.27" {
		t.Errorf("Image = %q, want docker.io/library/nginx:1.27", svc.Image)
	}
	if svc.Build != nil {
		t.Errorf("Build = %+v, want nil", svc.Build)
	}
}

func TestParse_ServiceWithBuild(t *testing.T) {
	src := `services:
  api:
    build:
      context: .
      dockerfile: Dockerfile.api
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Services) != 1 {
		t.Fatalf("len = %d", len(got.Services))
	}
	svc := got.Services[0]
	if svc.Image != "" {
		t.Errorf("Image = %q, want empty", svc.Image)
	}
	if svc.Build == nil {
		t.Fatal("Build is nil; expected a build reference")
	}
	if svc.Build.Context != "." {
		t.Errorf("Build.Context = %q", svc.Build.Context)
	}
	if svc.Build.Dockerfile != "Dockerfile.api" {
		t.Errorf("Build.Dockerfile = %q", svc.Build.Dockerfile)
	}
}

func TestParse_ServiceWithShortBuild(t *testing.T) {
	// "build: ./api" is the shorthand form.
	src := `services:
  api:
    build: ./api
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := got.Services[0]
	if svc.Build == nil {
		t.Fatal("Build is nil; expected a build reference")
	}
	if svc.Build.Context != "./api" {
		t.Errorf("Build.Context = %q, want ./api", svc.Build.Context)
	}
	if svc.Build.Dockerfile != "" {
		t.Errorf("Build.Dockerfile = %q, want empty (uses repo default)", svc.Build.Dockerfile)
	}
}

func TestParse_DependsOn(t *testing.T) {
	src := `services:
  web:
    image: nginx:1.27
  api:
    image: myapi:latest
    depends_on:
      - db
      - web
  db:
    image: postgres:16
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	api := findService(got, "api")
	if api == nil {
		t.Fatal("missing api service")
	}
	wantDeps := []string{"db", "web"}
	if !equalStrings(api.DependsOn, wantDeps) {
		t.Errorf("DependsOn = %v, want %v", api.DependsOn, wantDeps)
	}
}

func TestParse_ImageWithVariable(t *testing.T) {
	src := `services:
  web:
    image: nginx:${VERSION}
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := got.Services[0]
	if !svc.Parameterized {
		t.Error("expected Parameterized=true for variable substitution")
	}
}

func TestParse_MultipleServices(t *testing.T) {
	src := `services:
  web:
    image: nginx:1.27
  api:
    image: myapi:v1
  worker:
    image: myworker:v1
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Services) != 3 {
		t.Errorf("Services = %d, want 3", len(got.Services))
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	src := `services: web: this is not: valid yaml at all:`
	_, err := compose.Parse(src)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestParse_EmptyInput(t *testing.T) {
	got, err := compose.Parse("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Services) != 0 {
		t.Errorf("Services = %d, want 0", len(got.Services))
	}
}

func TestParse_NoServicesKey(t *testing.T) {
	// A compose file with only a `version:` key has no services.
	src := `version: "3.9"
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Services) != 0 {
		t.Errorf("Services = %d, want 0", len(got.Services))
	}
}

func TestParse_BuildContextWithImageIsMixed(t *testing.T) {
	// Real-world: a service can have BOTH `image:` and `build:`.
	// The image is what gets pushed; the build is how it's built.
	src := `services:
  api:
    image: ghcr.io/me/api:v1
    build:
      context: .
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := got.Services[0]
	if svc.Image != "ghcr.io/me/api:v1" {
		t.Errorf("Image = %q", svc.Image)
	}
	if svc.Build == nil {
		t.Fatal("Build is nil")
	}
	if svc.Build.Context != "." {
		t.Errorf("Build.Context = %q", svc.Build.Context)
	}
}

func TestParse_UnknownFieldIgnored(t *testing.T) {
	// Compose has many fields we don't care about (environment,
	// ports, volumes, networks, ...). The parser must ignore
	// them silently rather than failing.
	src := `services:
  web:
    image: nginx:1.27
    environment:
      - LOG_LEVEL=info
    ports:
      - "8080:80"
    volumes:
      - ./data:/data
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := got.Services[0]
	if svc.Image != "docker.io/library/nginx:1.27" {
		t.Errorf("Image = %q", svc.Image)
	}
}

func TestParse_BuildImageField(t *testing.T) {
	// Some operators use `build: { image: ... }` (Compose
	// BuildKit spec) to declare an image name for the build
	// output. We capture this so the scanner can attribute
	// builds to images.
	src := `services:
  api:
    build:
      context: .
      image: ghcr.io/me/api:dev
`
	got, err := compose.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := got.Services[0]
	if svc.Build == nil {
		t.Fatal("Build is nil")
	}
	if svc.Build.Image != "ghcr.io/me/api:dev" {
		t.Errorf("Build.Image = %q", svc.Build.Image)
	}
}

func TestParse_ErrorContainsServiceName(t *testing.T) {
	// When parsing fails at the image-parse stage (i.e., the
	// YAML is structurally valid but identity.Parse rejects the
	// image value), the error message should identify the
	// offending service so the operator can find it.
	//
	// "image: foo@" passes YAML parsing but identity.Parse
	// rejects it because the digest after @ is empty.
	src := `services:
  web:
    image: nginx:1.27
  api:
    image: "foo@"
`
	_, err := compose.Parse(src)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("error should mention service name 'api': %v", err)
	}
}

// findService returns a pointer to the named service, or nil if
// not found.
func findService(got *compose.File, name string) *compose.Service {
	for i := range got.Services {
		if got.Services[i].Name == name {
			return &got.Services[i]
		}
	}
	return nil
}

// equalStrings compares two string slices for element equality
// ignoring order (the YAML order isn't guaranteed by the parser).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, c := range seen {
		if c != 0 {
			return false
		}
	}
	return true
}
