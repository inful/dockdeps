# dockdeps

`dockdeps` is a CLI for tracking Docker image dependencies across repositories on multiple forges (GitHub, GitLab, Forgejo/Gitea). It scans your repos, parses every `Dockerfile` it finds, extracts the `FROM` lines, and builds a queryable dependency graph that lives in `~/.local/share/dockdeps/state/`.

```
$ dockdeps tree docker.io/library/golang:1.22
image: docker.io/library/golang:1.22
├─ repo: github.com/inful/app
└─ repo: github.com/inful/worker

$ dockdeps dependents docker.io/library/golang:1.22
github.com/inful/app
github.com/inful/worker

$ dockdeps graph --format json | jq '.nodes | length'
4
```

## Install

```bash
go install github.com/inful/dockdeps/cmd/dockdeps@latest
```

Or build from source:

```bash
git clone https://github.com/inful/dockdeps
cd dockdeps
go build -o dockdeps ./cmd/dockdeps
```

Pre-built binaries for Linux, macOS, and Windows (amd64 + arm64) are published at https://github.com/inful/dockdeps/releases.

## Releasing

Releases are automated via [goreleaser](https://goreleaser.com). Push a `vX.Y.Z` tag and the GitHub Actions workflow at `.github/workflows/release.yml` builds, signs (via checksums), and publishes:

- Cross-platform binaries (linux/darwin/windows × amd64/arm64)
- A Docker image to GHCR (`ghcr.io/inful/dockdeps:vX.Y.Z` and `:latest`)
- A debug-variant Docker image with busybox shell (`-debug` tags)

Local dry-run:

```bash
goreleaser release --snapshot --clean --skip=publish,docker
# inspect dist/
```

## Quickstart

```bash
# 1. Scaffold the config and state directories.
dockdeps init

# 2. Edit the config: ~/.config/dockdeps/config.yaml.
#    Set GITHUB_TOKEN (or use ${GITHUB_TOKEN}).
$EDITOR ~/.config/dockdeps/config.yaml

# 3. Walk your forges and populate the dependency graph.
dockdeps scan

# 4. Query it.
dockdeps dependents <image-or-repo>
dockdeps dependencies <image-or-repo>
dockdeps tree <image-or-repo>
dockdeps ls images
dockdeps ls repos
dockdeps graph --format json
```

## Commands

| Command | Description |
|---|---|
| `init` | Scaffold the config file and state directory at `~/.config/dockdeps/` and `~/.local/share/dockdeps/state/` |
| `scan` | Walk each configured forge, fetch Dockerfiles and `docker-compose.yml`, and update the dependency graph |
| `ls [repos\|images]` | List known entities from the on-disk state |
| `dependents <id>` | Show repos that depend on the given image or repo |
| `dependencies <id>` | Show what the given image or repo depends on |
| `tree <id> [--depth N]` | Render a tree of dependencies rooted at the given node |
| `graph --format json\|dot\|mermaid\|html` | Export the full graph in one of several formats |
| `aliases list` | Print all configured image→repo aliases |
| `aliases add <image> <source>` | Append a new alias and save the config |
| `doctor` | Check configuration and forge setup |
| `version` | Print the dockdeps version |

## Graph formats

`dockdeps graph` produces the dependency graph in one of four formats:

| Format | Use case |
|---|---|
| `json` | Machine-readable; downstream tooling can transform into anything else |
| `dot` | Graphviz — render with `dot -Tsvg graph.dot > graph.svg` |
| `mermaid` | Markdown-friendly; renders inline on GitHub, GitLab, etc. |
| `html` | Self-contained HTML page with inline SVG; no JavaScript or external resources |

## Configuration

The config file is YAML with `${ENV}` interpolation applied at load time:

```yaml
version: 1
state_dir: ~/.local/share/dockdeps/state  # default

forges:
  github:
    backend: github
    token: "${GITHUB_TOKEN}"
    user: inful
  gitlab:
    backend: gitlab
    base_url: https://gitlab.com
    token: "${GITLAB_TOKEN}"
    user: inful
  forgejo:
    backend: forgejo
    base_url: https://git.example.com
    token: "${FORGE_TOKEN}"
    user: inful

# Optional: aliases that link registry images to their producing
# repos. Useful when CI pushes images with tags the Dockerfile
# can't know about statically.
aliases:
  - image: ghcr.io/inful/app
    source: github.com/inful/app

# Optional: file filters.
filters:
  exclude_paths:
    - "**/node_modules/**"
  max_dockerfile_size: 65536
```

## State files

After `dockdeps scan`, three files appear in `state_dir`:

```
repos.yaml    # one document per source repo
images.yaml   # one document per image identity
edges.yaml    # flat list of dependency edges
```

They're plain YAML so you can grep, diff, and version-control them.

## Identity model

dockdeps has two node types: **repos** (sources) and **images** (dependencies). An image is identified by `[REGISTRY/]REPOSITORY[:TAG][@DIGEST]` and normalised so that "alpine" and "docker.io/library/alpine:latest" produce the same identity. Parameterised references (`FROM golang:${VERSION}`) are kept verbatim and flagged so the graph marks the corresponding edge as incomplete.

The two identity systems (registry + repo) are linked via aliases. An alias `{ image: "ghcr.io/me/app", source: "github.com/me/app" }` tells dockdeps that the registry image is produced by the named repo, which the scanner then surfaces on `Image.LinkedProducers`.

## Architecture

```
dockdeps/
├── cmd/dockdeps/        # kong CLI entrypoint
├── internal/
│   ├── identity/        # image reference parser
│   ├── parser/          # Dockerfile parser (FROM, AS, --platform)
│   ├── config/          # YAML config + ${ENV} interpolation
│   ├── store/           # YAML state persistence (atomic write)
│   ├── graph/           # queryable dependency graph
│   ├── render/          # tree + JSON output
│   └── scanner/         # orchestrates multiforge + parser + store
└── go.mod
```

`dockdeps` depends on [`github.com/inful/multiforge`](https://github.com/inful/multiforge) for forge interaction.

## License

GPL-3 — same as the rest of the `inful/*` Go tool family.
