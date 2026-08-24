# Swarm Deep Code Review Specification (v0.1.0)

- Date: 2026-08-24
- Methodology: Swarm Coding Hierarchical Multi-Agent Framework
- Swarm Budget: 10 Agents
- Objective: Exhaustive, multi-dimensional code review of `kungfu` covering CLI UX, state persistence, catalog networking & schema, hybrid search algorithms, script runner sandboxing, cross-platform security, race conditions, test coverage, and 100% SPEC-0001 compliance.

---

## 1. Swarm Org Chart & Delegation Topology

```
                         [Swarm Coordinator - ROOT]
                        /            |             \
                       /             |              \
           [Lead CLI & State] [Lead Catalog & Search] [Lead Runner & Security QA]
              (Budget: 3)         (Budget: 3)               (Budget: 3)
              /         \          /        \                 /          \
        [Spec CLI]  [Spec State] [Spec Cat] [Spec Search]  [Spec Runner] [Spec Sec/QA]
```

### Allocation Breakdown:
- **ROOT (Swarm Coordinator)**: Budget = 1. Architecture, task coordination, specification authoring, Reduce-phase reconciliation, final audit synthesis.
- **Lead 1: CLI, State & Lifecycle (`lead-cli-state`)**: Budget = 3.
  - `spec-cli-ux`: CLI commands, flag handling, output formatters, exit codes, UX ergonomics.
  - `spec-state-concurrency`: State store, atomic lock safety, 3-way dirty state checks, digest tracking.
- **Lead 2: Catalog, Network & Search Engine (`lead-catalog-search`)**: Budget = 3.
  - `spec-catalog-engine`: Discovery protocol (`/.well-known/agent-skills/index.json`), HTTP caching, ETag, SHA-256 verification, frontmatter & tag evaluation.
  - `spec-search-engine`: Inverted index, BM25 scoring, TF-IDF, RRF fusion, tokenization, Levenshtein suggestions.
- **Lead 3: Runner, Security & Quality Compliance (`lead-runner-security-qa`)**: Budget = 3.
  - `spec-runner-sandbox`: Runtime process execution, script interpreters, symlink jailing (`os.Root`), cross-platform signal handling.
  - `spec-qa-zero-contamination`: Security audit (zero internal corporate/personal contamination, credential safety, directory traversal), race detection, test coverage, SPEC-0001 compliance.

---

## 2. Review Rubric & Audit Dimensions

Each Specialist and Lead must evaluate their assigned domain against the following 6 core dimensions:

1. **Correctness & Robustness**:
   - Edge cases, error handling, defensive programming, boundary conditions, input validation.
2. **Security & Sandboxing**:
   - Zero Contamination (no internal Google, corporate, or private machine specifics).
   - Path traversal, symlink escapes, arbitrary execution safeguards, file permissions.
3. **Performance & Concurrency**:
   - In-memory data structures, allocation efficiency, goroutine lifecycle, mutex lock granularity, race condition freedom.
4. **Specification Compliance**:
   - Conformance to `SPEC-0001`, `ADR-0001`, `RFC-0002`, and `https://schemas.agentskills.io/discovery/0.2.0/schema.json`.
5. **CLI Ergonomics & Developer Experience**:
   - Output formatting (Table, JSON, Raw), standard exit code enum (`0`..`5`), progressive disclosure.
6. **Test Coverage & Verifiability**:
   - Targeted unit tests, race detection (`go test -race`), mock isolation, edge case verification.

---

## 3. Map-Reduce Operational Protocol

### Map Phase:
- Each Lead Agent defines domain specifications and assigns targeted inspection tasks to Specialists.
- Specialists perform code analysis, run targeted tests and validation commands on their assigned packages, and submit concrete findings with exact file/line references and remediation recommendations.

### Reduce Phase:
- Leads consolidate Specialist findings into unified domain reports.
- Swarm Coordinator reconciles all findings, runs full project verification (`go test ./...`, `go vet ./...`, race check), verifies no temporary stubs remain, and generates the final Comprehensive Code Review Report.
