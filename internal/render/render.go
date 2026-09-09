// Package render turns graph structures into human- or machine-
// readable text. It currently supports these output forms:
//
//   - TreeText: an indented tree suitable for terminals.
//   - GraphJSON: a structured JSON export suitable for downstream tooling.
//   - Mermaid: Mermaid flowchart syntax (renders on GitHub, GitLab, etc.).
//   - DOT: Graphviz DOT syntax (renders with `dot -Tsvg`).
//   - HTML: a self-contained interactive HTML page using vis-network.
//
// Renderers are pure functions: they take a graph/tree and write
// to a writer. They never touch the network, filesystem, or config.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
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
		_, _ = fmt.Fprintf(w, "%s: %s\n", kindLabel, t.ID)
	} else {
		connector := "├─ "
		if isLast {
			connector = "└─ "
		}
		_, _ = fmt.Fprintf(w, "%s%s%s: %s\n", prefix, connector, kindLabel, t.ID)
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

// Mermaid serialises g into Mermaid flowchart syntax suitable for
// rendering on GitHub, GitLab, and other Markdown renderers that
// support mermaid fenced blocks.
//
// Repos render with stadium (rounded-end) shapes; images render
// with rectangles. This visual distinction lets readers spot "which
// boxes are docker images vs source repos" at a glance.
//
// All node IDs are quoted so identifiers containing slashes, dots,
// or other Mermaid-special characters don't break the parser.
func Mermaid(g *graph.Graph) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("flowchart LR\n")

	// Declare every node with the appropriate shape. Declaring
	// each node once (instead of inline on each edge) lets
	// Mermaid merge declarations across multiple edges.
	for _, id := range g.AllRepos() {
		fmt.Fprintf(&sb, "  %s([%s])\n", quoteMermaid(id), id)
	}
	for _, id := range g.AllImages() {
		fmt.Fprintf(&sb, "  %s[%s]\n", quoteMermaid(id), id)
	}

	// Edges. Each "from --> to" arrow appears once per directed
	// edge in the graph; the underlying graph already dedupes
	// edges, so we can iterate blindly.
	for _, id := range g.AllRepos() {
		for _, target := range g.ForwardTargets(id) {
			fmt.Fprintf(&sb, "  %s --> %s\n", quoteMermaid(id), quoteMermaid(target))
		}
	}
	for _, id := range g.AllImages() {
		for _, target := range g.ForwardTargets(id) {
			fmt.Fprintf(&sb, "  %s --> %s\n", quoteMermaid(id), quoteMermaid(target))
		}
	}

	return []byte(sb.String()), nil
}

