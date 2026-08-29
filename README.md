# 🥋 KungFu (`kungfu`)

[![Go Reference](https://pkg.go.dev/badge/github.com/danicat/kungfu.svg)](https://pkg.go.dev/github.com/danicat/kungfu)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/badge/release-v0.2.1-green.svg)](https://github.com/danicat/kungfu/releases)

> **Fast, zero-dependency Agent Skills & Open Knowledge CLI Gateway** for autonomous AI coding agents and developers.

`kungfu` enables AI coding assistants and developers to discover, search, load, and execute specialized **Agent Skills** (`SKILL.md`) and companion tools directly from your terminal or multi-agent workflows. Compiling to a single standalone binary with zero runtime dependencies, `kungfu` delivers instant Just-In-Time (JIT) skill ingestion, native GitHub repository ingestion (replacing `npx skills add`), full compatibility with the `agentskills.io` standard, discovery from trusted catalogs, and single-command provenance-backed updates.

---

## ⚡ Key Features

- **⚡ Just-In-Time (JIT) Skill Loading**: Stream uncompressed `SKILL.md` instructions, companion references, and assets directly into standard output / LLM agent context on the fly (`kungfu load`) with zero disk clutter or persistent workspace pollution.
- **🐙 Native GitHub Ingestion (`npx skills` Replacement)**: Ingest, learn, and load skills directly from any arbitrary GitHub repository (`owner/repo`, `owner/repo/subpath`, `@tag`, `#branch`, or tree URLs) over pure raw HTTP without requiring Node.js, npm, or system git binaries.
- **🔄 Provenance-Tracked Single-Command Updates**: Per-skill provenance tracking records origin catalog endpoints, upstream GitHub URLs, commit refs, and SHA-256 cryptographic digests—allowing you to audit, verify, and update skills with a single command (`kungfu update` / `kungfu update --all`).
- **📐 `agentskills.io` Catalog Standard Compliant**: 100% compliant with the official [`agentskills.io`](https://agentskills.io) specification, supporting standard RFC 8615 well-known discovery manifests (`/.well-known/agent-skills/index.json`), declarative YAML frontmatter, and canonical taxonomies.
- **🛡️ Trusted Catalog Discovery**: Seamless, secure discovery and cryptographic verification from trusted catalogs (*Note: currently default and supported: [`skills.danicat.dev`](https://skills.danicat.dev)*).
- **📦 Zero Dependencies (Single Static Binary)**: Compiles to a self-contained, standalone Go binary with zero external runtime dependencies (no Node.js, Python, npm, or system daemons required for core gateway operations).
- **🔍 Sub-Millisecond Search Engine**: Embedded pure-Go search engine featuring **BM25**, **TF-IDF**, and **Reciprocal Rank Fusion (RRF) Hybrid Search** with boolean tag expressions (`AND`, `OR`, `NOT`).
- **💾 Explicit Persistence Lifecycle**: Skills are kept in-memory or persisted to workspace (`.agents/skills/`) or global home (`~/.agents/skills/`) only when explicitly instructed (`kungfu learn` / `kungfu add`).
- **🤖 Autonomous Agent Native**: Verbatim markdown streaming for prompt context injection, strict POSIX stream separation (`stdout` machine data, `stderr` diagnostics), and RFC 8259 structured JSON (`--json`).
- **💡 "Did You Mean?" Fuzzy Suggestion Engine**: Levenshtein-based fuzzy suggestions for misspelled skills, categories, tags, companion scripts, and reference documents.

---

## 🚀 Quickstart

### Installation

Install the latest release using Go (1.27+):

```bash
go install github.com/danicat/kungfu@latest
```

Because `kungfu` compiles to a single binary with zero runtime dependencies, you can immediately run it anywhere:

```bash
kungfu --version
# Output: kungfu version 0.2.1
```

---

## 📖 Usage & Commands

### 1. Discover Skills (`list`)

List all available skill categories and registered knowledge packages from trusted `agentskills.io` catalogs:

```bash
# List categories and skills in human or raw mode
kungfu list

# Filter by canonical category
kungfu list -c coding

# Filter with boolean tag expressions
kungfu list -t "golang AND testing"

# Output structured JSON for agent consumption
kungfu list --json
```

### 2. Search Catalog (`find`)

Perform sub-millisecond full-text and hybrid search across skill names, descriptions, categories, and tags:

```bash
# Hybrid Search (RRF combining BM25, TF-IDF, and keyword matching)
kungfu find "refactoring ast"

# Filter by category and search terms
kungfu find "test analytics" -c coding

# Limit results and tune score thresholds
kungfu find "audio synth" --limit 5 --threshold 0.25
```

### 3. Inspect Metadata (`show`)

Inspect comprehensive metadata, tags, author, and documentation paths:

```bash
kungfu show godoctor
kungfu show anthropics/anthropic-quickstarts/evals
```

### 4. Just-In-Time (JIT) Skill Load (`load`)

Stream raw, uncompressed `SKILL.md` instructions, companion references, or assets directly into standard output for instant LLM agent context injection with **zero disk footprint**:

```bash
# Stream catalog skill directly to stdout for immediate context injection
kungfu load godoctor

# Stream directly from arbitrary GitHub repo without installing
kungfu load anthropics/anthropic-quickstarts/evals
kungfu load https://github.com/danicat/kungfu/tree/main/skills/kungfu

# Load multiple skills into context simultaneously
kungfu load godoctor deslopify

# Load companion reference manuals, API guides, or assets via subpath
kungfu load godoctor/references/architecture.md
kungfu load buffer/assets/schema.sql
```

### 5. Install Locally or Globally (`learn` / `add`)

Install skills directly into your workspace (`.agents/skills/`) or global home directory (`~/.agents/skills/`) when persistent workspace access is needed:

```bash
# Install from catalog to current workspace (.agents/skills/<skill>/)
kungfu learn godoctor

# Install directly from GitHub repository (replaces npx skills add)
kungfu learn anthropics/anthropic-quickstarts/evals
kungfu learn owner/repo@v1.2.0
kungfu add danicat/kungfu

# Install and load instructions into context simultaneously
kungfu learn godoctor --load

# Install globally to user home (~/.agents/skills/<skill>/)
kungfu learn godoctor -g

# Overwrite existing installation
kungfu learn godoctor -f
```

### 6. Inspect State & Lifecycle (`status`)

Inspect installed skills, workspace/global paths, versions, and JIT load history:

```bash
# Check installed skills in current workspace
kungfu status

# Check global skills
kungfu status -g

# Output structured JSON
kungfu status --json
```

### 7. Provenance-Aware Single-Command Updates (`update`)

KungFu automatically tracks origin catalog provenance, GitHub repositories, versions, and cryptographic SHA-256 digests in its state manifest. Check for upstream updates or sync latest versions with a single command:

```bash
# Preview available updates against origin catalogs & GitHub (dry-run)
kungfu update

# Update all installed skills in current workspace in a single command
kungfu update --all

# Update a specific skill
kungfu update godoctor
kungfu update evals
```

### 8. Uninstall Skills (`forget`)

Uninstall skills and update state manifest:

```bash
# Uninstall from workspace
kungfu forget godoctor

# Uninstall all workspace skills
kungfu forget --all

# Uninstall global skill
kungfu forget -g kungfu
```

### 9. Run Bundled Scripts (`run`)

Execute companion automation scripts bundled inside skills with automatic runtime detection (`uv`, `python3`, `bun`, `deno`, `node`, `bash`):

```bash
kungfu run godoctor analyze.sh --json
```

### 10. Load Companion References (`load <skill>/<path>`)

Stream supplementary reference manuals, API guides, and architecture specifications:

```bash
kungfu load godoctor/references/architecture.md
kungfu load buffer/assets/schema.sql
```

---

## 🗂️ Taxonomy & Categories

Skills are dynamically organized into domain categories discovered directly from skill packages and discovery manifests, such as:

| Category | Emoji | Scope & Focus |
|---|---|---|
| `game-dev` | 🕹️ | 2D game architecture, Ebitengine, procedural art, chiptune audio, WebAssembly |
| `media` | 🎨 | Multimodal audio synthesis, visual prompts, procedural generators, asset pipelines |
| `coding` | 💻 | Go quality enforcement, AST refactoring, mutation testing, compiler rollback gates |
| `agents` | 🤖 | Agent Skills authoring, evaluation suites, progressive disclosure, swarms |
| `writing` | ✍️ | De-slopification, Inverted Pyramid density, tech review, style guidelines |
| `standards` | 📐 | Request for Comments (RFCs), Architecture Decision Records (ADRs), QA gates |
| `gateway` | 🥋 | Fast discovery, search indexing, live streaming, batch loading |

---

## 🏗️ Architecture

```
                       ┌────────────────────────┐
                       │     kungfu (Root)      │
                       │ (CLI Parsing & Streams)│
                       └───────────┬────────────┘
                                   │
         ┌─────────────────────────┼────────────────────────┐
         │                         │                        │
         ▼                         ▼                        ▼
┌──────────────────┐     ┌──────────────────┐     ┌──────────────────┐
│ internal/search  │     │ internal/runner  │     │  internal/state  │
│ (BM25, TF-IDF,   │     │ (Subprocesses &  │     │ (Manifests, TTL, │
│  RRF Hybrid)     │     │  Ref Documents)  │     │  Soft Deletes)   │
└────────┬─────────┘     └─────────┬────────┘     └────────┬─────────┘
         │                         │                       │
         └─────────────────────────┼───────────────────────┘
                                   ▼
                       ┌────────────────────────┐
                       │    internal/catalog    │
                       │ (Schema, Frontmatter,  │
                       │  HTTP, Cache, Embed)   │
                       └────────────────────────┘
```

---

## 🧪 Testing

Run standard tests and coverage analysis via Go or `godoctor`:

```bash
# Standard Go test
go test -v -race ./...

# With GoDoctor
godoctor call test '{"level": "standard"}'
```

---

## 📄 License

Apache License 2.0. See [LICENSE](LICENSE) for details.

---

## Disclaimer

This is not an officially supported Google product. It is a community-driven, experimental port created for educational and development purposes.

