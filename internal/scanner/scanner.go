// Package scanner walks configured forges, fetches Dockerfiles, and
// extracts dependency edges into a Store.
//
// The scanner is the heart of dockdeps' "scan" command. It takes a
// multiforge.Client and a config; for each forge:
//
//  1. List the user's repos.
//  2. For each repo, fetch the file tree at the default branch and
//     identify candidate Dockerfiles (root-level "Dockerfile", any
//     "*.dockerfile").
//  3. Fetch each Dockerfile and parse its FROM statements.
//  4. Add a repo → image edge for every FROM, plus an image node.
//  5. Apply configured aliases to populate Image.LinkedProducers
//     and add repo → alias-image edges.
//
// Errors during the scan are tolerated per-file: a single failed
// fetch doesn't abort the whole scan. We log the failure (via a
// callback the caller can supply) and continue.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/inful/dockdeps/internal/config"
	"github.com/inful/dockdeps/internal/identity"
	"github.com/inful/dockdeps/internal/parser"
	"github.com/inful/dockdeps/internal/store"
	"github.com/inful/multiforge"
)

// Scanner walks configured forges and updates its Store.
type Scanner struct {
	client  multiforge.Client
	cfg     *config.Config
	store   *store.Store
	mu      sync.Mutex // protects store mutations from concurrent scan goroutines
	logger  *slog.Logger
	onError func(repo string, err error)
	now     func() time.Time
}

// New constructs a Scanner with the given client and config. The
// store starts empty; callers either run Scan to populate it or
// call Load first to merge into an existing on-disk store.
func New(client multiforge.Client, cfg *config.Config) *Scanner {
	return &Scanner{
		client: client,
		cfg:    cfg,
		store:  &store.Store{},
		logger: slog.Default(),
		now:    time.Now,
	}
}

// Store returns the scanner's in-memory store. Useful for tests and
// for CLI commands that want to render without round-tripping
// through disk.
func (s *Scanner) Store() *store.Store {
	return s.store
}

// SetErrorHandler registers a callback invoked for every per-file
// failure. Useful for CLI output; tests typically leave this nil.
func (s *Scanner) SetErrorHandler(fn func(repo string, err error)) {
	s.onError = fn
}

// Load reads any existing state from cfg.StateDir into the scanner's
// in-memory store. The next Scan call will merge with this baseline.
func (s *Scanner) Load() error {
	if s.cfg.StateDir == "" {
		return nil
	}
	loaded, err := store.Load(s.cfg.StateDir)
	if err != nil {
		return fmt.Errorf("scanner: load existing state: %w", err)
	}
	if loaded != nil {
		s.store = loaded
	}
	return nil
}

// Save writes the scanner's in-memory store to cfg.StateDir.
func (s *Scanner) Save() error {
	if s.cfg.StateDir == "" {
		return errors.New("scanner: state_dir not configured")
	}
	return store.Save(s.cfg.StateDir, s.store)
}

// Scan walks each forge in the config, lists repos, fetches
// Dockerfiles, and updates the store. It's safe to call Scan
// repeatedly; the second call merges with whatever Load returned.
//
// Per-file failures (network blip, decode error, etc.) are reported
// via the error handler if one is set, then swallowed so the scan
// can continue.
func (s *Scanner) Scan(ctx context.Context) error {
	now := s.now()
	for name, fc := range s.cfg.Forges {
		if err := s.scanForge(ctx, name, fc, now); err != nil {
			s.reportError(name, fmt.Errorf("scan forge %q: %w", name, err))
		}
	}
	s.applyAliases(now)
	return nil
}

// scanForge processes a single forge: list repos, then scan each.
// The scanner uses the caller-supplied client regardless of which
// forge entry is being processed; this means a single Client can
// only talk to one forge. To scan multiple forges, callers should
// construct one Scanner per Client. (This is what the CLI does.)
func (s *Scanner) scanForge(ctx context.Context, name string, fc config.ForgeConfig, now time.Time) error {
	_ = name
	repos, err := s.client.ListUserRepos(ctx, fc.User)
	if err != nil {
		return fmt.Errorf("list repos for user %q: %w", fc.User, err)
	}

	// Cap concurrency at 4 to be polite to the forge. Each scan is a
	// handful of HTTP requests; 4 in flight keeps us well under any
	// sane rate limit while still being faster than serial.
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, r := range repos {
		wg.Add(1)
		sem <- struct{}{}
		go func(r multiforge.Repository) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.scanRepo(ctx, r, now); err != nil {
				s.reportError(r.FullName, err)
			}
		}(r)
	}
	wg.Wait()
	return nil
}

// scanRepo processes one repo: discover its Dockerfiles, fetch each,
// parse FROM statements, and record edges.
func (s *Scanner) scanRepo(ctx context.Context, r multiforge.Repository, now time.Time) error {
	repo := store.Repo{
		ID:            repoID(r),
		Forge:         string(r.Forge),
		Owner:         r.Owner,
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		LastSeen:      now,
	}

	candidates, err := s.discoverDockerfiles(ctx, r)
	if err != nil {
		s.reportError(r.FullName, err)
		// Even on error we record the repo so the user sees it in
		// `ls repos`. The dockerfiles slice will just be empty.
		s.upsertRepo(repo)
		return nil
	}

	for _, c := range candidates {
		content, err := s.client.GetFile(ctx, r.Owner, r.Name, c.Path, r.DefaultBranch)
		if err != nil {
			s.reportError(r.FullName, fmt.Errorf("GetFile %s: %w", c.Path, err))
			continue
		}
		froms, err := parser.Parse(string(content))
		if err != nil {
			s.reportError(r.FullName, fmt.Errorf("parse %s: %w", c.Path, err))
			continue
		}
		for _, f := range froms {
			img := s.upsertImage(f.Image, now)
			s.upsertEdge(store.Edge{
				FromKind: "repo",
				FromID:   repo.ID,
				ToKind:   "image",
				ToID:     img.ID,
				Kind:     "dockerfile-from",
				Location: store.Location{
					Repo: repo.ID,
					File: c.Path,
					Line: f.Line,
				},
				Parameterized: f.Parameterized,
			})
		}
		repo.Dockerfiles = append(repo.Dockerfiles, store.Dockerfile{
			Path: c.Path,
			SHA:  c.SHA,
		})
	}

	s.upsertRepo(repo)
	return nil
}

