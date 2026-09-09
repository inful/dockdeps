package render_test

import (
	"strings"
	"testing"

	"github.com/inful/dockdeps/internal/graph"
	"github.com/inful/dockdeps/internal/render"
)

func TestHTML_SelfContained(t *testing.T) {
	g := graph.New(graphFixture())
	out, err := render.HTML(g)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}

	s := string(out)

	// Must be a complete HTML document — no external resources,
	// no relative paths. Operators should be able to email the
	// file or commit it to a repo and have it work standalone.
	if !strings.HasPrefix(s, "<!DOCTYPE html>") {
		t.Errorf("output should start with <!DOCTYPE html>: %.80q...", s)
	}
	if !strings.Contains(s, "<html") {
		t.Error("missing <html> element")
	}
	if !strings.Contains(s, "</html>") {
		t.Error("missing </html> close tag")
	}

	// No external script or stylesheet references — the report
	// is self-contained. We allow inline <script> tags but not
	// <script src="..."> tags.
	if strings.Contains(s, `<script src=`) {
		t.Error("output references an external script; HTML must be self-contained")
	}
	if strings.Contains(s, `<link rel="stylesheet"`) {
		t.Error("output references an external stylesheet; HTML must be self-contained")
	}
}

func TestHTML_ContainsGraphData(t *testing.T) {
	g := graph.New(graphFixture())
	out, err := render.HTML(g)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}

	s := string(out)
	// Each node ID must appear in the embedded data. The renderer
	// typically embeds the graph as JSON in a <script> tag.
	for _, want := range []string{
		"github.com/inful/app",
		"github.com/inful/worker",
		"docker.io/library/golang:1.22",
		"docker.io/library/debian:bookworm",
		"docker.io/library/alpine:3.19",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("HTML missing node %q", want)
		}
	}
}

func TestHTML_ContainsInlineSVG(t *testing.T) {
	// The HTML page renders the graph as inline SVG rather than
	// via JavaScript. SVG works in any browser, in Markdown
	// renderers (GitHub, GitLab), and in PDF conversion tools —
	// no scripts required.
	g := graph.New(graphFixture())
	out, err := render.HTML(g)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "<svg") {
		t.Error("expected inline <svg> element")
	}
	if !strings.Contains(s, "</svg>") {
		t.Error("missing </svg> close tag")
	}
	// The SVG should declare the xmlns so it renders standalone
	// (some renderers require this for self-contained SVG).
	if !strings.Contains(s, `xmlns="http://www.w3.org/2000/svg"`) {
		t.Error("SVG missing xmlns declaration")
	}
}

func TestHTML_SVGContainsNodesAndEdges(t *testing.T) {
	// Each known node ID should appear as text inside the SVG,
	// and edges should be drawn as <path> elements.
	g := graph.New(graphFixture())
	out, err := render.HTML(g)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}

	s := string(out)
	for _, want := range []string{
		"github.com/inful/app",
		"github.com/inful/worker",
		"docker.io/library/golang:1.22",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("SVG missing node %q", want)
		}
	}

	// Edges: count path elements (excluding the marker-end
	// arrowhead defs). We expect at least 4 edges.
	pathCount := strings.Count(s, "<path")
	// Subtract 1 for the marker-end arrowhead def path.
	if pathCount-1 < 4 {
		t.Errorf("expected at least 4 edge paths, got %d (after marker def)", pathCount-1)
	}
}
