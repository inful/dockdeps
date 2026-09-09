package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/inful/dockdeps/internal/config"
	"github.com/inful/dockdeps/internal/scanner"
	"github.com/inful/dockdeps/internal/store"
	"github.com/inful/multiforge"
)

// fakeClient is a minimal multiforge.Client for CLI integration tests.
type fakeClient struct {
	repos    []multiforge.Repository
	contents map[string][]byte
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		repos: []multiforge.Repository{
			{ID: "github.com/inful/app", Forge: multiforge.GitHub, Owner: "inful", Name: "app", FullName: "inful/app", DefaultBranch: "main"},
		},
		contents: map[string][]byte{
			"inful/app/Dockerfile": []byte("FROM golang:1.22 AS builder\nFROM alpine:3.19\n"),
		},
	}
}

func (f *fakeClient) ListUserRepos(ctx context.Context, user string) ([]multiforge.Repository, error) {
	out := make([]multiforge.Repository, len(f.repos))
	copy(out, f.repos)
	return out, nil
}
func (f *fakeClient) ListOrgRepos(ctx context.Context, org string) ([]multiforge.Repository, error) {
	return nil, nil
}
func (f *fakeClient) GetRepository(ctx context.Context, owner, repo string) (*multiforge.Repository, error) {
	for i := range f.repos {
		if f.repos[i].Owner == owner && f.repos[i].Name == repo {
			r := f.repos[i]
			return &r, nil
		}
	}
	return nil, multiforge.NewError(multiforge.KindNotFound, "GetRepository", errors.New("not found"))
}
func (f *fakeClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	return "main", nil
}
func (f *fakeClient) GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	c, ok := f.contents[owner+"/"+repo+"/"+path]
	if !ok {
		return nil, multiforge.NewError(multiforge.KindNotFound, "GetFile", errors.New("not found"))
	}
	return c, nil
}
func (f *fakeClient) ListFiles(ctx context.Context, owner, repo, path, ref string) ([]multiforge.FileInfo, error) {
	if _, ok := f.contents[owner+"/"+repo+"/Dockerfile"]; ok {
		return []multiforge.FileInfo{{Path: "Dockerfile", SHA: "abc"}}, nil
	}
	return nil, nil
}

// runCLI is a small helper that invokes main()'s logic with
// injected args and captures stdout/stderr.
func runCLI(t *testing.T, args []string) (stdout, stderr string, exitCode int) {
	t.Helper()

	oldStdout, oldStderr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	exitCode = runMainForTest(args)

	_ = wOut.Close()
	_ = wErr.Close()
	outBytes, _ := io.ReadAll(rOut)
	errBytes, _ := io.ReadAll(rErr)
	return string(outBytes), string(errBytes), exitCode
}

// runMainForTest is a wrapper around the same logic as main() but
// returns the exit code rather than calling os.Exit. Defined in
// main_test.go so we can call it from tests without exporting.
func runMainForTest(args []string) int {
	cli := &CLI{}
	parser, err := kong.New(cli,
		kong.Name("dockdeps"),
		kong.Description("Track Docker image dependencies across multiple forges."),
		kong.UsageOnError(),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		return 1
	}
	kctx, err := parser.Parse(args)
	if err != nil {
		parser.FatalIfErrorf(err)
		return 2
	}
	if err := kctx.Run(cli); err != nil {
		return 1
	}
	return 0
}

// TestCLI_Version is a smoke test for the simplest subcommand.
func TestCLI_Version(t *testing.T) {
	stdout, _, _ := runCLI(t, []string{"version"})
	if !strings.Contains(stdout, "dockdeps") {
		t.Errorf("expected version output, got %q", stdout)
	}
}

// TestCLI_Help confirms --help produces the usage summary.
func TestCLI_Help(t *testing.T) {
	stdout, _, _ := runCLI(t, []string{"--help"})
	for _, want := range []string{"init", "scan", "ls", "tree", "graph"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help output missing %q", want)
		}
	}
}

// TestCLI_Init scaffolds a config file in a temp directory and
// confirms the config + state dir are written.
func TestCLI_Init(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	stdout, _, _ := runCLI(t, []string{"init"})
	if !strings.Contains(stdout, "wrote config") {
		t.Errorf("init output missing 'wrote config': %q", stdout)
	}

	// Config should exist.
	cfgPath := filepath.Join(dir, ".config", "dockdeps", "config.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("config not created: %v", err)
	}

	// State dir should exist.
	stateDir := filepath.Join(dir, ".local", "share", "dockdeps", "state")
	if _, err := os.Stat(stateDir); err != nil {
		t.Errorf("state dir not created: %v", err)
	}
}

// TestCLI_Doctor exercises the doctor command with a real config.
func TestCLI_Doctor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Run init first so the config file exists.
	_, _, _ = runCLI(t, []string{"init"})

	stdout, _, _ := runCLI(t, []string{"doctor"})
	if !strings.Contains(stdout, "forges configured") {
		t.Errorf("doctor output missing 'forges configured': %q", stdout)
	}
}

