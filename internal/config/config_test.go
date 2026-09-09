package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inful/dockdeps/internal/config"
)

func TestLoad_MinimalValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
forges:
  github:
    backend: github
    token: secret
    user: inful
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if got.StateDir == "" {
		t.Error("StateDir should default to non-empty")
	}
	if len(got.Forges) != 1 {
		t.Fatalf("Forges = %d, want 1", len(got.Forges))
	}
	g, ok := got.Forges["github"]
	if !ok {
		t.Fatal("missing github forge")
	}
	if g.Backend != "github" {
		t.Errorf("Backend = %q, want github", g.Backend)
	}
	if g.Token != "secret" {
		t.Errorf("Token = %q, want secret", g.Token)
	}
	if g.User != "inful" {
		t.Errorf("User = %q, want inful", g.User)
	}
}

func TestLoad_EnvInterpolation(t *testing.T) {
	t.Setenv("DOCKDEPS_TEST_TOKEN", "from-env")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
forges:
  github:
    backend: github
    token: "${DOCKDEPS_TEST_TOKEN}"
    user: inful
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Forges["github"].Token != "from-env" {
		t.Errorf("Token = %q, want from-env", got.Forges["github"].Token)
	}
}

func TestLoad_MissingEnv(t *testing.T) {
	// When a referenced env var is unset, the token should be the
	// empty string after interpolation (which then triggers a
	// validation error in the forge, but Load itself doesn't fail).
	t.Setenv("DOCKDEPS_DEFINITELY_UNSET", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
forges:
  github:
    backend: github
    token: "${DOCKDEPS_DEFINITELY_UNSET}"
    user: inful
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Forges["github"].Token != "" {
		t.Errorf("Token = %q, want empty for unset env var", got.Forges["github"].Token)
	}
}

func TestLoad_AllForges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
forges:
  github:
    backend: github
    token: ghp_xxx
    user: inful
  gitlab:
    backend: gitlab
    base_url: https://gitlab.example.com
    token: glpat_xxx
    user: inful
  forgejo:
    backend: forgejo
    base_url: https://git.example.com
    token: fk_xxx
    user: inful
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Forges) != 3 {
		t.Errorf("Forges = %d, want 3", len(got.Forges))
	}
	if got.Forges["gitlab"].BaseURL != "https://gitlab.example.com" {
		t.Errorf("gitlab.BaseURL = %q", got.Forges["gitlab"].BaseURL)
	}
	if got.Forges["forgejo"].BaseURL != "https://git.example.com" {
		t.Errorf("forgejo.BaseURL = %q", got.Forges["forgejo"].BaseURL)
	}
}

func TestLoad_Aliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
forges:
  github:
    backend: github
    token: x
    user: inful
aliases:
  - image: ghcr.io/inful/dockdeps
    source: github.com/inful/dockdeps
  - image: git.home.luguber.info/inful/x
    source: git.home.luguber.info/inful/x
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Aliases) != 2 {
		t.Fatalf("Aliases = %d, want 2", len(got.Aliases))
	}
	if got.Aliases[0].Image != "ghcr.io/inful/dockdeps" {
		t.Errorf("Aliases[0].Image = %q", got.Aliases[0].Image)
	}
	if got.Aliases[0].Source != "github.com/inful/dockdeps" {
		t.Errorf("Aliases[0].Source = %q", got.Aliases[0].Source)
	}
}

func TestLoad_Filters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
forges:
  github:
    backend: github
    token: x
    user: inful
filters:
  exclude_paths:
    - "**/node_modules/**"
    - "**/.git/**"
  max_dockerfile_size: 32768
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Filters.MaxDockerfileSize != 32768 {
		t.Errorf("MaxDockerfileSize = %d, want 32768", got.Filters.MaxDockerfileSize)
	}
	if len(got.Filters.ExcludePaths) != 2 {
		t.Errorf("ExcludePaths len = %d", len(got.Filters.ExcludePaths))
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error should mention read: %v", err)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `not: valid: yaml: at: all:`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_UnknownForgeBackend(t *testing.T) {
	// An unknown backend string is allowed at the config level (the
	// forge itself will reject it when constructed), but the
	// backend value should round-trip cleanly.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
forges:
  custom:
    backend: custom
    token: x
    user: inful
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Forges["custom"].Backend != "custom" {
		t.Errorf("Backend = %q", got.Forges["custom"].Backend)
	}
}

func TestLoad_DefaultsStateDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
forges:
  github:
    backend: github
    token: x
    user: inful
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.StateDir == "" {
		t.Error("StateDir should default to non-empty")
	}
	if !filepath.IsAbs(got.StateDir) {
		t.Errorf("StateDir should be absolute, got %q", got.StateDir)
	}
}

func TestLoad_ExplicitStateDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
version: 1
state_dir: /var/lib/dockdeps
forges:
  github:
    backend: github
    token: x
    user: inful
`)

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.StateDir != "/var/lib/dockdeps" {
		t.Errorf("StateDir = %q", got.StateDir)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
