// Package scanner walks one forge, fetches Dockerfiles, and
// extracts dependency edges into a Store.
//
// One Scanner = one forge. The Client passed to New must be bound
// to a single backend; multi-forge configs require callers to
// iterate over cfg.Forges and construct one Scanner per forge, then
// apply aliases once at the end. This separation exists because a
// multiforge.Client is bound to one backend at construction time
// (a single HTTP client cannot talk to both github.com and
// gitlab.com simultaneously).
//
// For each repo at the configured forge:
//
//  1. Fetch the file tree at the default branch.
//  2. Identify candidate Dockerfiles (root-level "Dockerfile", any
//     "*.dockerfile").
//  3. Fetch each Dockerfile and parse its FROM statements.
//  4. Add a repo → image edge for every FROM, plus an image node.
//
// ApplyAliases adds the alias-produces edges that link registry
// images to producing repos. It's exposed as a separate method so
// callers can call it once after scanning all forges (aliases are
// global, not per-forge).
//
// Per-file failures (network blip, decode error, etc.) are reported
// via the error handler if one is set, then swallowed so the scan
// can continue.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/inful/dockdeps/internal/compose"
	"github.com/inful/dockdeps/internal/config"
	"github.com/inful/dockdeps/internal/identity"
	"github.com/inful/dockdeps/internal/parser"
	"github.com/inful/dockdeps/internal/store"
	"github.com/inful/multiforge"
)

// Scanner walks one forge and updates its Store.
type Scanner struct {
	client  multiforge.Client
	forge   config.ForgeConfig // the single forge this scanner is bound to
	cfg     *config.Config     // for aliases and state_dir
	store   *store.Store
	mu      sync.Mutex // protects store mutations from concurrent scan goroutines
	logger  *slog.Logger
	onError func(repo string, err error)
	now     func() time.Time
}

// New constructs a Scanner bound to one forge. The client must be
// configured for that forge; mixing a github.Client with a gitlab
// ForgeConfig (or vice versa) will produce wrong results. The store
// starts empty; callers either run Scan to populate it or call
// Load first to merge into an existing on-disk store.
func New(client multiforge.Client, forge config.ForgeConfig, cfg *config.Config) *Scanner {
	return &Scanner{
		client: client,
		forge:  forge,
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

// Scan walks the configured forge's repos, fetches their Dockerfiles,
// and updates the store. It's safe to call Scan repeatedly; the
// second call merges with whatever Load returned.
//
// Per-file failures (network blip, decode error, etc.) are reported
// via the error handler if one is set, then swallowed so the scan
// can continue.
func (s *Scanner) Scan(ctx context.Context) error {
	now := s.now()
	repos, err := s.client.ListUserRepos(ctx, s.forge.User)
	if err != nil {
		return fmt.Errorf("list repos for user %q: %w", s.forge.User, err)
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

// ApplyAliases adds alias-produces edges to the store for each
// entry in cfg.Aliases. Call this once after all forges have been
// scanned (aliases are global, not per-forge). It's idempotent:
// re-running with the same aliases is a no-op.
func (s *Scanner) ApplyAliases() {
	s.applyAliases(s.now())
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

	// Process compose files first so the repo→image edges from
	// compose are in the store before dockerfile FROMs are
	// resolved (which produces image-image edges). Order doesn't
	// actually affect the graph, but it keeps the data flow
	// predictable for debug logs.
	if err := s.scanComposeFiles(ctx, r, repo.ID, now); err != nil {
		s.reportError(r.FullName, err)
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

// discoverComposeFiles returns candidate docker-compose filenames
// at the repo root. We look for both the canonical
// `docker-compose.yml` and the modern `compose.yaml`/`compose.yml`
// names. The first match wins.
func (s *Scanner) discoverComposeFiles(ctx context.Context, r multiforge.Repository) ([]multiforge.FileInfo, error) {
	entries, err := s.client.ListFiles(ctx, r.Owner, r.Name, "", r.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("list files at root: %w", err)
	}
	candidates := map[string]bool{
		"docker-compose.yml": true,
		"docker-compose.yaml": true,
		"compose.yml":        true,
		"compose.yaml":       true,
	}
	var out []multiforge.FileInfo
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		name := e.Path
		if idx := lastSlash(name); idx >= 0 {
			name = name[idx+1:]
		}
		if candidates[name] {
			out = append(out, e)
		}
	}
	return out, nil
}

// scanComposeFiles fetches each discovered compose file and adds
// service→image edges (Kind="compose-image") to the store.
func (s *Scanner) scanComposeFiles(ctx context.Context, r multiforge.Repository, repoID string, now time.Time) error {
	files, err := s.discoverComposeFiles(ctx, r)
	if err != nil {
		s.reportError(r.FullName, err)
		return nil
	}
	for _, c := range files {
		content, err := s.client.GetFile(ctx, r.Owner, r.Name, c.Path, r.DefaultBranch)
		if err != nil {
			s.reportError(r.FullName, fmt.Errorf("GetFile %s: %w", c.Path, err))
			continue
		}
		cf, err := compose.Parse(string(content))
		if err != nil {
			s.reportError(r.FullName, fmt.Errorf("parse %s: %w", c.Path, err))
			continue
		}
		for _, svc := range cf.Services {
			if svc.Image == "" {
				continue // service uses `build:` without an `image:`; can't attribute
			}
			img, err := identity.Parse(svc.Image)
			if err != nil {
				s.reportError(r.FullName, fmt.Errorf("service %q image %q: %w", svc.Name, svc.Image, err))
				continue
			}
			stored := s.upsertImage(img, now)
			_ = stored // identity already on the image
			s.upsertEdge(store.Edge{
				FromKind: "repo",
				FromID:   repoID,
				ToKind:   "image",
				ToID:     img.ID(),
				Kind:     "compose-image",
				Location: store.Location{
					Repo: repoID,
					File: c.Path,
					Line: 0, // compose parser doesn't track line numbers
				},
				Parameterized: svc.Parameterized,
			})
		}
	}
	return nil
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
// The host part is derived from the Backend field so the ID is
// stable across runs and across Scanner instances that talk to
// different forge deployments.
func repoID(r multiforge.Repository) string {
	var host string
	switch r.Forge {
	case multiforge.GitHub:
		host = "github.com"
	case multiforge.GitLab:
		host = "gitlab.com"
	case multiforge.Forgejo:
		// Forgejo has no canonical host — it's whatever the
		// operator deployed. Use the Backend name as a stable
		// marker; callers that need the real host can read the
		// repository's Forge field elsewhere.
		host = string(r.Forge)
	default:
		host = string(r.Forge) + ".com"
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
