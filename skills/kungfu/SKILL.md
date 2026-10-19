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
  version: "0.5.2"
  catalog: https://skills.danicat.dev
---

# KungFu: Agent Skills Gateway

`kungfu` is a CLI gateway that streams Agent Skills, reference guides, and companion scripts into agent context on demand.

### Activation Triggers
Activate `kungfu` whenever you encounter:
1. **Shortcut Notation**: `::notation` (such as `::godoctor`, `::go@go-skills`, `::?search`, or `::godoctor!test`).
2. **Double Slash Commands**: `//<skill-name>` (such as `//godoctor` or `//ebitengineer`).
3. **Unknown Commands**: A command or workflow not present in session context.
4. **Domain Tasks**: Tasks in domains where a skill playbook exists (such as Go refactoring, 2D game physics, audio synthesis, or SEO audits).

---

## 1. Search & Discovery (`kungfu find` / `::?`)

Use `kungfu find` to search skills by keyword using BM25 and TF-IDF ranking:

```bash
kungfu find game
kungfu find seo
kungfu find audio
kungfu find ast
```

---

## 2. Just-In-Time Loading (`kungfu load` / `::skill`)

Stream skill instructions into session context without installing files to the workspace.

> [!IMPORTANT]
> **Buffer Limit & Pagination**:
> - Command execution buffers cap output at **8,192 bytes** (8 KiB).
> - Output above 8,192 bytes truncates from the top, dropping the title and initial instructions.
> - `kungfu load` paginates automatically (~6,000 bytes per page) to prevent truncation.
> - Load skills one at a time (`kungfu load skillA skillB` is disabled).

```bash
# Load page 1 of a skill
kungfu load ebitengineer

# Read later pages
kungfu load ebitengineer -p 2
kungfu load ebitengineer --page 3

# Stream full unpaginated content
kungfu load ebitengineer --full

# Load from a specific catalog
kungfu load go@go-skills

# Stream raw markdown without frontmatter
kungfu load godoctor --raw

# Read from local cache only or force an upstream refresh
kungfu load godoctor --local
kungfu load godoctor -u -f
```

---

## 3. Load References & Assets (`kungfu load <skill>/<subpath>`)

Load reference guides, playbooks, schemas, or assets bundled inside a skill:

```bash
kungfu load godoctor/references/architecture.md
kungfu load buffer/references/pitfalls.md
kungfu load social-copy/references/linkedin_playbook.md
kungfu load buffer-analytics/assets/schema.sql
```

---

## 4. Run Companion Scripts (`kungfu run` / `::skill!script`)

Run scripts bundled in a skill's `scripts/` directory.

> [!IMPORTANT]
> **Always use `kungfu run`**:
> - Do not search the filesystem for script files.
> - `kungfu learn` is not required; `kungfu run` fetches and caches scripts on demand.
> - `kungfu run` selects the runtime (`uv`, `python3`, `bun`, `deno`, `node`, or `bash`) and runs in the current working directory.

```bash
kungfu run godoctor smart_build
kungfu run buffer sync.py --dry-run
kungfu run godoctor analyze_ast --package ./internal/...
```

---

## 5. Shortcut Dialect (`::notation`)

Omitting `@catalog` targets the default catalog (`::godoctor` equals `::godoctor@default`). Add `@catalog` to target another registered catalog (`::go@go-skills`).

| Notation | Equivalent Command | Purpose |
|---|---|---|
| `::?keyword` | `kungfu find keyword` | Search skills by keyword |
| `::?keyword@catalog` | `kungfu find keyword@catalog` | Search within a catalog |
| `::skill` | `kungfu load skill` | Load a skill from the default catalog |
| `::skill@catalog` | `kungfu load skill@catalog` | Load a skill from a specific catalog |
| `::skill/subpath` | `kungfu load skill/subpath` | Load a reference file or asset |
| `::skill!script [args]` | `kungfu run skill script [args]` | Run a bundled script |
| `::*skill` | `kungfu load -u -f skill` | Force refresh and load from upstream |
| `::>skill` | `kungfu show skill` | Show skill metadata and files |
| `::+skill` | `kungfu learn skill` | Install skill to workspace (`.agents/skills/`) |
| `::+skill@catalog` | `kungfu learn skill@catalog` | Install skill from a catalog to workspace |
| `::++skill` | `kungfu learn -g skill` | Install skill globally (`~/.agents/skills/`) |
| `::+*skill` | `kungfu learn -f skill` | Force overwrite install to workspace |
| `::++*skill` | `kungfu learn -g -f skill` | Force overwrite install globally |
| `::-skill` | `kungfu forget skill` | Uninstall skill from workspace |
| `::--skill` | `kungfu forget -g skill` | Uninstall skill globally |
| `::^` or `::^_^` | `kungfu update` | Check or apply updates to installed skills |
| `::@` | `kungfu catalog list` | List registered catalogs |
| `::@^` | `kungfu catalog sync` | Sync all registered catalogs |
| `::@^catalog` | `kungfu catalog sync catalog` | Sync a specific catalog |

---

## 6. Persistence & Administration

```bash
# Install skills to workspace or global scope
kungfu learn godoctor
kungfu learn go@go-skills
kungfu learn anthropics/anthropic-quickstarts/evals
kungfu learn godoctor --load

# List and inspect skills
kungfu list
kungfu show <skill>

# Check installed skills and usage history
kungfu status
kungfu status -g

# Check for or apply updates
kungfu update
kungfu update --all

# Uninstall skills
kungfu forget <skill>
kungfu forget <skill> -g

# Manage skill catalogs
kungfu catalog add google/skills
kungfu catalog list
kungfu catalog sync

# Manage opt-in anonymous community telemetry
kungfu telemetry status
kungfu telemetry enable
kungfu telemetry disable
kungfu telemetry sync
```

### Core Rules
1. **Run Scripts via `kungfu run`**: Never search disk for skill scripts. Run `kungfu run <skill> <script> [args]`.
2. **Stream by Default**: Use `kungfu load` unless the user asks to install a skill permanently with `kungfu learn`.
