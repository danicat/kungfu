# KungFu

[![Go Reference](https://pkg.go.dev/badge/github.com/danicat/kungfu.svg)](https://pkg.go.dev/github.com/danicat/kungfu)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/danicat/kungfu)](https://github.com/danicat/kungfu/releases)

`kungfu` is a command-line tool for finding, streaming, running, and installing **Agent Skills** (`SKILL.md`). Coding agents can stream skill instructions and reference files into a session on demand (`kungfu load`) or install them into workspace and global folders (`kungfu learn`).

`kungfu` implements the [Cloudflare Agent Skills Discovery RFC (`v0.2.0`)](https://github.com/cloudflare/agent-skills-discovery-rfc) (`/.well-known/agent-skills/index.json`) from [Cloudflare's Agent Readiness guide](https://blog.cloudflare.com/agent-readiness/). It loads single-file (`type: "skill-md"`) and packaged `.tar.gz` or `.zip` (`type: "archive"`) skills and verifies their `sha256` digests.

## Installation

Install with Go 1.27 or newer:

```bash
go install github.com/danicat/kungfu@latest
```

Verify the installation:

```bash
kungfu --version
```

## Commands

### Search skills (`find`)

Search across skill names, descriptions, and tags using hybrid BM25 and fuzzy matching:

```bash
# Search by keyword
kungfu find "refactoring ast"

# Limit results
kungfu find "audio" --limit 5

# Search within a specific catalog (full name or unique short repo name)
kungfu find "testing@spf13/go-skills"
kungfu find "testing@go-skills"
```

### Load instructions (`load`)

Stream `SKILL.md` instructions, references, or assets to `stdout`. Output paginates at ~6,000 bytes to fit within 8 KiB agent buffers, and cached skills in `~/.cache/kungfu/skills/` are served from disk whenever their `sha256` digest matches the catalog index:

```bash
# Stream skill instructions (page 1)
kungfu load godoctor

# Read later pages or stream the entire file unpaginated
kungfu load godoctor -p 2
kungfu load godoctor --full

# Stream raw markdown including YAML frontmatter (unpaginated unless -p or --page-size is set)
kungfu load godoctor --raw

# Load from a specific catalog (full name or unique short repo name)
kungfu load go@spf13/go-skills
kungfu load go@go-skills

# Load from local cache only (offline-safe) or force an upstream refresh
kungfu load godoctor --local
kungfu load godoctor -u -f

# Load a companion reference document or asset (optionally scoped to a catalog)
kungfu load godoctor/references/architecture.md
kungfu load go/references/guide.md@spf13/go-skills
```

### Run companion scripts (`run`)

Run scripts or executables bundled inside a skill's `scripts/` directory (`uv`, `python3`, `bun`, `deno`, `tsx`, `node`, `bash`, `sh`, or compiled binaries):

```bash
kungfu run godoctor analyze.sh --json
```

### List and inspect skills (`list` / `show`)

List available skills or inspect metadata, references, and scripts for one or more skills (`show --json` always returns a JSON array):

```bash
# List all available skills
kungfu list
kungfu list --json

# Show metadata and bundled files for a skill
kungfu show godoctor
kungfu show go@go-skills --json
```

### Install skills (`learn`)

Save skills to the workspace (`.agents/skills/`) or globally (`~/.agents/skills/`):

```bash
# Install to workspace (.agents/skills/<skill>/)
kungfu learn godoctor

# Install from a specific catalog or GitHub repository
kungfu learn go@go-skills
kungfu learn danicat/kungfu

# Install globally to ~/.agents/skills/<skill>/
kungfu learn godoctor -g

# Overwrite an existing installation
kungfu learn godoctor -f
```

### Inspect status (`status`)

View installed skills, versions, and (when local telemetry is enabled) local usage counts:

```bash
kungfu status
kungfu status -g
kungfu status godoctor
kungfu status --json
```

### Update and remove skills (`update` / `forget`)

Sync all catalogs and check or apply skill updates:

```bash
# Synchronize all catalogs and check for available skill updates (dry run)
kungfu update

# Synchronize all catalogs and apply updates to installed skills
kungfu update --all
kungfu update godoctor
kungfu update -g -f godoctor

# Remove skills from workspace or global scope
kungfu forget godoctor
kungfu forget --all
kungfu forget -g -f kungfu
```

### Manage catalogs (`catalog`)

Add, list, and remove skill catalogs from GitHub repositories, HTTPS manifests, or local folders. Two built-in catalogs (`default` and `kungfu`) are always registered and cannot be removed:

```bash
# Add a GitHub skills repository (optionally pinning a ref or subpath)
kungfu catalog add google/skills
kungfu catalog add owner/repo --ref v1.2.0 --subpath skills/go --name go-skills

# Add an HTTPS manifest or local directory
kungfu catalog add https://example.com/.well-known/agent-skills/index.json --name custom-catalog
kungfu catalog add ./my-team-skills --name team-skills

# List or remove catalogs
kungfu catalog list
kungfu catalog remove google/skills
```

When indexing a catalog source, `kungfu` checks three formats in order:
1. `.well-known/agent-skills/index.json`: [Cloudflare Agent Skills Discovery RFC v0.2.0](https://github.com/cloudflare/agent-skills-discovery-rfc) manifest (`skills` array with `skill-md` or `archive` entries and SHA-256 digests; see [Is your site agent-ready?](https://blog.cloudflare.com/agent-readiness/)).
2. `index.json`: `google/skills` index file.
3. Directory tree: `agentskills.io` folder scan for `SKILL.md` files.

## User configuration

`kungfu` reads user settings from `~/.config/kungfu/config.yaml` (or `$XDG_CONFIG_HOME/kungfu/config.yaml`):

```yaml
catalogs:
  default: https://skills.danicat.dev/.well-known/agent-skills/index.json
  kungfu: danicat/kungfu
telemetry:
  enabled: false
```

| Key | Default | Description |
|---|---|---|
| `catalogs.default` | `https://skills.danicat.dev/.well-known/agent-skills/index.json` | Default catalog URL |
| `catalogs.kungfu` | `danicat/kungfu` | Built-in `kungfu` skill catalog |
| `telemetry.enabled` | `false` | Opt-in local usage counters (`load`, `run`, `learn`) stored only in local `state.json` (never sent over the network); can also be toggled with `KUNGFU_TELEMETRY=1` or `0` |

## Global flags

| Flag | Description |
|---|---|
| `--json` | Emit JSON output |
| `--config <path>` | Custom config file path |
| `--cache-dir <dir>` | Custom cache folder (default `~/.cache/kungfu`) |
| `-v`, `--verbose` | Verbose diagnostics on `stderr` |
| `-V`, `--version` | Print version |
| `-h`, `--help` | Show help |

## Testing

Run the test suite with the race detector enabled:

```bash
go test -v -race ./...
```

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.

---

## Disclaimer

This is not an officially supported Google product.
