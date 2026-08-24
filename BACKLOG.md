# 📋 KungFu Project Backlog

This backlog tracks prioritized architectural enhancements, feature requests, and roadmap milestones for `kungfu`.

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