// quoteMermaid wraps an ID in double quotes if it contains any
// character that Mermaid treats as syntax. Identifiers made of
// only [A-Za-z0-9_-] are emitted unquoted for readability.
func quoteMermaid(id string) string {
	if id == "" {
		return `""`
	}
	safe := true
	for _, r := range id {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return id
	}
	// Quote and escape internal quotes / backslashes.
	escaped := strings.ReplaceAll(id, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// DOT serialises g into Graphviz DOT syntax suitable for rendering
// with `dot -Tsvg graph.dot > graph.svg` or any other Graphviz
// consumer.
//
// Repos render as boxes; images render as ellipses. Edges are
// unlabelled forward arrows. Layout direction is left-to-right
// (rankdir=LR) which matches how dependency graphs naturally read.
func DOT(g *graph.Graph) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("digraph G {\n")
	sb.WriteString("  rankdir=LR;\n")
	sb.WriteString("  node [fontname=\"Helvetica\"];\n")
	sb.WriteString("  edge [arrowhead=vee];\n")

	// Node declarations with shape per kind. Repos are boxes
	// (with a subtle fill so the human eye groups them);
	// images are ellipses (no fill) to read as "things".
	for _, id := range g.AllRepos() {
		fmt.Fprintf(&sb, "  %s [label=%s, shape=box, style=\"filled,rounded\", fillcolor=\"#e0e7ff\"];\n",
			quoteDOT(id), quoteDOT(id))
	}
	for _, id := range g.AllImages() {
		fmt.Fprintf(&sb, "  %s [label=%s, shape=ellipse];\n",
			quoteDOT(id), quoteDOT(id))
	}

	// Edges.
	for _, id := range g.AllRepos() {
		for _, target := range g.ForwardTargets(id) {
			fmt.Fprintf(&sb, "  %s -> %s;\n", quoteDOT(id), quoteDOT(target))
		}
	}
	for _, id := range g.AllImages() {
		for _, target := range g.ForwardTargets(id) {
			fmt.Fprintf(&sb, "  %s -> %s;\n", quoteDOT(id), quoteDOT(target))
		}
	}

	sb.WriteString("}\n")
	return []byte(sb.String()), nil
}

// quoteDOT wraps an ID in double quotes, escaping internal
// backslashes and double-quotes so the result is a valid DOT
// quoted ID. DOT accepts unquoted IDs that match
// [A-Za-z0-9_200b-206f]; everything else must be quoted.
func quoteDOT(id string) string {
	escaped := strings.ReplaceAll(id, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// HTML serialises g into a self-contained HTML page that renders
// the dependency graph statically as inline SVG. The page embeds
// the data, the rendering, and the page chrome — no external
// resources are required at view time, so the file works offline
// and can be committed to a repo or emailed.
//
// We use inline SVG rather than an interactive JavaScript
// renderer (vis-network, d3, etc.) because:
//
//   1. The page is fully self-contained — no CDN dependency, no
//      <script src="...">, no <link rel="stylesheet">.
//   2. SVG is universally supported in browsers, Markdown
//      renderers (GitHub, GitLab), and PDF conversion tools.
//   3. The trade-off is no interactivity (no pan/zoom) — but the
//      page is small enough that scroll-and-zoom-via-CSS is
//      acceptable for the v0.2 audience (a single user's repos).
//
// Layout is a simple top-down DAG (repos on the left, images on
// the right), with longest-path layering so edges flow
// left-to-right without crossing in most cases.
func HTML(g *graph.Graph) ([]byte, error) {
	svg, err := renderSVG(g)
	if err != nil {
		return nil, fmt.Errorf("render: HTML: build SVG: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(htmlHead)
	sb.WriteString("<body>\n")
	sb.WriteString(`<div id="header"><h1>Docker dependency graph</h1>`)
	fmt.Fprintf(&sb, `<p class="meta">%d repos &middot; %d images &middot; generated by dockdeps</p>`,
		len(g.AllRepos()), len(g.AllImages()))
	sb.WriteString("</div>\n")
	sb.WriteString(svg)
	sb.WriteString("\n</body>\n</html>\n")
	return []byte(sb.String()), nil
}

// renderSVG produces an inline SVG document with the graph nodes
// and edges. The layout is a layered DAG: each node gets a layer
// based on the longest path from any "source" node (a node with
// no incoming edges), and within a layer nodes are stacked
// vertically in sorted ID order for stability across runs.
func renderSVG(g *graph.Graph) (string, error) {
	const (
		nodeW = 220
		nodeH = 28
		padX  = 40
		padY  = 20
		gapX  = 60
		gapY  = 14
	)

	// Assign each node a layer via longest-path layering.
	layers := computeLayers(g)

	// Group nodes by layer, sort each layer's nodes by ID for
	// stable layout across runs.
	layerNodes := make(map[int][]string)
	for id, layer := range layers {
		layerNodes[layer] = append(layerNodes[layer], id)
	}
	maxLayer := 0
	for l := range layerNodes {
		if l > maxLayer {
			maxLayer = l
		}
	}
	for l := 0; l <= maxLayer; l++ {
		sort.Strings(layerNodes[l])
	}

	// Compute canvas size.
	width := padX*2 + (maxLayer+1)*nodeW + maxLayer*gapX
	height := padY * 2
	for l := 0; l <= maxLayer; l++ {
		h := len(layerNodes[l])*nodeH + (len(layerNodes[l])-1)*gapY
		if h > height-padY*2 {
			height = padY*2 + h
		}
	}
	if height < 200 {
		height = 200
	}

	// Map node ID to (x, y) of its top-left corner.
	pos := map[string][2]int{}
	for l := 0; l <= maxLayer; l++ {
		x := padX + l*(nodeW+gapX)
		// Vertical centre the column.
		colH := len(layerNodes[l])*nodeH + (len(layerNodes[l])-1)*gapY
		yStart := padY + (height-padY*2-colH)/2
		for i, id := range layerNodes[l] {
			pos[id] = [2]int{x, yStart + i*(nodeH+gapY)}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">`+"\n",
		width, height, width, height)

	// Background.
	fmt.Fprintf(&sb, `  <rect width="%d" height="%d" fill="#ffffff"/>`+"\n", width, height)

	// Edges first (so nodes draw on top).
	for _, id := range g.AllRepos() {
		for _, target := range g.ForwardTargets(id) {
			from, ok1 := pos[id]
			to, ok2 := pos[target]
			if !ok1 || !ok2 {
				continue
			}
			x1 := from[0] + nodeW
			y1 := from[1] + nodeH/2
			x2 := to[0]
			y2 := to[1] + nodeH/2
			// Cubic bezier curve for a clean S-shape.
			cx := (x1 + x2) / 2
			fmt.Fprintf(&sb, `  <path d="M %d,%d C %d,%d %d,%d %d,%d" stroke="#9ca3af" stroke-width="1" fill="none" marker-end="url(#arrow)"/>`+"\n",
				x1, y1, cx, y1, cx, y2, x2, y2)
		}
	}
	for _, id := range g.AllImages() {
		for _, target := range g.ForwardTargets(id) {
			from, ok1 := pos[id]
			to, ok2 := pos[target]
			if !ok1 || !ok2 {
				continue
			}
			x1 := from[0] + nodeW
			y1 := from[1] + nodeH/2
			x2 := to[0]
			y2 := to[1] + nodeH/2
			cx := (x1 + x2) / 2
			fmt.Fprintf(&sb, `  <path d="M %d,%d C %d,%d %d,%d %d,%d" stroke="#9ca3af" stroke-width="1" fill="none" marker-end="url(#arrow)"/>`+"\n",
				x1, y1, cx, y1, cx, y2, x2, y2)
		}
	}

	// Arrowhead marker (referenced by marker-end above).
	sb.WriteString(`  <defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0,0 L 10,5 L 0,10 z" fill="#9ca3af"/></marker></defs>` + "\n")

	// Nodes. Repos are light-blue rounded boxes; images are
	// light-grey ellipses.
	for _, id := range g.AllRepos() {
		p, ok := pos[id]
		if !ok {
			continue
		}
		fmt.Fprintf(&sb, `  <g><rect x="%d" y="%d" width="%d" height="%d" rx="6" ry="6" fill="#e0e7ff" stroke="#6366f1" stroke-width="1"/>`,
			p[0], p[1], nodeW, nodeH)
		fmt.Fprintf(&sb, `<text x="%d" y="%d" font-family="monospace" font-size="11" fill="#1a1a1a">`+"\n",
			p[0]+8, p[1]+18)
		sb.WriteString(svgEscapeText(id))
		sb.WriteString("\n  </text></g>\n")
	}
	for _, id := range g.AllImages() {
		p, ok := pos[id]
		if !ok {
			continue
		}
		fmt.Fprintf(&sb, `  <g><ellipse cx="%d" cy="%d" rx="%d" ry="%d" fill="#f3f4f6" stroke="#9ca3af" stroke-width="1"/>`,
			p[0]+nodeW/2, p[1]+nodeH/2, nodeW/2, nodeH/2)
		fmt.Fprintf(&sb, `<text x="%d" y="%d" font-family="monospace" font-size="11" fill="#1a1a1a" text-anchor="middle">`+"\n",
			p[0]+nodeW/2, p[1]+nodeH/2+4)
		sb.WriteString(svgEscapeText(id))
		sb.WriteString("\n  </text></g>\n")
	}

	sb.WriteString("</svg>\n")
	return sb.String(), nil
}

// computeLayers assigns each node a layer (column) in the DAG.
// Layer 0 is for "source" nodes (no incoming edges). Every other
// node's layer is 1 + the max layer of its predecessors. Cycles
// are broken by treating already-visited nodes as having layer 0
// to prevent infinite recursion.
func computeLayers(g *graph.Graph) map[string]int {
	layers := map[string]int{}
	// First, identify all nodes so every node gets a layer even
	// if it's a sink (no outgoing edges).
	all := map[string]bool{}
	for _, id := range g.AllRepos() {
		all[id] = true
	}
	for _, id := range g.AllImages() {
		all[id] = true
	}

	var compute func(id string, visiting map[string]bool) int
	compute = func(id string, visiting map[string]bool) int {
		if l, ok := layers[id]; ok {
			return l
		}
		if visiting[id] {
			// Cycle: treat as layer 0 to break the loop.
			return 0
		}
		visiting[id] = true
		defer delete(visiting, id)

		predecessors := g.BackwardSources(id)
		if len(predecessors) == 0 {
			layers[id] = 0
			return 0
		}
		maxPred := 0
		for _, p := range predecessors {
			l := compute(p, visiting)
			if l+1 > maxPred {
				maxPred = l + 1
			}
		}
		layers[id] = maxPred
		return maxPred
	}

	for id := range all {
		compute(id, map[string]bool{})
	}
	return layers
}

// svgEscapeText escapes characters that have special meaning
// inside SVG <text> elements: &, <, >.
func svgEscapeText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// htmlHead is the static page chrome. We embed it as a constant so
// the rendering logic stays focused on the data side. There are no
// external resources — the page works offline, can be emailed,
// and can be committed to a repo without breaking.
const htmlHead = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Docker dependency graph</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif; margin: 0; background: #fafafa; color: #1a1a1a; }
  #header { padding: 16px 24px; border-bottom: 1px solid #e5e7eb; background: #fff; }
  #header h1 { margin: 0 0 4px; font-size: 18px; font-weight: 600; }
  #header .meta { margin: 0; font-size: 13px; color: #6b7280; }
  svg { display: block; margin: 16px auto; max-width: 100%; height: auto; }
</style>
</head>
`
