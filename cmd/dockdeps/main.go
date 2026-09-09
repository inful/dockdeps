// dockdeps is a CLI for tracking Docker image dependencies across
// repositories on multiple forges (GitHub, GitLab, Forgejo/Gitea).
//
// Usage:
//
//	dockdeps init                           # scaffold config + state dir
//	dockdeps scan                           # walk configured repos, update state
//	dockdeps ls [repos|images]              # list known entities
//	dockdeps dependents <image-or-repo>     # who depends on this?
//	dockdeps dependencies <image-or-repo>   # what does this depend on?
//	dockdeps tree <image-or-repo>           # visual tree of dependencies
//	dockdeps graph --format dot|json|mermaid  # full graph export
//	dockdeps doctor                         # check config and connectivity
//	dockdeps version
//
// See the README for configuration details.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/inful/dockdeps/internal/config"
	"github.com/inful/dockdeps/internal/graph"
	"github.com/inful/dockdeps/internal/render"
	"github.com/inful/dockdeps/internal/scanner"
	"github.com/inful/dockdeps/internal/store"
	"github.com/inful/multiforge"
)

// Version is set via -ldflags at build time.
var Version = "0.1.0-dev"

// CLI is the top-level kong binding. Each subcommand is a separate
// struct with its own flags and a Run() method.
type CLI struct {
	GlobalFlags

	Init         InitCmd         `cmd:"" help:"Scaffold a config file and state directory."`
	Scan         ScanCmd         `cmd:"" help:"Walk configured forges and update the dependency graph."`
	Ls           LsCmd           `cmd:"" help:"List known entities."`
	Dependents   DependentsCmd   `cmd:"" name:"dependents" help:"Show repos that depend on the given image or repo."`
	Dependencies DependenciesCmd `cmd:"" name:"dependencies" help:"Show what the given image or repo depends on."`
	Tree         TreeCmd         `cmd:"" help:"Render a tree of dependencies."`
	Graph        GraphCmd        `cmd:"" help:"Export the full dependency graph."`
	Doctor       DoctorCmd       `cmd:"" help:"Check configuration and forge connectivity."`
	Version      VersionCmd      `cmd:"" help:"Print dockdeps version."`
}

// GlobalFlags are flags available on every subcommand.
type GlobalFlags struct {
	// Config is the path to the config file. We do NOT use
	// type:"path" because kong resolves "~" via user.Current()
	// (which reads /etc/passwd on Unix), whereas loadConfig
	// uses os.UserHomeDir which honours $HOME. The latter is
	// what tests and most operators want.
	Config string `help:"Path to config file" default:"~/.config/dockdeps/config.yaml"`
}

// InitCmd scaffolds a config file in the user's home directory.
type InitCmd struct{}

func (c *InitCmd) Run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("init: locate home dir: %w", err)
	}
	cfgPath := filepath.Join(home, ".config", "dockdeps", "config.yaml")
	stateDir := filepath.Join(home, ".local", "share", "dockdeps", "state")

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return fmt.Errorf("init: mkdir config dir: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("init: mkdir state dir: %w", err)
	}

	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Printf("config already exists at %s\n", cfgPath)
	} else {
		if err := os.WriteFile(cfgPath, []byte(defaultConfigYAML), 0o644); err != nil {
			return fmt.Errorf("init: write config: %w", err)
		}
		fmt.Printf("wrote config to %s\n", cfgPath)
	}
	fmt.Printf("state directory: %s\n", stateDir)
	return nil
}

// ScanCmd walks configured forges and updates the state files.
type ScanCmd struct{}

func (c *ScanCmd) Run(globals *CLI) error {
	cfg, err := loadConfig(globals.Config)
	if err != nil {
		return err
	}
	if len(cfg.Forges) == 0 {
		return fmt.Errorf("scan: no forges configured; run `dockdeps init` and edit the config")
	}
	if err := runScan(cfg); err != nil {
		return err
	}
	fmt.Printf("scanned %d forges; state written to %s\n", len(cfg.Forges), cfg.StateDir)
	return nil
}

