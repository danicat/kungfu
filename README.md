# KungFu (`kungfu`)

[![Go Reference](https://pkg.go.dev/badge/github.com/danicat/kungfu.svg)](https://pkg.go.dev/github.com/danicat/kungfu)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/badge/release-v0.5.1-green.svg)](https://github.com/danicat/kungfu/releases)

A command-line tool for discovering, loading, installing, and managing Agent Skills.

`kungfu` enables developers and AI coding agents to discover, stream, and manage specialized **Agent Skills** (`SKILL.md`) and companion tools. Skills can be streamed dynamically into agent context on demand or installed in workspace and global directories.

---

## Key Features

- **Just-in-Time (JIT) Loading**: Stream skill instructions (`SKILL.md`), reference docs, and companion assets directly to `stdout` for context injection without installing files to disk (`kungfu load`).
- **GitHub and Catalog Ingestion**: Discover and load skills from `agentskills.io` catalog endpoints or directly from GitHub repositories (`kungfu load` / `kungfu learn`).
- **Workspace and Global Management**: Install skills to `.agents/skills/` (workspace) or `~/.agents/skills/` (global), track provenance, check for updates, and remove skills when no longer needed (`kungfu learn`, `kungfu update`, `kungfu forget`).
- **Catalog Search and Filtering**: Search across skill titles, descriptions, categories, and tags to find the right capabilities for your task (`kungfu find`, `kungfu list`).

---

## Installation

Install using Go (1.27+):

```bash
go install github.com/danicat/kungfu@latest
```

Verify the installation:

```bash
kungfu --version
```

---

## Commands

### Discover Skills (`list`)

List available categories and registered skills:

```bash
# List all skills
kungfu list

# Filter by category
kungfu list -c coding

# Filter by tags
kungfu list -t "golang AND testing"

# Output structured JSON
kungfu list --json
```

### Search Skills (`find`)

Search across skill names, descriptions, categories, and tags:

```bash
# Search by keyword
kungfu find "refactoring ast"

# Filter search within a category
kungfu find "testing" -c coding

# Limit results and set score threshold
kungfu find "audio" --limit 5 --threshold 0.25
```

### Inspect Metadata (`show`)

Display metadata, tags, and documentation paths for a skill:

```bash
kungfu show godoctor
kungfu show anthropics/anthropic-quickstarts/evals
```

### Load Instructions (`load`)

Stream raw `SKILL.md` instructions, companion references, or assets to `stdout`:

```bash
# Stream skill instructions to stdout
kungfu load godoctor

# Stream directly from a GitHub repository
kungfu load anthropics/anthropic-quickstarts/evals

# Load from local cache or workspace without remote check
kungfu load godoctor --local

# Force checking remote upstream and updating local cache
kungfu load godoctor --update

# Bypass integrity verification if needed
kungfu load godoctor --update --force

# Stream multiple skills simultaneously
kungfu load godoctor deslopify

# Load a companion reference document or asset
kungfu load godoctor/references/architecture.md
```

### Install Skills (`learn` / `add`)

Install skills locally in the workspace (`.agents/skills/`) or globally (`~/.agents/skills/`):

```bash
# Install to workspace (.agents/skills/<skill>/)
kungfu learn godoctor

# Install from a GitHub repository
kungfu learn anthropics/anthropic-quickstarts/evals

# Install globally to ~/.agents/skills/<skill>/
kungfu learn godoctor -g

# Overwrite existing installation
kungfu learn godoctor -f

# Install and output instructions simultaneously
kungfu learn godoctor --load
```

### Inspect Status (`status`)

View installed skills, paths, versions, and load history:

```bash
# Check workspace skills
kungfu status

# Check global skills
kungfu status -g

# Output as JSON
kungfu status --json
```

### Update Skills (`update`)

Check for upstream updates or sync installed skills with their origin source:

```bash
# Check for available updates (dry run)
kungfu update

# Update all workspace skills
kungfu update --all

# Update a specific skill
kungfu update godoctor
```

### Remove Skills (`forget`)

Uninstall skills from workspace or global directories:

```bash
# Remove from workspace
kungfu forget godoctor

# Remove all workspace skills
kungfu forget --all

# Remove global skill
kungfu forget -g kungfu
```

### Manage Catalogs (`catalog`)

Register, list, remove, and sync entire catalogs of skills from GitHub repositories, HTTP manifest endpoints, or local directories:

```bash
# Add a GitHub skills repository (automatically discovers all SKILL.md packages across the tree)
kungfu catalog add google/skills

# Add a catalog from an HTTP manifest endpoint
kungfu catalog add https://example.com/catalog.json --name custom-catalog

# Add a local directory containing skills
kungfu catalog add ./my-team-skills --name team-skills

# List all registered catalogs and default source
kungfu catalog list

# Synchronize and refresh indexed skills for registered catalogs
kungfu catalog sync

# Remove a registered catalog
kungfu catalog remove google/skills
```

#### Catalog Discovery Order

When discovering skills in a repository or remote host, `kungfu` automatically queries formats in the following precedence order:
1. **`catalog.json`** (`skills.danicat.dev` standard): Explicit manifest containing items, tags, and category metadata.
2. **`index.json`** (`google/skills` standard): Discovery envelope indexing available skills.
3. **Directory Crawl** (`agentskills.io` standard): Recursive file-tree discovery searching for all valid `SKILL.md` bundles.

### Run Companion Scripts (`run`)

Execute companion automation scripts bundled inside a skill (supports Python/uv, Node/Bun/Deno, Shell):

```bash
kungfu run godoctor analyze.sh --json
```

---

## User Configuration

KungFu supports user-level configuration stored at `~/.config/kungfu/config.yaml` (or `.json`):

```yaml
version: 1
catalogs:
  default: https://skills.danicat.dev/catalog.json
load:
  auto_update: true
```

- **`catalogs.default`**: Default catalog URL (defaults to `https://skills.danicat.dev/catalog.json`).
- **`load.auto_update`**: Automatically update cached catalog metadata and downloaded skills when an upstream digest change is detected (default `true`).



---

## Global Flags

| Flag | Description |
|---|---|
| `--json` | Emit RFC 8259 structured JSON |
| `--raw` | Emit verbatim raw markdown / text |
| `--config <path>` | Path to custom configuration file |
| `--cache-dir <dir>` | Custom cache directory (default `~/.cache/kungfu`) |
| `-v`, `--verbose` | Enable verbose diagnostics on `stderr` |
| `--no-color` | Disable ANSI color styling |
| `-V`, `--version` | Display version information |
| `-h`, `--help` | Display help message |

---

## Categories

Skills in the catalog are organized into canonical categories:

| Category | Description |
|---|---|
| `game-dev` | 2D game architecture, Ebitengine, procedural art, audio, WebAssembly |
| `media` | Audio synthesis, visual generation, asset pipelines |
| `coding` | Code quality enforcement, AST refactoring, testing, static analysis |
| `agents` | Agent skills authoring, evaluation suites, swarms, discovery gateway |
| `writing` | Technical writing, review workflows, style guidelines |
| `analytics` | Search console, analytics data warehousing, SQL querying |
| `standards` | RFCs, architecture decision records, QA gates |

---

## Testing

Run unit and integration tests:

```bash
go test -v -race ./...
```

---

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.

---

## Disclaimer

This is not an officially supported Google product.
