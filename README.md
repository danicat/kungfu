# KungFu (`kungfu`)

[![Go Reference](https://pkg.go.dev/badge/github.com/danicat/kungfu.svg)](https://pkg.go.dev/github.com/danicat/kungfu)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/badge/release-v0.5.2-green.svg)](https://github.com/danicat/kungfu/releases)

`kungfu` is a command-line tool for discovering, streaming, running, and installing **Agent Skills** (`SKILL.md`). Coding agents can stream skill instructions and reference files into context on demand (`kungfu load`) or install them into workspace and global directories (`kungfu learn`).

---

## Installation

Install with Go 1.27 or newer:

```bash
go install github.com/danicat/kungfu@latest
```

Verify the installation:

```bash
kungfu --version
```

---

## Commands

### Search Skills (`find`)

Search across skill names, descriptions, categories, and tags:

```bash
# Search by keyword
kungfu find "refactoring ast"

# Limit results
kungfu find "audio" --limit 5

# Search within a specific catalog
kungfu find "testing@go-skills"
```

### Load Instructions (`load`)

Stream `SKILL.md` instructions, references, or assets to `stdout`. Output pages at ~6,000 bytes to fit within 8 KiB agent buffers:

```bash
# Stream skill instructions (page 1)
kungfu load godoctor

# Read subsequent pages or stream full content
kungfu load godoctor -p 2
kungfu load godoctor --full

# Stream directly from a GitHub repository
kungfu load anthropics/anthropic-quickstarts/evals

# Load from local cache only or force an upstream refresh
kungfu load godoctor --local
kungfu load godoctor --update --force

# Load a companion reference document or asset
kungfu load godoctor/references/architecture.md
```

### Run Companion Scripts (`run`)

Run scripts bundled inside a skill (`uv`, `python3`, `bun`, `deno`, `node`, or `bash`):

```bash
kungfu run godoctor analyze.sh --json
```

### List & Inspect Skills (`list` / `show`)

List available skills or inspect metadata, references, and scripts for a skill:

```bash
# List all available skills
kungfu list
kungfu list --json

# Show metadata and bundled files for a skill
kungfu show godoctor
kungfu show anthropics/anthropic-quickstarts/evals
```

### Install Skills (`learn` / `add`)

Install skills to the workspace (`.agents/skills/`) or globally (`~/.agents/skills/`):

```bash
# Install to workspace (.agents/skills/<skill>/)
kungfu learn godoctor

# Install from a GitHub repository
kungfu learn anthropics/anthropic-quickstarts/evals

# Install globally to ~/.agents/skills/<skill>/
kungfu learn godoctor -g

# Overwrite existing installation
kungfu learn godoctor -f

# Install and stream instructions simultaneously
kungfu learn godoctor --load
```

### Inspect Status (`status`)

View installed skills, versions, and local usage history:

```bash
kungfu status
kungfu status -g
kungfu status godoctor
kungfu status --json
```

### Update & Remove Skills (`update` / `forget`)

Check for upstream updates or uninstall skills:

```bash
# Check for available updates (dry run)
kungfu update

# Apply updates
kungfu update --all
kungfu update godoctor

# Remove skills from workspace or global scope
kungfu forget godoctor
kungfu forget --all
kungfu forget -g kungfu
```

### Manage Catalogs (`catalog`)

Add, list, remove, and sync skill catalogs from GitHub repos, HTTP manifests, or local folders:

```bash
# Add a GitHub skills repository
kungfu catalog add google/skills

# Add an HTTP manifest or local directory
kungfu catalog add https://example.com/catalog.json --name custom-catalog
kungfu catalog add ./my-team-skills --name team-skills

# List, sync, or remove catalogs
kungfu catalog list
kungfu catalog sync
kungfu catalog remove google/skills
```

When indexing a catalog source, `kungfu` checks three formats in order:
1. `catalog.json`: `skills.danicat.dev` manifest with items, tags, and digests.
2. `index.json`: `google/skills` index file.
3. Directory tree: `agentskills.io` folder scan for `SKILL.md` files.

### Opt-In Community Telemetry (`telemetry`)

`kungfu` supports opt-in anonymous usage counters for official `skills.danicat.dev` catalog skills (`loads`, `page_loads`, `subpath_loads`, `runs`, `learns`). It excludes custom, local, and third-party skills. `kungfu load` never makes network calls for telemetry; `kungfu catalog sync` and `kungfu telemetry sync` flush pending counter deltas:

```bash
# View telemetry status and pending official skill deltas
kungfu telemetry status

# Opt in (baselines existing local history so only future usage is sent)
kungfu telemetry enable

# Flush pending counter deltas to Cloud Firestore immediately
kungfu telemetry sync

# Opt out at any time
kungfu telemetry disable
```

---

## User Configuration

`kungfu` reads user settings from `~/.config/kungfu/config.yaml` (or `.json`):

```yaml
catalogs:
  default: https://skills.danicat.dev/catalog.json
load:
  auto_update: true
telemetry:
  enabled: false
```

| Key | Default | Description |
|---|---|---|
| `catalogs.default` | `https://skills.danicat.dev/catalog.json` | Default catalog URL |
| `load.auto_update` | `true` | Refresh cached skills when the upstream digest changes |
| `telemetry.enabled` | `false` | Opt in to anonymous counters for official skills (`DO_NOT_TRACK=1` and `KUNGFU_TELEMETRY=0\|1` override) |

---

## Global Flags

| Flag | Description |
|---|---|
| `--json` | Emit JSON output |
| `--raw` | Emit raw tab/text output |
| `--config <path>` | Custom configuration file path |
| `--cache-dir <dir>` | Custom cache directory (default `~/.cache/kungfu`) |
| `-v`, `--verbose` | Verbose diagnostics on `stderr` |
| `--no-color` | Disable ANSI color styling |
| `-V`, `--version` | Print version |
| `-h`, `--help` | Show help |

---

## Testing

Run the test suite with the race detector enabled:

```bash
go test -v -race ./...
```

---

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.

---

## Disclaimer

This is not an officially supported Google product.
