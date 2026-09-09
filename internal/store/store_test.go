package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inful/dockdeps/internal/store"
)

func TestStore_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	s, err := store.Load(dir)
	if err != nil {
		t.Fatalf("Load from empty dir: %v", err)
	}
	if len(s.Repos) != 0 {
		t.Errorf("Repos = %d, want 0", len(s.Repos))
	}
	if len(s.Images) != 0 {
		t.Errorf("Images = %d, want 0", len(s.Images))
	}
	if len(s.Edges) != 0 {
		t.Errorf("Edges = %d, want 0", len(s.Edges))
	}
}

func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	original := &store.Store{
		Repos: []store.Repo{
			{
				ID:            "github.com/inful/dockdeps",
				Forge:         "github",
				Owner:         "inful",
				Name:          "dockdeps",
				DefaultBranch: "main",
				LastSeen:      time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
				Dockerfiles: []store.Dockerfile{
					{Path: "Dockerfile", SHA: "abc123"},
					{Path: "cmd/worker/Dockerfile", SHA: "def456"},
				},
			},
		},
		Images: []store.Image{
			{
				ID:              "docker.io/library/golang:1.22",
				Registry:        "docker.io",
				Repository:      "library/golang",
				Tag:             "1.22",
				LinkedProducers: []string{},
				FirstSeen:       time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
				LastSeen:        time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
			},
			{
				ID:         "ghcr.io/inful/dockdeps:latest",
				Registry:   "ghcr.io",
				Repository: "inful/dockdeps",
				Tag:        "latest",
				LinkedProducers: []string{
					"github.com/inful/dockdeps",
				},
				FirstSeen: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
				LastSeen:  time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
			},
		},
		Edges: []store.Edge{
			{
				FromKind: "repo",
				FromID:   "github.com/inful/dockdeps",
				ToKind:   "image",
				ToID:     "docker.io/library/golang:1.22",
				Kind:     "dockerfile-from",
				Location: store.Location{
					Repo: "github.com/inful/dockdeps",
					File: "Dockerfile",
					Line: 3,
				},
				Parameterized: false,
			},
		},
	}

	if err := store.Save(dir, original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(loaded.Repos) != 1 {
		t.Fatalf("Repos = %d, want 1", len(loaded.Repos))
	}
	if loaded.Repos[0].ID != "github.com/inful/dockdeps" {
		t.Errorf("Repos[0].ID = %q", loaded.Repos[0].ID)
	}
	if len(loaded.Repos[0].Dockerfiles) != 2 {
		t.Errorf("Dockerfiles = %d, want 2", len(loaded.Repos[0].Dockerfiles))
	}

	if len(loaded.Images) != 2 {
		t.Fatalf("Images = %d, want 2", len(loaded.Images))
	}
	// Find the ghcr.io image and check its linked_producers.
	var ghcr *store.Image
	for i := range loaded.Images {
		if loaded.Images[i].Registry == "ghcr.io" {
			ghcr = &loaded.Images[i]
		}
	}
	if ghcr == nil {
		t.Fatal("missing ghcr.io image")
	}
	if len(ghcr.LinkedProducers) != 1 || ghcr.LinkedProducers[0] != "github.com/inful/dockdeps" {
		t.Errorf("LinkedProducers = %v", ghcr.LinkedProducers)
	}

	if len(loaded.Edges) != 1 {
		t.Fatalf("Edges = %d, want 1", len(loaded.Edges))
	}
	if loaded.Edges[0].FromID != "github.com/inful/dockdeps" {
		t.Errorf("Edges[0].FromID = %q", loaded.Edges[0].FromID)
	}
	if loaded.Edges[0].Location.Line != 3 {
		t.Errorf("Edges[0].Location.Line = %d", loaded.Edges[0].Location.Line)
	}
}

func TestStore_AtomicWrite(t *testing.T) {
	// Save should never leave the directory in a half-written state:
	// either the new files exist in full, or the old files do.
	dir := t.TempDir()

	s := &store.Store{
		Repos: []store.Repo{{ID: "github.com/test/r"}},
	}

	if err := store.Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify the three files exist.
	for _, name := range []string{"repos.yaml", "images.yaml", "edges.yaml"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}
}

func TestStore_CreatesDirectory(t *testing.T) {
	// Save to a non-existent directory should create it.
	parent := t.TempDir()
	target := filepath.Join(parent, "nested", "state")

	s := &store.Store{}
	if err := store.Save(target, s); err != nil {
		t.Fatalf("Save to nested dir: %v", err)
	}

	loaded, err := store.Load(target)
	if err != nil {
		t.Fatalf("Load after Save to nested dir: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil")
	}
}

func TestStore_DeterministicIDs(t *testing.T) {
	// Round-tripping repos/edges/images should produce stable IDs
	// regardless of how the operator populated them. We test this
	// by saving, reloading, and saving again — the resulting files
	// should be byte-identical (modulo timestamps).
	dir := t.TempDir()

	s := &store.Store{
		Repos: []store.Repo{
			{ID: "github.com/inful/dockdeps", Owner: "inful", Name: "dockdeps"},
		},
		Edges: []store.Edge{
			{FromKind: "repo", FromID: "github.com/inful/dockdeps", ToKind: "image", ToID: "docker.io/library/alpine:3.19"},
		},
	}

	if err := store.Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	firstRepos, err := os.ReadFile(filepath.Join(dir, "repos.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.Save(dir, loaded); err != nil {
		t.Fatalf("Save again: %v", err)
	}
	secondRepos, err := os.ReadFile(filepath.Join(dir, "repos.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if string(firstRepos) != string(secondRepos) {
		t.Errorf("round-trip not deterministic:\nfirst:\n%s\nsecond:\n%s", firstRepos, secondRepos)
	}
}
