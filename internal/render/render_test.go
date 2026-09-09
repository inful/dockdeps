package render_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inful/dockdeps/internal/graph"
	"github.com/inful/dockdeps/internal/render"
	"github.com/inful/dockdeps/internal/store"
)

func fixture() *store.Store {
	return &store.Store{
		Repos: []store.Repo{
			{ID: "github.com/inful/app", Owner: "inful", Name: "app", DefaultBranch: "main"},
		},
		Images: []store.Image{
			{ID: "docker.io/library/golang:1.22", Registry: "docker.io", Repository: "library/golang", Tag: "1.22"},
			{ID: "docker.io/library/alpine:3.19", Registry: "docker.io", Repository: "library/alpine", Tag: "3.19"},
		},
		Edges: []store.Edge{
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/golang:1.22"},
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/alpine:3.19"},
		},
	}
}

func TestTree_Basic(t *testing.T) {
	g := graph.New(fixture())
	tree, err := g.Tree("github.com/inful/app", 10)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}

	out := render.TreeText(tree)
	if !strings.Contains(out, "github.com/inful/app") {
		t.Errorf("output should contain app: %q", out)
	}
	if !strings.Contains(out, "docker.io/library/golang:1.22") {
		t.Errorf("output should contain golang: %q", out)
	}
	if !strings.Contains(out, "docker.io/library/alpine:3.19") {
		t.Errorf("output should contain alpine: %q", out)
	}
}

func TestTree_Indented(t *testing.T) {
	// Children should be visually distinguishable from the root.
	// The standard "tree" formatting uses connectors ("├─"/"└─")
	// rather than literal whitespace; we assert that children
	// appear below the root with a connector.
	g := graph.New(fixture())
	tree, err := g.Tree("github.com/inful/app", 10)
	if err != nil {
		t.Fatal(err)
	}
	out := render.TreeText(tree)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d:\n%s", len(lines), out)
	}
	// The root line should NOT have a connector prefix.
	if strings.HasPrefix(lines[0], "├─") || strings.HasPrefix(lines[0], "└─") {
		t.Errorf("root should not have a connector: %q", lines[0])
	}
	// Children should have a connector prefix.
	connected := 0
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "├─") || strings.HasPrefix(line, "└─") {
			connected++
		}
	}
	if connected != 2 {
		t.Errorf("expected 2 child lines with connectors, got %d:\n%s", connected, out)
	}
}

func TestTree_RepoVsImagePrefix(t *testing.T) {
	// Repo nodes should be marked with "repo:" and images with
	// "image:" so the reader can tell them apart at a glance.
	g := graph.New(fixture())
	tree, err := g.Tree("github.com/inful/app", 10)
	if err != nil {
		t.Fatal(err)
	}
	out := render.TreeText(tree)

	if !strings.Contains(out, "repo: ") {
		t.Errorf("expected 'repo: ' prefix in output, got:\n%s", out)
	}
	if !strings.Contains(out, "image: ") {
		t.Errorf("expected 'image: ' prefix in output, got:\n%s", out)
	}
}

func TestGraph_JSON(t *testing.T) {
	g := graph.New(fixture())
	out, err := render.GraphJSON(g)
	if err != nil {
		t.Fatalf("GraphJSON: %v", err)
	}

	// Output must be valid JSON.
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	// Sanity-check the high-level shape: it should have nodes,
	// edges, and at least our two known repos.
	nodes, ok := doc["nodes"].([]any)
	if !ok {
		t.Fatalf("expected nodes array, got %T", doc["nodes"])
	}
	if len(nodes) < 3 {
		t.Errorf("expected at least 3 nodes (1 repo + 2 images), got %d", len(nodes))
	}

	edges, ok := doc["edges"].([]any)
	if !ok {
		t.Fatalf("expected edges array, got %T", doc["edges"])
	}
	if len(edges) < 2 {
		t.Errorf("expected at least 2 edges, got %d", len(edges))
	}
}