// discoverDockerfiles returns the candidate Dockerfile paths in a
// repo at its default branch. The current strategy is conservative:
// we look for "Dockerfile" at the root and "*.dockerfile" at the
// root. Subdirectory Dockerfiles are deferred to a future version.
func (s *Scanner) discoverDockerfiles(ctx context.Context, r multiforge.Repository) ([]multiforge.FileInfo, error) {
	entries, err := s.client.ListFiles(ctx, r.Owner, r.Name, "", r.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("list files at root: %w", err)
	}
	var out []multiforge.FileInfo
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		name := e.Path
		// Strip any leading directory components to handle
		// responses that include them.
		if idx := lastSlash(name); idx >= 0 {
			name = name[idx+1:]
		}
		if name == "Dockerfile" || name == "dockerfile" || hasSuffix(name, ".dockerfile") {
			out = append(out, e)
		}
	}
	return out, nil
}

// applyAliases walks cfg.Aliases and for each entry:
//
//  1. Adds the alias image to the store (if not already there).
//  2. Adds the source repo to the alias image's LinkedProducers.
//  3. Adds a repo → alias-image edge with Kind="alias-produces".
func (s *Scanner) applyAliases(now time.Time) {
	for _, a := range s.cfg.Aliases {
		s.upsertAliasImage(a, now)
	}
}

// upsertAliasImage inserts or updates an image from an alias
// string, populating LinkedProducers and the alias-produces edge.
func (s *Scanner) upsertAliasImage(a config.Alias, now time.Time) {
	img, err := identity.Parse(a.Image)
	if err != nil {
		s.reportError(a.Source, fmt.Errorf("alias %q: %w", a.Image, err))
		return
	}
	stored := s.upsertImage(img, now)
	if !containsString(stored.LinkedProducers, a.Source) {
		stored.LinkedProducers = append(stored.LinkedProducers, a.Source)
	}
	s.upsertEdge(store.Edge{
		FromKind: "repo",
		FromID:   a.Source,
		ToKind:   "image",
		ToID:     stored.ID,
		Kind:     "alias-produces",
	})
}

// repoID constructs a stable forge-native identifier for a repo.
// Format: "<forge-host>/<owner>/<name>" — e.g. "github.com/inful/dockdeps".
// The forge part comes from the Backend field, but for the host we
// default to "github.com" / "gitlab.com" / "forgejo" so the ID is
// stable across runs.
func repoID(r multiforge.Repository) string {
	host := string(r.Forge) + ".com"
	if r.Forge == multiforge.GitHub {
		host = "github.com"
	} else if r.Forge == multiforge.GitLab {
		host = "gitlab.com"
	} else if r.Forge == multiforge.Forgejo {
		host = string(r.Forge)
	}
	return host + "/" + r.Owner + "/" + r.Name
}

// upsertRepo inserts or updates a repo in the store by ID.
func (s *Scanner) upsertRepo(r store.Repo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.store.Repos {
		if s.store.Repos[i].ID == r.ID {
			s.store.Repos[i] = r
			return
		}
	}
	s.store.Repos = append(s.store.Repos, r)
}

// upsertImage inserts or updates an image in the store by ID.
// Returns a pointer to the (possibly newly-inserted) store.Image so
// callers can update fields like LinkedProducers.
func (s *Scanner) upsertImage(img identity.Image, now time.Time) *store.Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := img.ID()
	for i := range s.store.Images {
		if s.store.Images[i].ID == id {
			existing := &s.store.Images[i]
			if existing.FirstSeen.IsZero() {
				existing.FirstSeen = now
			}
			existing.LastSeen = now
			return existing
		}
	}

	s.store.Images = append(s.store.Images, store.Image{
		ID:         id,
		Registry:   img.Registry,
		Repository: img.Repository,
		Tag:        img.Tag,
		Digest:     img.Digest,
		FirstSeen:  now,
		LastSeen:   now,
	})
	return &s.store.Images[len(s.store.Images)-1]
}

// upsertEdge inserts an edge if no edge with the same from/to/kind
// already exists. Duplicates are common when aliases overlap with
// Dockerfile FROMs.
func (s *Scanner) upsertEdge(e store.Edge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.store.Edges {
		if existing.FromID == e.FromID && existing.ToID == e.ToID && existing.Kind == e.Kind {
			return
		}
	}
	s.store.Edges = append(s.store.Edges, e)
}

// reportError routes a scan error to the registered handler (if
// any) and falls back to the default logger at WARN level.
func (s *Scanner) reportError(repo string, err error) {
	if s.onError != nil {
		s.onError(repo, err)
		return
	}
	s.logger.Warn("scanner", "repo", repo, "err", err.Error())
}

// containsString is a small helper for string-slice membership.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// lastSlash returns the index of the last '/' in s, or -1.
func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// hasSuffix is a tiny strings.HasSuffix to avoid an import for one
// caller.
func hasSuffix(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}
