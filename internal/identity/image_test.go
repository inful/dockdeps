package identity_test

import (
	"testing"

	"github.com/inful/dockdeps/internal/identity"
)

// TestParse covers the canonical cases for image-reference parsing.
// Each case asserts the four parsed components: registry,
// repository, tag, and digest. The "want ID" sub-assertion confirms
// the stable identifier the graph uses.
func TestParse(t *testing.T) {
	tests := []struct {
		name           string
		in             string
		wantRegistry   string
		wantRepository string
		wantTag        string
		wantDigest     string
		wantID         string
		wantErr        bool
	}{
		{
			name:           "plain name defaults to docker.io library",
			in:             "alpine",
			wantRegistry:   "docker.io",
			wantRepository: "library/alpine",
			wantTag:        "latest",
			wantID:         "docker.io/library/alpine:latest",
		},
		{
			name:           "name with explicit tag",
			in:             "alpine:3.19",
			wantRegistry:   "docker.io",
			wantRepository: "library/alpine",
			wantTag:        "3.19",
			wantID:         "docker.io/library/alpine:3.19",
		},
		{
			name:           "namespaced name defaults to docker.io",
			in:             "inful/dockdeps",
			wantRegistry:   "docker.io",
			wantRepository: "inful/dockdeps",
			wantTag:        "latest",
			wantID:         "docker.io/inful/dockdeps:latest",
		},
		{
			name:           "explicit registry",
			in:             "ghcr.io/inful/dockdeps:dev",
			wantRegistry:   "ghcr.io",
			wantRepository: "inful/dockdeps",
			wantTag:        "dev",
			wantID:         "ghcr.io/inful/dockdeps:dev",
		},
		{
			name:           "registry with port",
			in:             "registry.gitlab.com:443/inful/some-project:v1",
			wantRegistry:   "registry.gitlab.com:443",
			wantRepository: "inful/some-project",
			wantTag:        "v1",
			wantID:         "registry.gitlab.com:443/inful/some-project:v1",
		},
		{
			name:           "digest form",
			in:             "alpine@sha256:abc123",
			wantRegistry:   "docker.io",
			wantRepository: "library/alpine",
			wantDigest:     "sha256:abc123",
			wantID:         "docker.io/library/alpine@sha256:abc123",
		},
		{
			name:           "scratch is its own registry",
			in:             "scratch",
			wantRegistry:   "",
			wantRepository: "scratch",
			wantTag:        "",
			wantID:         "scratch",
		},
		{
			name:           "parameterized image contains ARG",
			in:             "golang:${VERSION}",
			wantRegistry:   "docker.io",
			wantRepository: "library/golang",
			wantTag:        "${VERSION}",
			wantID:         "docker.io/library/golang:${VERSION}",
		},
		{
			name:           "parameterized with explicit registry",
			in:             "ghcr.io/me/app:${TAG}",
			wantRegistry:   "ghcr.io",
			wantRepository: "me/app",
			wantTag:        "${TAG}",
			wantID:         "ghcr.io/me/app:${TAG}",
		},
		{
			name:    "empty input is rejected",
			in:      "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := identity.Parse(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) error = nil, want non-nil", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.in, err)
			}
			if got.Registry != tt.wantRegistry {
				t.Errorf("Registry = %q, want %q", got.Registry, tt.wantRegistry)
			}
			if got.Repository != tt.wantRepository {
				t.Errorf("Repository = %q, want %q", got.Repository, tt.wantRepository)
			}
			if got.Tag != tt.wantTag {
				t.Errorf("Tag = %q, want %q", got.Tag, tt.wantTag)
			}
			if got.Digest != tt.wantDigest {
				t.Errorf("Digest = %q, want %q", got.Digest, tt.wantDigest)
			}
			if got.ID() != tt.wantID {
				t.Errorf("ID() = %q, want %q", got.ID(), tt.wantID)
			}
		})
	}
}

// TestIsParameterized verifies the heuristic used by the scanner to
// flag FROM statements whose image reference can't be resolved
// statically (because of an ARG or shell variable).
func TestIsParameterized(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"alpine:3.19", false},
		{"golang:1.22", false},
		{"golang:${VERSION}", true},
		{"ghcr.io/me/app:${TAG}", true},
		{"alpine:${VERSION:-3.19}", true}, // bash-style default
		{"alpine:$VERSION", true},         // bare-dollar form
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			img, err := identity.Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.in, err)
			}
			if got := img.IsParameterized(); got != tt.want {
				t.Errorf("IsParameterized() = %v, want %v", got, tt.want)
			}
		})
	}
}
