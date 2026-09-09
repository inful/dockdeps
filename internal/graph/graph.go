// Package graph builds a queryable dependency graph from a Store.
//
// The graph is built once per Store and queried for:
//
//   - Dependencies of a node (transitive): what does X depend on?
//   - Dependents of a node (transitive): who depends on X?
//   - Tree rooted at a node: indented view of dependencies
//   - Orphans: images with no linked producer
//   - Membership: does the graph know about X?
//
// Edges flow in one direction: "from depends on to". When asked
// for dependents of X, we traverse edges in REVERSE (incoming).
// When asked for dependencies of X, we traverse edges forward
// (outgoing).
//
// Cycle detection: the graph is stored as a flat edge list, and
// traversal uses a visited-set to avoid infinite recursion. Docker
// build graphs are typically acyclic, but aliases and image-image
// chains can occasionally form loops when operators misconfigure.
package graph

import (
	"errors"
	"fmt"
	"sort"

	"github.com/inful/dockdeps/internal/store"
)

// NodeKind is the type of a node in the graph.
type NodeKind string

const (
	KindRepo  NodeKind = "repo"
	KindImage NodeKind = "image"
)

// Node is a graph node (Repo or Image).
type Node struct {
	Kind NodeKind
	ID   string
	// Optional inline data for convenience; may be empty.
	Label string
}

// Graph is the queryable form of a Store.
type Graph struct {
	repos    map[string]store.Repo
	images   map[string]store.Image
	forward  map[string][]string // node ID → outgoing edge targets
	backward map[string][]string // node ID → incoming edge sources
}

// New builds a Graph from a Store. The Store is not held by
// reference; mutations to it after New returns are not reflected.
func New(s *store.Store) *Graph {
	g := &Graph{
		repos:    make(map[string]store.Repo),
		images:   make(map[string]store.Image),
		forward:  make(map[string][]string),
		backward: make(map[string][]string),
	}
	for _, r := range s.Repos {
		g.repos[r.ID] = r
	}
	for _, img := range s.Images {
		g.images[img.ID] = img
	}
	for _, e := range s.Edges {
		g.forward[e.FromID] = append(g.forward[e.FromID], e.ToID)
		g.backward[e.ToID] = append(g.backward[e.ToID], e.FromID)
	}
	return g
}

// HasNode reports whether the graph knows about a node.
func (g *Graph) HasNode(id string) bool {
	if _, ok := g.repos[id]; ok {
		return true
	}
	if _, ok := g.images[id]; ok {
		return true
	}
	return false
}

// NodeKind returns the kind of a node, or empty string if unknown.
func (g *Graph) NodeKind(id string) NodeKind {
	if _, ok := g.repos[id]; ok {
		return KindRepo
	}
	if _, ok := g.images[id]; ok {
		return KindImage
	}
	return ""
}