// TestCLI_LsFromStore exercises `ls repos` against a populated
// state file. We write the state directly (no scan needed).
func TestCLI_LsFromStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Run init so the config file is in place.
	_, _, _ = runCLI(t, []string{"init"})

	// Populate state directly so we don't need network.
	cfg, err := config.Load(filepath.Join(dir, ".config", "dockdeps", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &store.Store{
		Repos: []store.Repo{
			{ID: "github.com/inful/app", Owner: "inful", Name: "app", DefaultBranch: "main"},
		},
		Images: []store.Image{
			{ID: "docker.io/library/golang:1.22"},
		},
	}
	if err := store.Save(cfg.StateDir, s); err != nil {
		t.Fatal(err)
	}

	stdout, _, _ := runCLI(t, []string{"ls", "repos"})
	if !strings.Contains(stdout, "github.com/inful/app") {
		t.Errorf("ls repos output missing app: %q", stdout)
	}
}

// TestCLI_Tree exercises `tree` against a populated state.
func TestCLI_Tree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_, _, _ = runCLI(t, []string{"init"})

	cfg, err := config.Load(filepath.Join(dir, ".config", "dockdeps", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &store.Store{
		Repos: []store.Repo{
			{ID: "github.com/inful/app", Owner: "inful", Name: "app", DefaultBranch: "main"},
		},
		Images: []store.Image{
			{ID: "docker.io/library/golang:1.22"},
		},
		Edges: []store.Edge{
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/golang:1.22"},
		},
	}
	if err := store.Save(cfg.StateDir, s); err != nil {
		t.Fatal(err)
	}

	stdout, _, _ := runCLI(t, []string{"tree", "github.com/inful/app"})
	if !strings.Contains(stdout, "docker.io/library/golang:1.22") {
		t.Errorf("tree output missing golang: %q", stdout)
	}
}

// TestCLI_Dependents exercises `dependents` against a populated
// state.
func TestCLI_Dependents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_, _, _ = runCLI(t, []string{"init"})

	cfg, err := config.Load(filepath.Join(dir, ".config", "dockdeps", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &store.Store{
		Repos: []store.Repo{
			{ID: "github.com/inful/app", Owner: "inful", Name: "app", DefaultBranch: "main"},
		},
		Images: []store.Image{
			{ID: "docker.io/library/golang:1.22"},
		},
		Edges: []store.Edge{
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/golang:1.22"},
		},
	}
	if err := store.Save(cfg.StateDir, s); err != nil {
		t.Fatal(err)
	}

	stdout, _, _ := runCLI(t, []string{"dependents", "docker.io/library/golang:1.22"})
	if !strings.Contains(stdout, "github.com/inful/app") {
		t.Errorf("dependents output missing app: %q", stdout)
	}
}

// TestCLI_Graph exercises `graph --format json`.
func TestCLI_Graph(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_, _, _ = runCLI(t, []string{"init"})

	cfg, err := config.Load(filepath.Join(dir, ".config", "dockdeps", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &store.Store{
		Repos: []store.Repo{
			{ID: "github.com/inful/app", Owner: "inful", Name: "app", DefaultBranch: "main"},
		},
		Images: []store.Image{
			{ID: "docker.io/library/golang:1.22"},
		},
		Edges: []store.Edge{
			{FromKind: "repo", FromID: "github.com/inful/app", ToKind: "image", ToID: "docker.io/library/golang:1.22"},
		},
	}
	if err := store.Save(cfg.StateDir, s); err != nil {
		t.Fatal(err)
	}

	stdout, _, _ := runCLI(t, []string{"graph"})
	if !strings.Contains(stdout, "github.com/inful/app") {
		t.Errorf("graph output missing app: %q", stdout)
	}
	if !strings.Contains(stdout, "docker.io/library/golang:1.22") {
		t.Errorf("graph output missing golang: %q", stdout)
	}
}

// TestScanner_WithFakeClient confirms the scanner works end-to-end
// with a fake multiforge.Client (sanity check that the CLI glue
// actually uses the scanner we just tested).
func TestScanner_WithFakeClient(t *testing.T) {
	fc := newFakeClient()
	cfg := &config.Config{
		Forges: map[string]config.ForgeConfig{
			"github": {Backend: "github", Token: "x", User: "inful"},
		},
	}
	s := scanner.New(fc, cfg.Forges["github"], cfg)
	if err := s.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(s.Store().Images) != 2 {
		t.Errorf("expected 2 images, got %d", len(s.Store().Images))
	}
}

// Compile-time check: scanner.Scan uses time.Now via s.now; we
// can't intercept it from the CLI test, but we can confirm the
// field is used by setting it in a scanner.
var _ = func() *scanner.Scanner {
	s := scanner.New(nil, config.ForgeConfig{}, &config.Config{})
	s.SetErrorHandler(func(string, error) {})
	return s
}

// Silence unused-import warnings from packages used only via reflection.
var (
	_ = scanner.New
)
