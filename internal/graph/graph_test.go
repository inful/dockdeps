package graph_test

import (
	"testing"

	"github.com/inful/dockdeps/internal/graph"
	"github.com/inful/dockdeps/internal/store"
)

// fixture builds a small graph:
//
//	app (repo)
//	  ├── golang:1.22 (image)
//	  │     └── debian:bookworm (image)
//	  └── alpine:3.19 (image)
//	worker (repo)
//	  └── golang:1.22 (image)
//
// Plus one alias: "ghcr.io/inful/app:latest" → app.
func fixture() *store.Store {
	return &store.Store{
		Repos: []store.Repo{
			{ID: "github.com/inful/app", Owner: "inful", Name: "app", DefaultBranch: "main"},
			{ID: "github.com/inful/worker", Owner: "inful", Name: "worker", DefaultBranch: "main"},
		},
		Images: []store.Image{
			{ID: "docker.io/library/golang:1.22", Registry: "docker.io", Repository: "library/golang", Tag: "1.22"},
			{ID: "docker.io/library/debian:bookworm", Registry: "docker.io", Repository: "library/debian", Tag: "bookworm"},
			{ID: "docker.io/library/alpine:3.19", Registry: "docker.io", Repository: "library/alpine", Tag: "3.19"},
			{ID: "ghcr.io/inful/app:latest", Registry: "ghcr.io", Repository: "inful/app", Tag: "latest"},
		},
		Edges: []store.Edge{
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/golang:1.22"},
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/alpine:3.19"},
			{FromKind: "repo", FromID: "github.com/inful/worker", ToKind: "image", ToID: "docker.io/library/golang:1.22"},
			{FromKind: "image", FromID: "docker.io/library/golang:1.22", ToKind: "image", ToID: "docker.io/library/debian:bookworm"},
		},
	}
}

func TestGraph_Dependencies_OfRepo(t *testing.T) {
	g := graph.New(fixture())
	deps, err := g.Dependencies("github.com/inful/app")
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	// app depends directly on golang:1.22 and alpine:3.19; transitively
	// on debian:bookworm (via golang).
	ids := []string{}
	for _, d := range deps {
		ids = append(ids, d.ID)
	}
	wantContains := []string{
		"docker.io/library/golang:1.22",
		"docker.io/library/alpine:3.19",
		"docker.io/library/debian:bookworm",
	}
	for _, want := range wantContains {
		if !contains(ids, want) {
			t.Errorf("Dependencies of app missing %q (got %v)", want, ids)
		}
	}
}

func TestGraph_Dependencies_OfImage(t *testing.T) {
	g := graph.New(fixture())
	deps, err := g.Dependencies("docker.io/library/golang:1.22")
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(deps))
	}
	if deps[0].ID != "docker.io/library/debian:bookworm" {
		t.Errorf("dep = %q, want debian:bookworm", deps[0].ID)
	}
}

func TestGraph_Dependents_OfImage(t *testing.T) {
	g := graph.New(fixture())
	deps, err := g.Dependents("docker.io/library/golang:1.22")
	if err != nil {
		t.Fatalf("Dependents: %v", err)
	}
	// app and worker both depend on golang:1.22 directly.
	repoIDs := []string{}
	for _, d := range deps {
		repoIDs = append(repoIDs, d.ID)
	}
	wantContains := []string{"github.com/inful/app", "github.com/inful/worker"}
	for _, want := range wantContains {
		if !contains(repoIDs, want) {
			t.Errorf("Dependents of golang missing %q (got %v)", want, repoIDs)
		}
	}
}

func TestGraph_Tree_OfRepo(t *testing.T) {
	g := graph.New(fixture())
	tree, err := g.Tree("github.com/inful/app", 10)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if tree == nil {
		t.Fatal("Tree returned nil")
	}
	if tree.ID != "github.com/inful/app" {
		t.Errorf("root ID = %q", tree.ID)
	}
	if len(tree.Children) != 2 {
		t.Errorf("expected 2 children of app, got %d", len(tree.Children))
	}
}

func TestGraph_Tree_MaxDepth(t *testing.T) {
	g := graph.New(fixture())
	tree, err := g.Tree("docker.io/library/golang:1.22", 0)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(tree.Children) != 0 {
		t.Errorf("depth 0 should produce no children, got %d", len(tree.Children))
	}
}

func TestGraph_UnknownNode(t *testing.T) {
	g := graph.New(fixture())
	_, err := g.Dependencies("github.com/inful/nonexistent")
	if err == nil {
		t.Error("expected error for unknown node")
	}
	_, err = g.Dependents("docker.io/library/nonexistent:latest")
	if err == nil {
		t.Error("expected error for unknown node")
	}
}

func TestGraph_OrphanImages(t *testing.T) {
	g := graph.New(fixture())
	// scratch has no producer and no dependents in our fixture, but
	// we'd add it via the scanner. Here we just test the helper:
	orphans := g.OrphanImages()
	for _, img := range orphans {
		if len(img.LinkedProducers) > 0 {
			t.Errorf("orphan %s has linked_producers", img.ID)
		}
	}
}

func TestGraph_KnownNodes(t *testing.T) {
	g := graph.New(fixture())
	if !g.HasNode("github.com/inful/app") {
		t.Error("HasNode should return true for known repo")
	}
	if !g.HasNode("docker.io/library/golang:1.22") {
		t.Error("HasNode should return true for known image")
	}
	if g.HasNode("github.com/inful/nonexistent") {
		t.Error("HasNode should return false for unknown node")
	}
}

// contains is a small helper for string-slice membership tests.
func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
