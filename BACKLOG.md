# 📋 KungFu Project Backlog

This backlog tracks prioritized architectural enhancements, feature requests, and roadmap milestones for `kungfu`.

---

## ✅ Completed (v0.5.0)

- [x] **Fail-Fast & Zero-Fallback Architecture**: Purged all silent fallback mechanisms, stale cache fallbacks, git branch fallbacks, and legacy compatibility layers across `catalog`, `config`, `state`, and `runner`. Implemented visible, immediate error reporting with rich diagnostics.
- [x] **8 KiB Buffer Ceiling & Safe Pagination**: Added automatic pagination to `kungfu load` (`--full`, `-p`/`--page`, `--page-size`) with rune boundary slicing (`utf8.RuneStart`) and PTY line expansion (`\r\n`) protection, strictly preventing output truncation in agent execution buffers.
- [x] **Stateless JIT Loading**: Removed state modification side effects and disk writes from `kungfu load`, making skill context streaming purely read-only and ephemeral.
- [x] **Single-Skill Loading Enforcement**: Disabled multi-skill batch loading (`kungfu load a b`) with usage error guidance to eliminate pagination interleaving.
- [x] **Decoupled Skill Catalog & Removed Embedding**: Eliminated embedded binaries and special handling for `kungfu` skill. The repository itself is exposed as a GitHub catalog.
- [x] **Security & Concurrency Hardening**: Enforced symlink boundary verification (`isSafeSkillDirectory`), directory ancestor containment checks (`isContained`), runtime interpreter resolution, and panic on environment failures.
- [x] **Google Open Source Compliance**: Added `CONTRIBUTING.md`, `NOTICE`, normalized copyright headers to Google LLC, and sanitized paths.

---

## ✅ Completed (v0.4.0)

- [x] **User Configuration System (`~/.config/kungfu/config.yaml`)**: Added structured configuration (`internal/config`) supporting custom default catalog URLs and `load.auto_update` policy.
- [x] **Default Catalog Tracking in State & Unified Sync**: Registered default catalog into state with clean unified `kungfu catalog sync` across all catalogs (including `default`).
- [x] **Resilient JIT Loading & Refresh Modes**: Enhanced `kungfu load` with `--local`, `-u`/`--update`, and `-f`/`--force` flags, supporting two-phase upstream drift detection and graceful offline cache fallback.
- [x] **Catalog Decoupling**: Decoupled embedded fallback catalog from overriding live or cached catalog items.
- [x] **CLI Simplification & Uno-Reversal**: Removed category filter flags across commands, collapsed `kungfu list` hierarchy to display skills directly, pruned search strategy/threshold flags, removed boolean tag query DSL in favor of keyword ranking, and eliminated redundant `--no-strip` and mass `--all` flags on `load`/`learn`.
- [x] **Streamlined Skill Architecture & Inverted Pyramid**: Restructured `SKILL.md` following high-value JIT workflows (Overview → Search → Load → Reference → Run Script → Shortcut Language → Utilities) with `@catalog` notation support.

---

## ✅ Completed (v0.3.0)

- [x] **Multi-Catalog Management (`kungfu catalog`)**: Added full catalog lifecycle management (`add`, `list`, `remove`, `sync`) supporting GitHub repos (e.g. `google/skills`), HTTP manifests, and local directories.
- [x] **Multi-Standard Catalog Discovery**: Discovery hierarchy prioritizing `catalog.json` (skills.danicat.dev standard) -> `index.json` (google/skills format) -> directory crawl / GitHub Git Tree API (agentskills.io standard).
- [x] **Modular Command Architecture & Codebase Cleanup**: Decomposed monolithic command structure into clean, single-responsibility files under `internal/commands/` without helper anti-patterns or naming stuttering (`internal/catalog/github.go`).
- [x] **Accurate State & Disk Reconciliation**: Enhanced disk reconciler to detect when installed skills are removed from disk outside kungfu, properly synchronizing global and workspace status.
- [x] **Accurate JIT Load History & Metrics**: JIT load count and timestamps accurately record only invoked skills.

---

## ✅ Completed (v0.2.1)

