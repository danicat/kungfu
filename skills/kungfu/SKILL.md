---
name: kungfu
description: >
  "I know Kung Fu!": Activate this skill to acquire knowledge about anything
  immediately. It teaches how to discover skills with keywords, JIT load skills into
  context, load reference guides and assets, execute companion scripts, and use
  the KungFu pair-programming shortcut dialect. When encountering unknown commands,
  niche domains, or ::notation, activate kungfu.
license: Apache-2.0
metadata:
  category: agents
  tags: "gateway, agent-skills, discovery, search, catalog, cli, learn, jit"
  author: Daniela Petruzalek (daniela@danicat.dev)
  version: "0.5.0"
  catalog: https://skills.danicat.dev
---

# KungFu: Agent Skills Gateway

`kungfu` is a high-performance CLI gateway that streams specialized Agent Skills, companion reference guides, and executable automation directly into agent context on demand.

### Autonomous Activation Triggers
Activate `kungfu` immediately whenever you encounter:
1. **Shortcut Notation**: The user uses `::notation` (e.g., `::godoctor`, `::go@go-skills`, `::?search`, `::godoctor!test`).
2. **Double Slash Commands**: The user types `//<skill-name>` (e.g., `//godoctor`, `//ebitengineer`).
3. **Unrecognized Commands**: The user runs a command or slash-command not natively present in context.
4. **Specialized Domain Tasks**: Missing domain expertise (e.g., AST refactoring, 2D game physics, procedural audio, SEO audits) where dedicated reference playbooks exist.

---

## 1. 🔍 Search & Discovery (`kungfu find` / `::?`)

`kungfu find` is a discovery tool based on keywords. The hybrid ranking engine (BM25 + TF-IDF) evaluates candidates by relevance to help decide which skill(s) to inspect and load:

```bash
# Keyword discovery
kungfu find game
kungfu find seo
kungfu find audio
kungfu find ast
```

---

## 2. ⚡ Just-In-Time Loading (`kungfu load` / `::skill`)

Stream complete verbatim instructions directly into session context with zero workspace footprint.

> [!IMPORTANT]
> **Buffer Truncation Ceiling & Safe Pagination**:
> - Execution buffers (e.g. `run_command`) enforce an exact **8,192-byte** (8 KiB) ring-buffer limit.
> - Exceeding 8,192 bytes causes **head truncation** (tail retention), silently dropping titles, frontmatter, and initial instructions.
> - PTY newline expansion (`\n` → `\r\n`) consumes 2 bytes per line break.
> - To prevent truncation, `kungfu load` paginates automatically (safe page size: ~6,000 bytes).
> - Loading multiple skills simultaneously on a single command (`kungfu load skillA skillB`) is **disabled** to avoid pagination conflicts. Always load skills individually.

```bash
# Load single skill into context (paginated automatically if large)
kungfu load ebitengineer

# Navigate to subsequent pages
kungfu load ebitengineer -p 2
kungfu load ebitengineer --page 3

# Stream complete unpaginated content (bypass pagination)
kungfu load ebitengineer --full

# Load skill from a specific catalog
kungfu load go@go-skills

# Stream raw unformatted markdown directly for LLM context injection
kungfu load godoctor --raw

# Local cache only / force refresh
kungfu load godoctor --local         # Force local cache only
kungfu load godoctor -u -f           # Force upstream check & reload
```

---

## 3. 📖 Load References & Companion Assets (`kungfu load <skill>/<subpath>`)

Skills often bundle deep architectural guides, schemas, cheat-sheets, and playbooks in subdirectories. Load any specific reference or asset file directly:

```bash
# Load companion reference guides & architecture playbooks
kungfu load godoctor/references/architecture.md
kungfu load buffer/references/pitfalls.md
kungfu load social-copy/references/linkedin_playbook.md

# Load bundled database schemas, configs, or assets
kungfu load buffer-analytics/assets/schema.sql
```

---

## 4. 🚀 Run Automation Scripts (`kungfu run` / `::skill!script`)

Skills can bundle executable automation scripts in `scripts/`.

