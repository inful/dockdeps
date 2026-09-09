// Package config loads and validates the dockdeps configuration
// file.
//
// The config is YAML with ${ENV} interpolation applied to string
// values (not keys). This matches the convention used by docbuilder
// and repo-mr-file so operators familiar with those tools don't
// have to learn a new syntax.
//
// A minimal config needs only one forge block:
//
//	version: 1
//	forges:
//	  github:
//	    backend: github
//	    token: ${GITHUB_TOKEN}
//	    user: me
//
// All other fields have sensible defaults: StateDir defaults to
// ~/.local/share/dockdeps/state, Filters.MaxDockerfileSize defaults
// to 64 KiB, and Filters.ExcludePaths defaults to common noise
// directories.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure.
type Config struct {
	// Version is the schema version (always 1 in v0.1).
	Version int `yaml:"version"`

	// StateDir is the directory where dockdeps writes its state
	// files. Created automatically by the scanner on first run.
	StateDir string `yaml:"state_dir"`

	// Forges maps a forge name (e.g. "github", "gitlab-personal")
	// to its configuration. The name is local to dockdeps; the
	// Backend field tells multiforge which implementation to use.
	Forges map[string]ForgeConfig `yaml:"forges"`

	// Repos is an optional explicit list of forge-repo paths to
	// scan. When non-empty, the scanner ignores auto-discovery and
	// uses this list verbatim.
	Repos []string `yaml:"repos,omitempty"`

	// Aliases maps a registry-path image to a forge-repo source
	// that produces it. The scanner uses these to link "external"
	// images back to their producing repos.
	Aliases []Alias `yaml:"aliases,omitempty"`

	// Filters control which files are inspected during a scan.
	Filters FiltersConfig `yaml:"filters,omitempty"`
}

// ForgeConfig is the per-forge configuration block.
type ForgeConfig struct {
	// Backend selects which multiforge implementation to use:
	// "github", "gitlab", or "forgejo". Required.
	Backend string `yaml:"backend"`

	// Token is a personal access token. ${ENV} interpolation is
	// applied at Load time.
	Token string `yaml:"token"`

	// BaseURL is the API root for self-hosted forges. Empty for
	// github.com.
	BaseURL string `yaml:"base_url,omitempty"`

	// User is the forge handle whose repos should be discovered
	// during a scan. Required.
	User string `yaml:"user"`
}

// Alias links a registry image path to a forge repo that produces
// it. Used by the scanner to populate Image.LinkedProducers.
type Alias struct {
	Image  string `yaml:"image"`  // e.g. "ghcr.io/inful/dockdeps"
	Source string `yaml:"source"` // e.g. "github.com/inful/dockdeps"
}

// FiltersConfig scopes the files the scanner inspects.
type FiltersConfig struct {
	// ExcludePaths is a list of glob patterns to skip when
	// walking a repository for Dockerfiles. Matched against the
	// file's path relative to the repo root.
	ExcludePaths []string `yaml:"exclude_paths,omitempty"`

	// MaxDockerfileSize is the largest Dockerfile (in bytes) the
	// scanner will fetch. Files larger than this are skipped to
	// avoid pathological cases. Default 64 KiB.
	MaxDockerfileSize int `yaml:"max_dockerfile_size,omitempty"`
}

// Load reads, parses, and validates the config at path. ${ENV}
// interpolation is applied to all string fields at this point so
// downstream code never sees literal "${...}" markers.
//
// Load is permissive: unknown keys are ignored, missing optional
// fields fall back to defaults, and an unknown forge backend name
// is allowed (the multiforge constructor will reject it later with
// a clear error). This means Load is safe to call before
// validating individual forges.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	// Interpolate ${ENV} in the raw YAML bytes before parsing.
	// os.ExpandEnv handles both $VAR and ${VAR}; missing vars
	// become empty strings (which the forge constructor then
	// rejects).
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

// Save writes cfg back to path as YAML, atomically. The existing
// file (if any) is replaced via tempfile-then-rename so a crash
// mid-write leaves the previous good copy in place.
func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	cleanup = false
	return nil
}

// applyDefaults fills in fields the operator didn't specify. Run
// after Unmarshal so callers don't have to repeat themselves.
func applyDefaults(cfg *Config) {
	if cfg.StateDir == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "."
		}
		cfg.StateDir = filepath.Join(home, ".local", "share", "dockdeps", "state")
	}
	if cfg.Filters.MaxDockerfileSize == 0 {
		cfg.Filters.MaxDockerfileSize = 64 * 1024
	}
	if cfg.Filters.ExcludePaths == nil {
		cfg.Filters.ExcludePaths = []string{
			"**/node_modules/**",
			"**/.git/**",
			"**/vendor/**",
		}
	}
}