// LsCmd lists repos or images from the saved state.
type LsCmd struct {
	What    string `arg:"" enum:"repos,images" default:"repos" help:"What to list: repos or images."`
	Orphans bool   `help:"(images only) show only images with no linked producer."`
}

func (c *LsCmd) Run(globals *CLI) error {
	cfg, err := loadConfig(globals.Config)
	if err != nil {
		return err
	}
	s, err := store.Load(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("ls: load state: %w", err)
	}
	if c.What == "repos" {
		for _, r := range s.Repos {
			fmt.Printf("%s\t%s/%s\t%s\n", r.ID, r.Owner, r.Name, r.DefaultBranch)
		}
		return nil
	}
	// images
	for _, img := range s.Images {
		if c.Orphans && len(img.LinkedProducers) > 0 {
			continue
		}
		fmt.Printf("%s\n", img.ID)
	}
	return nil
}

// DependentsCmd shows what depends on the given node.
type DependentsCmd struct {
	Node string `arg:"" help:"Image or repo ID to query."`
}

func (c *DependentsCmd) Run(globals *CLI) error {
	return queryGraph(globals.Config, c.Node, "dependents")
}

// DependenciesCmd shows what the given node depends on.
type DependenciesCmd struct {
	Node string `arg:"" help:"Image or repo ID to query."`
}

func (c *DependenciesCmd) Run(globals *CLI) error {
	return queryGraph(globals.Config, c.Node, "dependencies")
}

// queryGraph loads the state and runs the named graph query.
func queryGraph(configPath, node, query string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	s, err := store.Load(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("%s: load state: %w", query, err)
	}
	g := graph.New(s)
	var nodes []graph.Node
	switch query {
	case "dependents":
		nodes, err = g.Dependents(node)
	case "dependencies":
		nodes, err = g.Dependencies(node)
	default:
		return fmt.Errorf("unknown query %q", query)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", query, err)
	}
	for _, n := range nodes {
		fmt.Println(n.ID)
	}
	return nil
}

// TreeCmd renders the dependency tree rooted at node.
type TreeCmd struct {
	Node  string `arg:"" help:"Image or repo ID to query."`
	Depth int    `help:"Maximum recursion depth (0 = just the root)" default:"10"`
}

func (c *TreeCmd) Run(globals *CLI) error {
	cfg, err := loadConfig(globals.Config)
	if err != nil {
		return err
	}
	s, err := store.Load(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("tree: load state: %w", err)
	}
	g := graph.New(s)
	tree, err := g.Tree(c.Node, c.Depth)
	if err != nil {
		return fmt.Errorf("tree: %w", err)
	}
	fmt.Print(render.TreeText(tree))
	return nil
}

// GraphCmd exports the full graph.
type GraphCmd struct {
	Format string `help:"Output format" enum:"json,dot,mermaid" default:"json"`
}

func (c *GraphCmd) Run(globals *CLI) error {
	cfg, err := loadConfig(globals.Config)
	if err != nil {
		return err
	}
	s, err := store.Load(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("graph: load state: %w", err)
	}
	g := graph.New(s)
	switch c.Format {
	case "json":
		out, err := render.GraphJSON(g)
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	case "dot", "mermaid":
		return fmt.Errorf("graph --format=%s: not yet implemented (Phase 2)", c.Format)
	default:
		return fmt.Errorf("unknown format %q", c.Format)
	}
	return nil
}

// DoctorCmd checks config + connectivity.
type DoctorCmd struct{}

func (c *DoctorCmd) Run(globals *CLI) error {
	cfg, err := loadConfig(globals.Config)
	if err != nil {
		return err
	}
	fmt.Printf("config: %s\n", globals.Config)
	fmt.Printf("state_dir: %s\n", cfg.StateDir)
	fmt.Printf("forges configured: %d\n", len(cfg.Forges))
	for name, fc := range cfg.Forges {
		tokenSet := fc.Token != ""
		fmt.Printf("  %s: backend=%s user=%s token=%v\n", name, fc.Backend, fc.User, tokenSet)
		if !tokenSet {
			fmt.Printf("    WARNING: token is empty (env var unset?)\n")
		}
	}
	return nil
}