> [!IMPORTANT]
> **FIRST INSTINCT: ALWAYS USE `kungfu run`**
> - **NEVER search the filesystem or look for script files on disk.**
> - **`kungfu learn` is NOT required.** Scripts are executable immediately on any catalog skill via automatic JIT remote fetching and execution caching.
> - `kungfu run` automatically resolves runtimes (`uv`, `python3`, `bun`, `deno`, `node`, `bash`), fetches companion scripts on demand, sets execution permissions, and runs in the current working directory.

```bash
# Execute automation scripts directly
kungfu run godoctor smart_build
kungfu run buffer sync.py --dry-run
kungfu run godoctor analyze_ast --package ./internal/...
```

---

## 5. 💬 Shortcut Language (Pair-Programming Dialect)

KungFu defines a high-density notation for interactive human-agent pair programming.

### Catalog Scoping (`@catalog`)
- Omitting `@catalog` targets the default catalog: `::godoctor` is equivalent to `::godoctor@default`.
- Specifying `@catalog` targets that catalog: `::go@go-skills` references the `go` skill in the `"go-skills"` catalog.

| Notation | Equivalent Command | Purpose |
|---|---|---|
| `::?keyword` | `kungfu find keyword` | **Discover skills** by keyword |
| `::?keyword@catalog` | `kungfu find keyword` (scoped) | **Discover skills** within a specific catalog |
| `::skill` | `kungfu load skill` | **JIT load** from default catalog (`::skill@default`) |
| `::skill@catalog` | `kungfu load skill@catalog` | **JIT load** from specific catalog (e.g. `::go@go-skills`) |
| `::skill/subpath` | `kungfu load skill/subpath` | **Load reference/asset** (e.g. `::godoctor/references/arch.md`) |
| `::skill!script [args]` | `kungfu run skill script [args]` | **Run companion script** (e.g. `::godoctor!smart_build`) |
| `::*skill` | `kungfu load -u -f skill` | **Force update & reload** from upstream registry |
| `::>skill` | `kungfu show skill` | **Inspect metadata** (author, tools, scripts, versions) |
| `::+skill` | `kungfu learn skill` | **Install to workspace** (`.agents/skills/<skill>/`) |
| `::+skill@catalog` | `kungfu learn skill@catalog` | **Install from specific catalog** to workspace |
| `::++skill` | `kungfu learn -g skill` | **Install globally** (`~/.agents/skills/<skill>/`) |
| `::+*skill` | `kungfu learn -f skill` | **Force overwrite install** to workspace |
| `::++*skill` | `kungfu learn -g -f skill` | **Force overwrite install** globally |
| `::-skill` | `kungfu forget skill` | **Uninstall** skill from workspace |
| `::--skill` | `kungfu forget -g skill` | **Uninstall** skill globally |
| `::^` or `::^_^` | `kungfu update` | **Check/apply updates** to installed skills |
| `::@` | `kungfu catalog list` | **List** registered catalogs |
| `::@^` | `kungfu catalog sync` | **Synchronize** all external catalogs |
| `::@^catalog` | `kungfu catalog sync catalog` | **Synchronize** a specific catalog |

---

## 6. 📦 All The Others: Persistence, Utilities & Administration

### Workspace Persistence (`kungfu learn` / `kungfu add`)
When skills should be permanently committed to the repository:
```bash
# Install to current workspace (.agents/skills/<skill>/)
kungfu learn godoctor

# Install from specific catalog or GitHub repository
kungfu learn go@go-skills
kungfu learn anthropics/anthropic-quickstarts/evals

# Install and stream instructions into context simultaneously
kungfu learn godoctor --load
```

### Secondary Utilities & Administration
```bash
# List available skills
kungfu list

# Inspect skill metadata, author, and tools
kungfu show <skill>

# Check installed skills, versions, and JIT history
kungfu status
kungfu status -g

# Check for updates or sync installed skills
kungfu update
kungfu update --all

# Remove installed skills from workspace or global scope
kungfu forget <skill>
kungfu forget <skill> -g

# Manage external skill catalogs
kungfu catalog add google/skills
kungfu catalog list
kungfu catalog sync
```

### Core Operational Rules
1. **Automation Script First Instinct**: Never crawl disk looking for scripts. Always invoke `kungfu run <skill> <script> [args]`.
2. **JIT Streaming by Default**: Prefer ephemeral `kungfu load` over persistent `kungfu learn` unless permanent repository storage is requested.
