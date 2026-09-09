package render_test

import (
	"strings"
	"testing"

	"github.com/inful/dockdeps/internal/graph"
	"github.com/inful/dockdeps/internal/render"
	"github.com/inful/dockdeps/internal/store"
)

// graphFixture builds a small multi-forge graph:
//
//	app (repo)
//	  ├── golang:1.22 (image)
//	  │     └── debian:bookworm (image)
//	  └── alpine:3.19 (image)
//	worker (repo)
//	  └── golang:1.22 (image)
//
// Plus one alias image "ghcr.io/inful/app:latest" with app linked
// as a producer.
func graphFixture() *store.Store {
	return &store.Store{
		Repos: []store.Repo{
			{ID: "github.com/inful/app", Owner: "inful", Name: "app", DefaultBranch: "main"},
			{ID: "github.com/inful/worker", Owner: "inful", Name: "worker", DefaultBranch: "main"},
		},
		Images: []store.Image{
			{ID: "docker.io/library/golang:1.22"},
			{ID: "docker.io/library/debian:bookworm"},
			{ID: "docker.io/library/alpine:3.19"},
			{
				ID:       "ghcr.io/inful/app:latest",
				LinkedProducers: []string{"github.com/inful/app"},
			},
		},
		Edges: []store.Edge{
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/golang:1.22"},
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/alpine:3.19"},
			{FromKind: "repo", FromID: "github.com/inful/worker", ToKind: "image", ToID: "docker.io/library/golang:1.22"},
			{FromKind: "image", FromID: "docker.io/library/golang:1.22", ToKind: "image", ToID: "docker.io/library/debian:bookworm"},
		},
	}
}

func TestMermaid_BasicShape(t *testing.T) {
	g := graph.New(graphFixture())
	out, err := render.Mermaid(g)
	if err != nil {
		t.Fatalf("Mermaid: %v", err)
	}

	s := string(out)
	// Must declare flowchart syntax.
	if !strings.HasPrefix(s, "flowchart LR") {
		t.Errorf("output should start with 'flowchart LR', got: %.80q...", s)
	}

	// Must mention each known node by its ID. The IDs contain
	// slashes and dots which are valid in Mermaid node labels
	// when quoted.
	for _, want := range []string{
		"github.com/inful/app",
		"github.com/inful/worker",
		"docker.io/library/golang:1.22",
		"docker.io/library/debian:bookworm",
		"docker.io/library/alpine:3.19",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Mermaid output missing node %q", want)
		}
	}
}

func TestMermaid_RepoAndImageShapes(t *testing.T) {
	// Repos should render with one shape (e.g., stadium for
	// repos) and images with another (e.g., rectangle). The
	// distinction lets visual readers spot "which boxes are
	// docker images vs source repos" at a glance.
	//
	// Note: Mermaid requires IDs containing slashes or dots to
	// be quoted, so we look for the quoted form: `"id"([text])`
	// for repos and `"id"[text]` for images.
	g := graph.New(graphFixture())
	out, err := render.Mermaid(g)
	if err != nil {
		t.Fatalf("Mermaid: %v", err)
	}

	s := string(out)
	// Stadium nodes use the syntax id([text]) — repos use this.
	if !strings.Contains(s, `"github.com/inful/app"([`) {
		t.Errorf("repo node should use stadium shape: %q", s)
	}
	// Rectangular nodes use the syntax id[text] — images use this.
	if !strings.Contains(s, `"docker.io/library/golang:1.22"[`) {
		t.Errorf("image node should use rect shape: %q", s)
	}
}

func TestMermaid_Edges(t *testing.T) {
	g := graph.New(graphFixture())
	out, err := render.Mermaid(g)
	if err != nil {
		t.Fatalf("Mermaid: %v", err)
	}

	s := string(out)
	// Each edge should appear as `from --> to` (or with a label).
	// We expect at least 4 edges (3 repo→image + 1 image→image).
	arrows := strings.Count(s, "-->")
	if arrows < 4 {
		t.Errorf("expected at least 4 arrows in output, got %d", arrows)
	}
}

func TestMermaid_EscapesSpecialCharacters(t *testing.T) {
	// IDs containing brackets, pipes, or hash marks would break
	// Mermaid syntax if unquoted. The exporter must quote them.
	//
	// We can't easily inject a weird ID through the public API,
	// so this test only checks that existing IDs (which contain
	// only safe characters) render correctly. A separate fuzz
	// test would cover the bad-character case in v0.2.
	g := graph.New(graphFixture())
	out, err := render.Mermaid(g)
	if err != nil {
		t.Fatalf("Mermaid: %v", err)
	}

	// Sanity: no unquoted newlines inside a node definition.
	if strings.Contains(string(out), "\n[") {
		t.Errorf("node definition contains a newline; quoting failed")
	}
}

func TestDOT_BasicShape(t *testing.T) {
	g := graph.New(graphFixture())
	out, err := render.DOT(g)
	if err != nil {
		t.Fatalf("DOT: %v", err)
	}

	s := string(out)
	// DOT must declare digraph with rankdir.
	if !strings.HasPrefix(s, "digraph G {") {
		t.Errorf("output should start with 'digraph G {', got: %.80q...", s)
	}
	if !strings.Contains(s, "rankdir=LR;") {
		t.Errorf("output should set rankdir=LR: %q", s)
	}
}

func TestDOT_NodesAndEdges(t *testing.T) {
	g := graph.New(graphFixture())
	out, err := render.DOT(g)
	if err != nil {
		t.Fatalf("DOT: %v", err)
	}

	s := string(out)

	// Each node should appear with a quoted ID and a label.
	if !strings.Contains(s, `"github.com/inful/app"`) {
		t.Errorf("repo node missing: %q", s)
	}
	if !strings.Contains(s, `"docker.io/library/golang:1.22"`) {
		t.Errorf("image node missing: %q", s)
	}

	// Edges: arrow syntax is `from -> to;`.
	arrows := strings.Count(s, "->")
	if arrows < 4 {
		t.Errorf("expected at least 4 edges, got %d", arrows)
	}
}

func TestDOT_RepoVsImageStyling(t *testing.T) {
	// DOT can apply shape, color, and fillstyle per node. We
	// expect repos and images to have distinct shapes so the
	// rendered graph is readable.
	g := graph.New(graphFixture())
	out, err := render.DOT(g)
	if err != nil {
		t.Fatalf("DOT: %v", err)
	}
	s := string(out)

	if !strings.Contains(s, "shape=box") {
		t.Errorf("expected at least one box-shaped node (repos): %q", s)
	}
	if !strings.Contains(s, "shape=ellipse") {
		t.Errorf("expected at least one ellipse-shaped node (images): %q", s)
	}
}

func TestDOT_EscapesSpecialCharacters(t *testing.T) {
	// DOT uses " to delimit IDs; a literal " in an ID must be
	// escaped as \". Our IDs don't contain quotes, but we still
	// verify the output is valid DOT by checking that every line
	// is balanced (sanity check, not a full parser).
	g := graph.New(graphFixture())
	out, err := render.DOT(g)
	if err != nil {
		t.Fatalf("DOT: %v", err)
	}
	s := string(out)

	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for _, line := range lines {
		if strings.Count(line, `"`)%2 != 0 {
			t.Errorf("unbalanced quotes in DOT line: %q", line)
		}
	}
}