// VersionCmd prints the dockdeps version.
type VersionCmd struct{}

func (c *VersionCmd) Run() error {
	fmt.Printf("dockdeps %s\n", Version)
	return nil
}

// loadConfig reads and expands the config file at the given path.
// We expand ~ to the user's home directory first since that's the
// default path users will see.
func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is empty; pass --config or set $DOCKDEPS_CONFIG")
	}
	if path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("expand ~: %w", err)
		}
		path = filepath.Join(home, path[1:])
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// runScan wires up a multiforge client per forge and runs the
// scanner. In Phase 1 we build one client per forge in the config;
// later phases can multiplex them via a fan-out client.
func runScan(cfg *config.Config) error {
	// For now, process each forge sequentially. Each forge gets its
	// own scanner so its client has a single backend.
	var lastErr error
	for name, fc := range cfg.Forges {
		client, err := buildClient(fc)
		if err != nil {
			lastErr = fmt.Errorf("forge %q: %w", name, err)
			fmt.Fprintf(os.Stderr, "warning: %v\n", lastErr)
			continue
		}
		s := scanner.New(client, cfg)
		if err := s.Load(); err != nil {
			lastErr = err
			fmt.Fprintf(os.Stderr, "warning: load state for forge %q: %v\n", name, err)
		}
		if err := s.Scan(context.Background()); err != nil {
			lastErr = err
			fmt.Fprintf(os.Stderr, "warning: scan forge %q: %v\n", name, err)
		}
		if err := s.Save(); err != nil {
			lastErr = err
			fmt.Fprintf(os.Stderr, "warning: save state for forge %q: %v\n", name, err)
		}
	}
	return lastErr
}

// buildClient constructs a multiforge.Client from a ForgeConfig.
func buildClient(fc config.ForgeConfig) (multiforge.Client, error) {
	return multiforge.New(context.Background(), multiforge.Config{
		Backend: multiforge.Backend(fc.Backend),
		Token:   fc.Token,
		BaseURL: fc.BaseURL,
	})
}

// defaultConfigYAML is written by `dockdeps init` when no config
// file exists yet.
const defaultConfigYAML = `# dockdeps configuration
#
# Add at least one forge block. Each block tells dockdeps how to
# talk to a forge on behalf of one user. ${ENV} interpolation is
# applied at load time.

version: 1

# state_dir defaults to ~/.local/share/dockdeps/state — uncomment
# to override.
# state_dir: /var/lib/dockdeps/state

forges:
  github:
    backend: github
    token: "${GITHUB_TOKEN}"
    user: inful

  # gitlab:
  #   backend: gitlab
  #   base_url: https://gitlab.com
  #   token: "${GITLAB_TOKEN}"
  #   user: inful

  # forgejo:
  #   backend: forgejo
  #   base_url: https://git.example.com
  #   token: "${FORGE_TOKEN}"
  #   user: inful

# Optional: explicit list of repos to scan (otherwise auto-discovered
# from each forge's user).
# repos:
#   - github.com/inful/dockdeps

# Optional: aliases that link a registry image to a forge repo that
# produces it. Useful for monorepos where CI pushes images with
# specific tags that the Dockerfile can't know about statically.
# aliases:
#   - image: ghcr.io/inful/dockdeps
#     source: github.com/inful/dockdeps

# Optional: file filters. Defaults are sensible for most projects.
# filters:
#   exclude_paths:
#     - "**/node_modules/**"
#     - "**/.git/**"
#     - "**/vendor/**"
#   max_dockerfile_size: 65536
`

func main() {
	cli := &CLI{}
	parser, err := kong.New(cli,
		kong.Name("dockdeps"),
		kong.Description("Track Docker image dependencies across multiple forges."),
		kong.UsageOnError(),
		kong.Vars{"version": Version},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	kctx, err := parser.Parse(os.Args[1:])
	if err != nil {
		parser.FatalIfErrorf(err)
		os.Exit(1)
	}
	if err := kctx.Run(cli); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