// AllRepos returns every repo ID known to the graph, sorted.
func (g *Graph) AllRepos() []string {
	out := make([]string, 0, len(g.repos))
	for id := range g.repos {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// AllImages returns every image ID known to the graph, sorted.
func (g *Graph) AllImages() []string {
	out := make([]string, 0, len(g.images))
	for id := range g.images {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ForwardTargets returns the direct outgoing edges of id, sorted.
// Used by renderers that need to walk the graph structurally.
func (g *Graph) ForwardTargets(id string) []string {
	out := append([]string(nil), g.forward[id]...)
	sort.Strings(out)
	return out
}

// BackwardSources returns the direct incoming edges of id, sorted.
func (g *Graph) BackwardSources(id string) []string {
	out := append([]string(nil), g.backward[id]...)
	sort.Strings(out)
	return out
}

// ForwardMap returns a copy of the forward adjacency map. Useful
// for serializers that want to walk every edge without enumerating
// nodes separately.
func (g *Graph) ForwardMap() map[string][]string {
	out := make(map[string][]string, len(g.forward))
	for k, v := range g.forward {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// BackwardMap returns a copy of the backward adjacency map.
func (g *Graph) BackwardMap() map[string][]string {
	out := make(map[string][]string, len(g.backward))
	for k, v := range g.backward {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Dependencies returns the transitive closure of nodes that id
// depends on, sorted by ID for deterministic output. The starting
// node itself is excluded from the result.
//
// Returns an error if id is unknown to the graph.
func (g *Graph) Dependencies(id string) ([]Node, error) {
	if !g.HasNode(id) {
		return nil, fmt.Errorf("graph: unknown node %q", id)
	}
	visited := map[string]bool{id: true}
	var out []string
	var walk func(string)
	walk = func(cur string) {
		for _, next := range g.forward[cur] {
			if visited[next] {
				continue
			}
			visited[next] = true
			out = append(out, next)
			walk(next)
		}
	}
	walk(id)
	return g.nodesFromIDs(out), nil
}

// Dependents returns the transitive closure of nodes that depend on
// id (i.e. the reverse direction), sorted by ID.
//
// Returns an error if id is unknown to the graph.
func (g *Graph) Dependents(id string) ([]Node, error) {
	if !g.HasNode(id) {
		return nil, fmt.Errorf("graph: unknown node %q", id)
	}
	visited := map[string]bool{id: true}
	var out []string
	var walk func(string)
	walk = func(cur string) {
		for _, next := range g.backward[cur] {
			if visited[next] {
				continue
			}
			visited[next] = true
			out = append(out, next)
			walk(next)
		}
	}
	walk(id)
	return g.nodesFromIDs(out), nil
}

// TreeNode is a node in the dependency tree rooted at a starting
// node. Children are direct dependencies only — the caller decides
// whether to recurse (typically via the depth limit at query time).
type TreeNode struct {
	Kind     NodeKind
	ID       string
	Children []*TreeNode
}

// Tree returns the dependency tree rooted at id. depth caps the
// recursion (0 means just the root, no children).
//
// Returns an error if id is unknown to the graph.
func (g *Graph) Tree(id string, depth int) (*TreeNode, error) {
	if !g.HasNode(id) {
		return nil, fmt.Errorf("graph: unknown node %q", id)
	}
	return g.buildTree(id, depth, map[string]bool{id: true}), nil
}

// buildTree recursively constructs a TreeNode. The visited set
// breaks cycles.
func (g *Graph) buildTree(id string, depth int, visited map[string]bool) *TreeNode {
	node := &TreeNode{
		Kind: g.NodeKind(id),
		ID:   id,
	}
	if depth <= 0 {
		return node
	}
	// Sort outgoing edges by target ID for deterministic output.
	targets := append([]string(nil), g.forward[id]...)
	sort.Strings(targets)
	for _, next := range targets {
		if visited[next] {
			continue
		}
		visited[next] = true
		node.Children = append(node.Children, g.buildTree(next, depth-1, visited))
	}
	return node
}

// OrphanImages returns images with no linked producer (i.e. no
// alias points at them and no Repo edge originates from them).
// Useful for surfacing "images I depend on that I don't know who
// publishes" in `dockdeps ls images --orphans`.
func (g *Graph) OrphanImages() []store.Image {
	var out []store.Image
	for _, img := range g.images {
		if len(img.LinkedProducers) == 0 {
			out = append(out, img)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// nodesFromIDs converts a list of node IDs into Nodes, looking up
// kind/label from the graph's maps. Unknown IDs (shouldn't happen
// if the graph is consistent) are silently skipped.
func (g *Graph) nodesFromIDs(ids []string) []Node {
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		switch g.NodeKind(id) {
		case KindRepo:
			r := g.repos[id]
			out = append(out, Node{Kind: KindRepo, ID: id, Label: r.Owner + "/" + r.Name})
		case KindImage:
			out = append(out, Node{Kind: KindImage, ID: id})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ErrUnknownNode is returned by Dependencies/Dependents/Tree when
// the starting node isn't in the graph. Callers can use errors.Is
// to distinguish "missing node" from other failures.
var ErrUnknownNode = errors.New("graph: unknown node")
