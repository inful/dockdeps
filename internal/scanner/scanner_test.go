package scanner_test

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inful/dockdeps/internal/config"
	"github.com/inful/dockdeps/internal/scanner"
	"github.com/inful/dockdeps/internal/store"
	"github.com/inful/multiforge"
)

// fakeClient is an in-memory multiforge.Client for testing. It serves
// a fixed set of repos and file contents.
type fakeClient struct {
	mu              sync.Mutex
	repos           []multiforge.Repository
	contents        map[string][]byte // "<owner>/<repo>/<path>" -> bytes
	defaultBranch   string
	listUserCalled  int
	getFileCalls    []string
	failNextGetFile error
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		repos: []multiforge.Repository{
			{
				ID:            "github.com/inful/app",
				Forge:         multiforge.GitHub,
				Owner:         "inful",
				Name:          "app",
				FullName:      "inful/app",
				DefaultBranch: "main",
			},
			{
				ID:            "github.com/inful/worker",
				Forge:         multiforge.GitHub,
				Owner:         "inful",
				Name:          "worker",
				FullName:      "inful/worker",
				DefaultBranch: "main",
			},
		},
		contents: map[string][]byte{
			"inful/app/Dockerfile":    []byte("FROM golang:1.22 AS builder\nFROM alpine:3.19\n"),
			"inful/worker/Dockerfile": []byte("FROM golang:1.22\n"),
		},
		defaultBranch: "main",
	}
}

func (f *fakeClient) key(owner, repo, path string) string {
	return owner + "/" + repo + "/" + path
}

func (f *fakeClient) ListUserRepos(ctx context.Context, user string) ([]multiforge.Repository, error) {
	f.mu.Lock()
	f.listUserCalled++
	f.mu.Unlock()
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
	return f.defaultBranch, nil
}

func (f *fakeClient) GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	f.mu.Lock()
	f.getFileCalls = append(f.getFileCalls, f.key(owner, repo, path))
	if f.failNextGetFile != nil {
		err := f.failNextGetFile
		f.failNextGetFile = nil
		f.mu.Unlock()
		return nil, err
	}
	f.mu.Unlock()

	c, ok := f.contents[f.key(owner, repo, path)]
	if !ok {
		return nil, multiforge.NewError(multiforge.KindNotFound, "GetFile", errors.New("not found"))
	}
	return c, nil
}

func (f *fakeClient) ListFiles(ctx context.Context, owner, repo, path, ref string) ([]multiforge.FileInfo, error) {
	if _, ok := f.contents[f.key(owner, repo, "Dockerfile")]; ok {
		return []multiforge.FileInfo{{Path: "Dockerfile", SHA: "abc"}}, nil
	}
	return nil, nil
}

func TestScanner_BasicScan(t *testing.T) {
	fc := newFakeClient()
	cfg := &config.Config{
		Forges: map[string]config.ForgeConfig{
			"github": {Backend: "github", Token: "x", User: "inful"},
		},
	}

	s := scanner.New(fc, cfg)
	if err := s.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if fc.listUserCalled != 1 {
		t.Errorf("expected ListUserRepos called once, got %d", fc.listUserCalled)
	}

	if len(s.Store().Repos) != 2 {
		t.Errorf("Repos = %d, want 2", len(s.Store().Repos))
	}
	// 3 images expected: golang:1.22, alpine:3.19 (golang appears in
	// both Dockerfiles but only counts as one image identity).
	if len(s.Store().Images) != 2 {
		t.Errorf("Images = %d, want 2 (got %v)", len(s.Store().Images), imageIDs(s.Store().Images))
	}
	// 3 edges expected: app→golang, app→alpine, worker→golang.
	if len(s.Store().Edges) != 3 {
		t.Errorf("Edges = %d, want 3", len(s.Store().Edges))
	}
}

func TestScanner_PersistsState(t *testing.T) {
	fc := newFakeClient()
	dir := t.TempDir()

	cfg := &config.Config{
		StateDir: dir,
		Forges: map[string]config.ForgeConfig{
			"github": {Backend: "github", Token: "x", User: "inful"},
		},
	}

	s := scanner.New(fc, cfg)
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Read back from disk; confirm the data round-trips.
	loaded, err := store.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Repos) != 2 {
		t.Errorf("repos on disk = %d, want 2", len(loaded.Repos))
	}
}

func TestScanner_AliasLinking(t *testing.T) {
	fc := newFakeClient()
	cfg := &config.Config{
		Forges: map[string]config.ForgeConfig{
			"github": {Backend: "github", Token: "x", User: "inful"},
		},
		Aliases: []config.Alias{
			{Image: "ghcr.io/inful/app:latest", Source: "github.com/inful/app"},
		},
	}

	s := scanner.New(fc, cfg)
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The alias adds an image AND an edge (alias-produces) linking
	// the repo to the image.
	var aliasImg *store.Image
	for i := range s.Store().Images {
		if s.Store().Images[i].ID == "ghcr.io/inful/app:latest" {
			aliasImg = &s.Store().Images[i]
		}
	}
	if aliasImg == nil {
		t.Fatal("alias image not in store")
	}
	if len(aliasImg.LinkedProducers) != 1 || aliasImg.LinkedProducers[0] != "github.com/inful/app" {
		t.Errorf("LinkedProducers = %v", aliasImg.LinkedProducers)
	}

	// Verify the alias edge was added.
	hasAliasEdge := false
	for _, e := range s.Store().Edges {
		if e.FromID == "github.com/inful/app" && e.ToID == "ghcr.io/inful/app:latest" && e.Kind == "alias-produces" {
			hasAliasEdge = true
		}
	}
	if !hasAliasEdge {
		t.Error("expected alias-produces edge from app to ghcr.io/inful/app:latest")
	}
}

func TestScanner_ToleratesGetFileFailure(t *testing.T) {
	fc := newFakeClient()
	// Force the next GetFile call to fail with a transient error.
	fc.failNextGetFile = multiforge.NewError(multiforge.KindTransient, "GetFile", errors.New("network blip"))

	cfg := &config.Config{
		Forges: map[string]config.ForgeConfig{
			"github": {Backend: "github", Token: "x", User: "inful"},
		},
	}

	s := scanner.New(fc, cfg)
	// Scan should still complete; the failed file is just skipped.
	if err := s.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// Worker should still be in the store because we successfully
	// fetched its Dockerfile.
	hasWorker := false
	for _, r := range s.Store().Repos {
		if r.ID == "github.com/inful/worker" {
			hasWorker = true
		}
	}
	if !hasWorker {
		t.Error("worker repo missing after partial failure")
	}
}

func TestScanner_RecordsLastSeen(t *testing.T) {
	fc := newFakeClient()
	cfg := &config.Config{
		Forges: map[string]config.ForgeConfig{
			"github": {Backend: "github", Token: "x", User: "inful"},
		},
	}

	before := time.Now()
	s := scanner.New(fc, cfg)
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	for _, r := range s.Store().Repos {
		if r.LastSeen.Before(before) || r.LastSeen.After(after) {
			t.Errorf("LastSeen %v not in [%v, %v]", r.LastSeen, before, after)
		}
	}
}

// imageIDs is a small helper to format image IDs in error messages.
func imageIDs(images []store.Image) []string {
	out := make([]string, len(images))
	for i, img := range images {
		out[i] = img.ID
	}
	sort.Strings(out)
	return out
}

// stringsContains is a tiny helper to keep test assertions readable.
func stringsContains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// filepathBase is used in some tests to confirm filenames.
func filepathBase(p string) string {
	return filepath.Base(p)
}
