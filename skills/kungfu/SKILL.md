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
  version: "0.6.2"
  catalog: https://skills.danicat.dev
---

# KungFu gateway

`kungfu` is a CLI gateway that streams Agent Skills, reference guides, and companion scripts into your session on demand.

> [!CAUTION]
> **DO NOT LEAK KUNGFU NOTATION INTO OTHER SKILLS OR OUTPUT TEXT**:
> **NEVER write, embed, or leak KungFu notation into any other `SKILL.md` definition, `README.md`, documentation file, code comment, commit message, article, or agent output text unless the user explicitly tells you otherwise.**

> [!IMPORTANT]
> **CLI Version Check (`v0.6.2`)**:
> Compare `kungfu --version` against this skill's frontmatter `version` (`0.6.2`). If the installed `kungfu` binary is older than `0.6.2`, upgrade the CLI and synchronize all catalogs immediately:
> ```bash
> go install github.com/danicat/kungfu@latest && kungfu update
> ```

### Activation triggers
Activate `kungfu` whenever you encounter:
1. **Shortcut notation**: `::notation` (such as `::godoctor`, `::go@go-skills`, `::?search`, `::godoctor!test`, `::parallel(10)`, or `::discovery -> ::implementation`).
2. **Double-slash commands**: `//<skill-name>` (such as `//godoctor` or `//ebitengineer`).
3. **Unknown commands**: A command or workflow not present in your active session.
4. **Domain tasks**: Tasks in domains where a skill playbook exists (such as Go refactoring, 2D game physics, audio synthesis, or SEO audits).

---

## 1. Search and discovery (`kungfu find` / `::?`)

Use `kungfu find` to search skills by keyword using BM25 and TF-IDF ranking:

```bash
kungfu find game
kungfu find seo
kungfu find audio
kungfu find ast
```

---

## 2. Just-in-time loading (`kungfu load` / `::skill`)

Stream skill instructions into your session without saving files to the workspace.

> [!IMPORTANT]
> **Buffer limit and pagination**:
> - Command execution buffers cap output at **8,192 bytes** (8 KiB).
> - Output over 8,192 bytes truncates from the top, dropping the title and initial instructions.
> - `kungfu load` paginates automatically (~6,000 bytes per page) to prevent truncation.
> - Load skills one at a time (`kungfu load skillA skillB` returns an error).

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

## 3. Load references and assets (`kungfu load <skill>/<subpath>`)

Load reference guides, playbooks, schemas, or assets bundled inside a skill:

```bash
kungfu load godoctor/references/architecture.md
kungfu load buffer/references/pitfalls.md
kungfu load social-copy/references/linkedin_playbook.md
kungfu load buffer-analytics/assets/schema.sql
```

---

## 4. Run companion scripts (`kungfu run` / `::skill!script`)

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

## 5. Shortcut dialect (`::notation`)

KungFu uses a compact shortcut notation for human-agent pair programming. When the user writes `::notation` or `//skill` in a prompt, run the matching `kungfu` CLI commands and workflow steps below.

### Shortcut reference table

| Notation | Command | Purpose |
|---|---|---|
| `::?keyword` | `kungfu find keyword` | **Discover skills** by keyword using hybrid BM25 + TF-IDF search |
| `::?keyword@catalog` | `kungfu find keyword@catalog` | **Discover skills** within a specific catalog |
| `::skill` *(or `//skill`)* | `kungfu load skill` | **JIT load** a skill from the default catalog (`::skill@default`) |
| `::skill@catalog` | `kungfu load skill@catalog` | **JIT load** a skill from a specific catalog (such as `::go@go-skills`) |
| `::skill/subpath` | `kungfu load skill/subpath` | **Load a reference or asset** (such as `::godoctor/references/architecture.md`) |
| `::skill!script [args]` | `kungfu run skill script [args]` | **Run a companion script** (such as `::godoctor!smart_build`) |
| `::skill(params...)` | Load + specialize skill | **Specialize a skill** with runtime arguments, worker counts, or inner skills |
| `A -> B` | Sequential chain | **Chain** skills and slash commands left-to-right |

### Grammar and composition rules

1. **Catalog and path scoping (`@catalog` and `/subpath`)**:
   - Omitting `@catalog` uses the default catalog: `::godoctor` equals `::godoctor@default`.
   - Adding `@catalog` selects a named catalog: `::go@go-skills` loads `go` from `go-skills`.
   - Adding `/subpath` loads a file from the skill: `::godoctor/references/architecture.md`.
   - If a name doesn't match an exact skill, `kungfu load` returns the top 5 fuzzy and hybrid `Did you mean` matches.
2. **Skill arguments (`::skill(params...)`)**:
   - Use `()` to pass runtime flags, worker counts, tier hints, workspace modes, or inner skills.
   - Values inside `()` have no fixed order: `::parallel(10)`, `::double-diamond(10)`, and `::parallel(10, ::discovery)` (which equals `::parallel(::discovery, 10)`).
   - If arguments inside `()` are ambiguous (such as `::parallel(10, 20)`), ask the user to clarify before proceeding.
   - Common values include worker counts (`10`), tier or effort flags (`flash, low`), workspace modes (`branch`, `share`), and inner skills (`::discovery`).
3. **Sequential chains (`A -> B`)**:
   - The `->` arrow chains skills and slash commands left-to-right into a multi-stage flow:
     `::discovery(10) -> /grill-me -> ::implementation(5)`
   - Finish each stage in order before starting the next stage.
4. **First-mention hint rule**:
   - The `::` (or `//`) prefix is a JIT-loading trigger needed only on the first mention of a skill in a prompt.
   - Once you name a skill in a prompt, later mentions in that prompt or chain don't need the `::` prefix (such as `::parallel(10, ::discovery) -> /grill-me -> parallel(5, implementation)`).

---

## 6. Persistence and administration

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

# Synchronize catalogs, flush telemetry, and check or apply skill updates
kungfu update
kungfu update --all

# Uninstall skills
kungfu forget <skill>
kungfu forget <skill> -g

# Manage skill catalogs
kungfu catalog add google/skills
kungfu catalog list
kungfu catalog remove google/skills

# Manage opt-in anonymous community telemetry
kungfu telemetry status
kungfu telemetry enable
kungfu telemetry disable
kungfu telemetry sync
```

### Core rules
1. **Run scripts via `kungfu run`**: Never search disk for skill scripts. Run `kungfu run <skill> <script> [args]`.
2. **Stream by default**: Use `kungfu load` unless the user asks to install a skill permanently with `kungfu learn`.
