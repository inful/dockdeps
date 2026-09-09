// Package compose parses docker-compose YAML files into a structured
// representation that the scanner uses to discover image references
// and build contexts in addition to those found in Dockerfiles.
//
// The parser handles the subset of the Compose schema that affects
// the dependency graph:
//
//   - services.<name>.image       — image reference (parsed as identity.Image)
//   - services.<name>.build       — build context (string shorthand or object form)
//   - services.<name>.depends_on  — service-level dependencies
//
// Other fields (environment, ports, volumes, networks, etc.) are
// silently ignored. The parser is forgiving: unknown fields don't
// fail the parse, and missing fields fall back to zero values.
package compose

import (
	"errors"
	"fmt"

	"github.com/inful/dockdeps/internal/identity"
	"gopkg.in/yaml.v3"
)

// File is the parsed form of a docker-compose YAML file.
type File struct {
	// Services is the list of services defined in the file, in
	// the order they appear in the source. Order matters for
	// pretty-printing but not for the dependency graph.
	Services []Service
}

// Service is one entry from the `services:` map.
type Service struct {
	// Name is the key under `services:` (e.g., "web" or "api").
	Name string

	// Image is the resolved image reference for this service.
	// Empty when the service uses `build:` without an `image:`.
	Image string

	// Build is the build reference for this service. Nil when
	// the service uses `image:` only.
	Build *Build

	// DependsOn lists the names of services this service
	// depends on, in declaration order.
	DependsOn []string

	// Parameterized is true when Image contains an unresolved
	// shell or ARG substitution (e.g., "nginx:${VERSION}").
	Parameterized bool
}

// Build is a `build:` reference. It can appear in two forms in
// compose YAML: as a string (the context path) or as an object
// with at least `context` and optionally `dockerfile` and `image`.
type Build struct {
	// Context is the build context path (e.g., "." or "./api").
	Context string

	// Dockerfile is the path to the Dockerfile within the
	// build context. Empty means "use the default Dockerfile".
	Dockerfile string

	// Image is the image name to tag the build output with.
	// When set, the build produces this image; the scanner can
	// use it to attribute builds to specific images.
	Image string
}

// Parse reads a docker-compose YAML file and returns its services.
// Unknown fields are ignored; missing fields fall back to zero
// values. Returns an error when the YAML is malformed or when a
// service field has the wrong shape.
func Parse(src string) (*File, error) {
	if src == "" {
		return &File{}, nil
	}

	// Use a free-form map so we can extract just the fields we
	// care about. yaml.v3 would otherwise need a struct per
	// service shape, including nested options that we ignore.
	var raw struct {
		Services map[string]rawService `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(src), &raw); err != nil {
		return nil, fmt.Errorf("compose: parse yaml: %w", err)
	}

	out := &File{}
	for name, rs := range raw.Services {
		svc := Service{Name: name}

		// `image:` is a top-level string.
		if rs.Image != "" {
			img, err := identity.Parse(rs.Image)
			if err != nil {
				return nil, fmt.Errorf("compose: service %q image %q: %w", name, rs.Image, err)
			}
			svc.Image = img.ID()
			svc.Parameterized = img.IsParameterized()
		}

		// `build:` is either a string shorthand (e.g., `build: ./api`)
		// or an object form (e.g., `build: { context: ., dockerfile: Dockerfile }`).
		// We decode it via a yaml.Node so we can handle both shapes.
		if rs.Build.Kind != 0 {
			svc.Build = decodeBuild(rs.Build)
		}

		// `depends_on:` is a list of service names.
		svc.DependsOn = rs.DependsOn

		out.Services = append(out.Services, svc)
	}
	return out, nil
}

// rawService is the loose struct yaml.v3 unmarshals into. All
// fields are optional; absent fields stay at their zero values.
//
// Build is decoded as a yaml.Node so we can handle both the
// string-shorthand form and the object form — yaml.v3 cannot
// decode a string into a struct pointer field, so using *rawBuild
// would fail for `build: ./api`.
type rawService struct {
	Image     string    `yaml:"image"`
	Build     yaml.Node `yaml:"build"`
	DependsOn []string  `yaml:"depends_on"`
	// Everything else (environment, ports, volumes, networks,
	// labels, etc.) is intentionally absent — yaml.v3 ignores
	// fields it doesn't know about, so this struct is forward-
	// compatible with new compose schema additions.
}

// decodeBuild turns a yaml.Node from the `build:` field into a
// *Build. Supports two shapes:
//
//   - String scalar (e.g., "build: ./api") → Context is set to
//     the scalar value, Dockerfile and Image are empty.
//   - Mapping (e.g., "build: { context: ., dockerfile: Dockerfile }")
//     → each known key is extracted into the corresponding field.
//
// Unknown mapping keys (e.g., `args:`, `target:`) are silently
// ignored so we stay forward-compatible with Compose schema
// additions.
func decodeBuild(node yaml.Node) *Build {
	switch node.Kind {
	case yaml.ScalarNode:
		// String-shorthand form: `build: ./api`. The Value is
		// the raw scalar (unquoted). We treat it as Context.
		var s string
		if err := node.Decode(&s); err != nil {
			return nil
		}
		return &Build{Context: s}
	case yaml.MappingNode:
		// Object form: decode each known key into the struct,
		// ignoring unknown ones.
		var rb struct {
			Context    string `yaml:"context"`
			Dockerfile string `yaml:"dockerfile"`
			Image      string `yaml:"image"`
		}
		if err := node.Decode(&rb); err != nil {
			return nil
		}
		return &Build{
			Context:    rb.Context,
			Dockerfile: rb.Dockerfile,
			Image:      rb.Image,
		}
	default:
		return nil
	}
}

// Validate is a placeholder for future structural validation
// (e.g., rejecting services with neither image nor build). Not
// currently called; the parser returns whatever the file says.
func (f *File) Validate() error {
	if f == nil {
		return errors.New("compose: nil file")
	}
	return nil
}
