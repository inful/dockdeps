// Package render turns graph structures into human- or machine-
// readable text. It currently supports two output forms:
//
//   - TreeText: an indented tree suitable for terminals.
//   - GraphJSON: a structured JSON export suitable for downstream
//     tooling (Mermaid converters, custom dashboards).
//
// Renderers are pure functions: they take a graph/tree and write to
// a writer. They never touch the network, filesystem, or config.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/inful/dockdeps/internal/graph"
)

// TreeText writes a human-readable indented tree. Each node is
// prefixed with a kind marker ("repo:" or "image:") so the reader
// can tell the two node types apart at a glance. Children are
// indented relative to their parent, with the last child using an
// elbow connector (└─) and earlier children using a tee (├─) to make
// nested chains easy to follow.
func TreeText(t *graph.TreeNode) string {
	var sb strings.Builder
	writeTree(&sb, t, "", true, true)
	return sb.String()
}

// writeTree recursively writes one node and its children. prefix
// accumulates the connector characters that precede this node's
// connector (vertical lines from ancestors that have more siblings
// after them). isRoot is true only for the top-level call; isLast
// is true when this node has no siblings after it (so its
// connector is the elbow "└─" rather than the tee "├─").
func writeTree(w io.Writer, t *graph.TreeNode, prefix string, isRoot, isLast bool) {
	kindLabel := "image"
	if t.Kind == graph.KindRepo {
		kindLabel = "repo"
	}

	if isRoot {
		// Root: no connector, no indent.
		fmt.Fprintf(w, "%s: %s\n", kindLabel, t.ID)
	} else {
		connector := "├─ "
		if isLast {
			connector = "└─ "
		}
		fmt.Fprintf(w, "%s%s%s: %s\n", prefix, connector, kindLabel, t.ID)
	}

	// The prefix passed to children is this node's prefix, plus a
	// vertical-line continuation if this node has more siblings.
	// (Root has no siblings, so no continuation is needed.)
	var childPrefix string
	if !isRoot && !isLast {
		childPrefix = prefix + "│  "
	} else if !isRoot {
		childPrefix = prefix + "   "
	}

	for i, c := range t.Children {
		writeTree(w, c, childPrefix, false, i == len(t.Children)-1)
	}
}

// GraphNode is the JSON shape of a node in the export.
type GraphNode struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// GraphEdge is the JSON shape of an edge in the export.
type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// GraphDocument is the top-level JSON shape emitted by GraphJSON.
type GraphDocument struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// GraphJSON serialises g into a structured JSON document. The shape
// is intentionally minimal (just nodes + edges) so downstream tools
// can transform it into Mermaid, DOT, or a custom visualizer without
// having to know dockdeps-specific terms.
//
// The returned bytes are pretty-printed with two-space indentation
// for readability.
func GraphJSON(g *graph.Graph) ([]byte, error) {
	doc := GraphDocument{}

	for _, id := range g.AllRepos() {
		doc.Nodes = append(doc.Nodes, GraphNode{Kind: "repo", ID: id})
	}
	for _, id := range g.AllImages() {
		doc.Nodes = append(doc.Nodes, GraphNode{Kind: "image", ID: id})
	}

	for _, id := range g.AllRepos() {
		for _, target := range g.ForwardTargets(id) {
			doc.Edges = append(doc.Edges, GraphEdge{From: id, To: target, Kind: "depends-on"})
		}
	}
	for _, id := range g.AllImages() {
		for _, target := range g.ForwardTargets(id) {
			doc.Edges = append(doc.Edges, GraphEdge{From: id, To: target, Kind: "depends-on"})
		}
	}

	return json.MarshalIndent(doc, "", "  ")
}
