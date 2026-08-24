# 🥋 KungFu (`kungfu`)

[![Go Reference](https://pkg.go.dev/badge/github.com/danicat/kungfu.svg)](https://pkg.go.dev/github.com/danicat/kungfu)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/badge/release-v0.1.0-green.svg)](https://github.com/danicat/kungfu/releases)

> **Fast, offline-first Agent Skills & Open Knowledge CLI Gateway** for autonomous AI coding agents and developers.

`kungfu` enables AI coding assistants and developers to discover, search, load, and execute specialized **Agent Skills** (`SKILL.md`) and companion tools directly from your terminal or multi-agent workflows.

---

## 🚀 Quickstart

### Installation

Install the latest release using Go (1.27+):

```bash
go install github.com/danicat/kungfu@latest
```

Verify your installation:

```bash
kungfu --version
# Output: kungfu version 0.1.0
```

---

## ⚡ Key Features

- **⚡ Sub-Millisecond Search**: Embedded pure-Go search engine supporting **BM25**, **TF-IDF**, and **Reciprocal Rank Fusion (RRF) Hybrid Search**.
- **📦 Zero-Dependency Execution**: Single static binary with zero external runtime dependencies (no Node.js or Python required for core gateway operations).
- **🌐 In-Memory Live Gateway**: Direct in-memory discovery and streaming with zero implicit disk cache clutter.
- **💾 Explicit Persistence**: Skills are permanently learned and persisted to workspace or global configuration only when explicitly instructed (`kungfu learn`).
- **🤖 Autonomous Agent Native**: Verbatim markdown streaming for prompt context injection, clean POSIX stream separation (`stdout` machine data, `stderr` diagnostics), and structured JSON (`--json`).
- **📐 Standards Compliant**: Adheres to the official [`agentskills.io`](https://agentskills.io) metadata specification.
- **💡 "Did You Mean?" Engine**: Levenshtein-based fuzzy suggestions for misspelled skills, categories, tags, scripts, and references.

---

## 📖 Usage & Commands

### 1. Discover Skills (`list`)

List all available skill categories and registered knowledge packages:

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

Perform full-text search across skill names, descriptions, categories, and tags:

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
```

### 4. Load into Context (`load`)

Stream raw, uncompressed `SKILL.md` directly into standard output for immediate LLM context injection:

```bash
# Stream skill content to stdout
kungfu load godoctor

# Load multiple skills simultaneously
kungfu load godoctor deslopify
```

### 5. Install Locally or Globally (`learn`)

Install skills directly into your workspace (`.agents/skills/`) or global home directory (`~/.agents/skills/`):

```bash
# Install to current workspace (.agents/skills/<skill>/)
kungfu learn godoctor

# Install and load instructions into context
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

### 7. Check & Apply Updates (`update`)

Check for upstream updates or sync latest versions with cryptographic integrity validation:

```bash
# Preview available updates (dry-run)
kungfu update

# Update all installed skills in current workspace
kungfu update --all

# Update a specific skill
kungfu update godoctor
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

### 10. Read Companion References (`read`)

Read supplementary reference manuals, API guides, and architecture specifications:

```bash
kungfu read godoctor architecture
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