- [x] **Centralized Dynamic Version Resolution**: Implemented `internal/version` package dynamically reading runtime build info from `runtime/debug.ReadBuildInfo()` with fallback default, removing duplicated hardcoded version strings across the codebase.
- [x] **Strict Discovery Schema & Category Alignment**: Cleaned up `Item` schema and category discovery to strictly align with the canonical specification and eliminate legacy metadata fields.
- [x] **Clean Show Formatting**: Streamlined `kungfu show` output formatting.

---

## ✅ Completed (v0.2.0)

- [x] **Native GitHub Ingestion (`npx skills` replacement)**: Support learning (`kungfu learn` / `kungfu add`) and streaming (`kungfu load`) arbitrary skills directly from GitHub (`owner/repo`, `owner/repo/subpath`, `https://github.com/...`, `@tag`, `#branch`, and `raw.githubusercontent.com/...`) via pure raw HTTP with zero git binary, npm/Node.js, or GitHub API token dependencies.
- [x] **`kungfu add` Drop-In Alias**: Added `add` as an alias for `learn` to provide full CLI compatibility with `npx skills add`.
- [x] **Full Provenance & Upstream Drift Tracking**: Enhanced `~/.config/kungfu/state.json` to record GitHub origin (`Origin: "github"`), upstream repository, branch/tag refs, and canonical raw URLs; enhanced `kungfu show` and `kungfu list` to display GitHub provenance.
- [x] **Remote Update & Drift Detection for GitHub Skills**: `kungfu update` checks remote raw endpoints and package digests, alerting developers to upstream changes and preventing accidental overwriting of local customizations.
- [x] **Companion File Caching for GitHub Skills**: `kungfu load` downloads companion `scripts/`, `references/`, and `assets/` to `~/.cache/kungfu/skills/<name>/` on-the-fly.
- [x] **Robust Frontmatter Parsing**: Supports sequence/list format for `allowed-tools` (e.g. `[bash, python]`) as well as string formats.

---

## ✅ Completed (v0.1.1)

- [x] **JIT Remote Script Execution**: Enabled `kungfu run` to execute skill automation scripts immediately after `kungfu load` via on-demand remote caching without requiring permanent installation.
- [x] **Complete Companion File Ingestion**: Scans and installs referenced scripts, references, and assets from markdown links, codeblocks, and runner invocations when learning skills.
- [x] **Execution Environment & Runtime Resolution**: Configured runner to execute from user workspace working directory with `PYTHONPATH`/`NODE_PATH` isolation and multi-runtime support (`uv`, `python3`, `bun`, `deno`, `node`, `bash`).
- [x] **Pure Registry Architecture**: Enforced strict registry ingestion from `skills.danicat.dev` with zero local-to-local copying.

---

## ✅ Completed (v0.1.0)

- [x] **RFC 8615 Standard Endpoint Resolution**: Auto-expansion of origin to `/.well-known/agent-skills/index.json`.
- [x] **Zero Fallbacks & Pure Spec Compliance**: Strict alignment with `https://schemas.agentskills.io/discovery/0.2.0/schema.json`.
- [x] **State Manifest & Lifecycle Management**: Persistent tracking in `~/.config/kungfu/state.json` (`status`, `update`, `forget`).
- [x] **Cryptographic Package Verification**: SHA-256 digest validation on all downloads.
- [x] **Live Registry Caching & HTTP 304 ETags**: Conditional requests with zero-byte responses when unmodified.
- [x] **Sub-Millisecond Hybrid Search Engine**: BM25 + TF-IDF with Reciprocal Rank Fusion (RRF).
- [x] **"Did You Mean?" Suggestion Engine**: Levenshtein distance for fuzzy typos across commands, skills, and categories.

---

## 📌 Future Enhancements / Roadmap

### 1. Progressive Disclosure & Section Paging
- Add section-level paging (`--section <slug>` or anchor `kungfu load skill#section`) for large skills (> 500 lines) to optimize LLM context window token efficiency.

### 2. Skill Dependency Graph Resolution
- Support declarative dependencies in frontmatter metadata (`dependencies: [ebitengineer, procedural-art]`) and transitive loading (`kungfu load --with-deps <skill>`).

