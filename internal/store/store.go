// Package store persists dockdeps' state to YAML files on disk.
//
// State is split across three files for clarity:
//
//   - repos.yaml  — one entry per source repository discovered
//   - images.yaml — one entry per image identity observed
//   - edges.yaml  — flat list of dependency edges
//
// The three files are written atomically: each goes through a
// tempfile-then-rename so a crash mid-write leaves the previous
// good copy in place rather than a half-written file that would
// fail to parse on the next run.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Repo is one source repository discovered during a scan.
type Repo struct {
	ID            string       `yaml:"id"`             // "github.com/inful/dockdeps"
	Forge         string       `yaml:"forge"`          // "github" | "gitlab" | "forgejo"
	Owner         string       `yaml:"owner"`          // "inful"
	Name          string       `yaml:"name"`           // "dockdeps"
	DefaultBranch string       `yaml:"default_branch"` // "main"
	LastSeen      time.Time    `yaml:"last_seen"`      // when we last scanned this repo
	Dockerfiles   []Dockerfile `yaml:"dockerfiles,omitempty"`
}

// Dockerfile is one Dockerfile we found inside a Repo.
type Dockerfile struct {
	Path string `yaml:"path"` // relative to repo root
	SHA  string `yaml:"sha"`  // blob SHA from the forge (when available)
}

// Image is one image identity observed in a FROM line or an alias.
type Image struct {
	ID              string    `yaml:"id"`                         // canonical ID (registry/repo:tag or @digest)
	Registry        string    `yaml:"registry"`                   // "docker.io", "ghcr.io", ...
	Repository      string    `yaml:"repository"`                 // "library/golang", "inful/dockdeps"
	Tag             string    `yaml:"tag"`                        // "1.22", "latest", ...
	Digest          string    `yaml:"digest,omitempty"`           // "sha256:..." when digest-pinned
	LinkedProducers []string  `yaml:"linked_producers,omitempty"` // forge-repo IDs that produce this image
	FirstSeen       time.Time `yaml:"first_seen"`
	LastSeen        time.Time `yaml:"last_seen"`
}

// Edge is one dependency edge in the graph. Edges are directed: from
// a Repo or Image to an Image. (Image → Image edges arise from one
// image's Dockerfile depending on another; Repo → Image edges arise
// from a Repo whose Dockerfile depends on an image.)
type Edge struct {
	FromKind      string   `yaml:"from_kind"` // "repo" | "image"
	FromID        string   `yaml:"from_id"`   // Repo.ID or Image.ID
	ToKind        string   `yaml:"to_kind"`   // "image"
	ToID          string   `yaml:"to_id"`     // Image.ID
	Kind          string   `yaml:"kind"`      // "dockerfile-from" | "compose-image" | "compose-build" | "alias-produces"
	Location      Location `yaml:"location,omitempty"`
	Parameterized bool     `yaml:"parameterized,omitempty"`
}

// Location pins an edge to a specific line in a specific file in a
// specific repo. Empty fields are omitted from the YAML.
type Location struct {
	Repo string `yaml:"repo,omitempty"`
	File string `yaml:"file,omitempty"`
	Line int    `yaml:"line,omitempty"`
}

// Store is the in-memory representation of all persisted state.
type Store struct {
	Repos  []Repo  `yaml:"repos"`
	Images []Image `yaml:"images"`
	Edges  []Edge  `yaml:"edges"`
}

// Load reads the three state files from dir. Missing files are
// treated as empty (so the first run on a fresh machine just gets
// an empty store). Corrupt YAML returns an error.
func Load(dir string) (*Store, error) {
	out := &Store{}

	// Helper: read+unmarshal a single file, treating a missing
	// file as an empty contribution.
	load := func(name string, dst any) error {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("store: read %s: %w", path, err)
		}
		if len(data) == 0 {
			return nil
		}
		if err := yaml.Unmarshal(data, dst); err != nil {
			return fmt.Errorf("store: parse %s: %w", path, err)
		}
		return nil
	}

	if err := load("repos.yaml", &out.Repos); err != nil {
		return nil, err
	}
	if err := load("images.yaml", &out.Images); err != nil {
		return nil, err
	}
	if err := load("edges.yaml", &out.Edges); err != nil {
		return nil, err
	}
	return out, nil
}

// Save writes the store to dir atomically. Each of the three files
// is written via a tempfile-then-rename so a crash mid-write leaves
// the previous good copy intact.
func Save(dir string, s *Store) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	if err := saveFile(dir, "repos.yaml", s.Repos); err != nil {
		return err
	}
	if err := saveFile(dir, "images.yaml", s.Images); err != nil {
		return err
	}
	if err := saveFile(dir, "edges.yaml", s.Edges); err != nil {
		return err
	}
	return nil
}

// saveFile writes v to dir/name atomically: encode to YAML, write
// to a tempfile in the same directory, then rename over the target.
func saveFile(dir, name string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: marshal %s: %w", name, err)
	}
	target := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return fmt.Errorf("store: create temp for %s: %w", name, err)
	}
	tmpName := tmp.Name()
	// Ensure we clean up on any failure path.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close %s: %w", name, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("store: rename %s: %w", name, err)
	}
	cleanup = false
	return nil
}
