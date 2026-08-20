# Alpha Release Readiness Review Specification (v0.1.0-alpha)

- Date: 2026-08-20
- Target Version: `v0.1.0-alpha`
- Total Swarm Budget: 50 Agents
- Objective: Comprehensive Security, Privacy, Architecture, Quality, Concurrency, and Release Readiness Audit for `kungfu` (Agent Skills & Open Knowledge Gateway CLI).

---

## 1. Review Domains & Org Chart

```
                        [Swarm Coordinator - ROOT]
                       /       |          |       \
                      /        |          |        \
     [Lead Security]   [Lead Arch]    [Lead Quality]  [Lead QA & Release]
       (Budget: 12)    (Budget: 12)    (Budget: 12)       (Budget: 12)
```

### Domain 1: Security & Privacy (Lead Security & Privacy Engineer)
- **Zero Contamination Compliance**: Verify no corporate/internal workflows, private keys, tokens, or hardcoded personal machine paths.
- **Cache & File Security**: Audit cache file permissions (`0755` vs `0700` dirs, `0644` vs `0600` files), atomic temp file writes, and directory traversal safeguards.
- **Runner Sandbox & Subprocess Safety**: Audit `internal/runner` execution, stdin/stdout/stderr pipes, timeout context handling, and environment isolation.

### Domain 2: Architecture & Design (Lead Architecture & Design Engineer)
- **Layer & Boundary Separation**: Enforce unidirectional dependencies (`cmd/kungfu` -> `internal/catalog`, `internal/search`, `internal/compress`, `internal/runner`).
- **HTTP & Cache Protocol**: Conditional HTTP request flow (`ETag`, `If-Modified-Since`, `304 Not Modified`), cache staleness, offline fallback.
- **Schema & Gateway Standardization**: `agentskills.io/schema/v1` and Open Knowledge Format conformance, declarative metadata model.
- **CLI Ergonomics & Exit Hierarchy**: Consistent flags, POSIX compliance, standard streams (stdout for content/pipes, stderr for logs/suggestions), standard exit code enum.

### Domain 3: Quality & Concurrency (Lead Systems & Concurrency Engineer)
- **Thread Safety & Mutex Locks**: Audit search engine inverted index thread-safety, catalog state, and concurrent reader/writer locks.
- **Resource Management**: Audit HTTP response body closing, OS file descriptor cleanup, temporary file deletion, subprocess cleanup.
- **Error Handling & Idioms**: Proper `%w` wrapping, sentinel errors (`errors.Is`), no panics in library code, clean error propagation.
- **Memory & Allocation Performance**: Pre-allocated slice capacities, pre-compiled regexes at package level, string builder buffers.

### Domain 4: QA & Release Readiness (Lead QA & Release Readiness Engineer)
- **Test Coverage & Edge Cases**: Validate code coverage across all packages (minimum 75%+ gate), boundary conditions, empty query behavior, network error simulation.
- **Cross-Platform Compatibility**: Path separator handling (`filepath.ToSlash` / `filepath.Join`), Windows/Linux/macOS home directory discovery.
- **Alpha Milestone Checklist**: Evaluate binary size, CLI startup latency, help text clarity, documentation integrity (`README.md`, `SKILL.md`), and Go module hygiene.
