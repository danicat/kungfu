# Changelog

All notable changes to `kungfu` are documented in this file.

## v0.9.1 - 2026-11-26

### Fixed

- Restricted cached named-catalog loading (`catalog.WithNamedCatalogs`) and `kungfu catalog list` to catalogs registered in `config.yaml`, preventing duplicate ghost entries and false stale-cache warnings from leftover unhashed `v0.8.x` cache files or removed catalogs.
- Added automatic pruning of legacy unhashed and orphaned catalog cache files (`catalog.PruneNamedCatalogCaches`) during `kungfu update`.

## v0.9.0 - 2026-11-26

### Breaking changes

- **Catalog registration now lives solely in `config.yaml`:** Registered catalogs are stored in `~/.config/kungfu/config.yaml` under `catalogs:` instead of being split between `config.yaml` and `state.json`.
- **YAML-only configuration:** Dropped `config.json` and the unused `load.auto_update` setting. Unknown keys in `config.yaml` (such as an older `load:` block) now print a warning to `stderr` instead of failing.
- **Local telemetry is opt-in (default off):** Local usage counters (`load`, `run`, `learn`) in `state.json` are disabled by default so read commands do zero state writes. Enable them with `telemetry.enabled: true` in `config.yaml` or `KUNGFU_TELEMETRY=1`.
- **Removed legacy CLI aliases and flags:** Removed command aliases (`ls`, `search`, `info`, `add`, `rm`, etc.), `--no-color`, global `--raw` (use `kungfu load --raw`), `learn --load`, `update --yes`/`--refresh`, `catalog add --refresh`/`-b`/`--branch`, and `Forgotten Skills` history.
- **Removed `catalog.json` discovery fallback:** Catalog discovery now checks `.well-known/agent-skills/index.json`, `index.json`, and repository directory trees (`SKILL.md`).
- **Removed hidden search operators:** `kungfu find` now accepts plain keywords and `@catalog` qualifiers only (`tag:`, `type:`, `catalog:`, and `source:` prefixes were removed because tags are already indexed as weighted search terms).
- **Consistent `show --json` schema:** `kungfu show --json` always returns a JSON array, even for a single skill.
- **Fail-closed archive digests and HTTPS-only URLs:** Remote skill archives (`.tar.gz` and `.zip`) require a matching `sha256` digest unless `--force` is passed on `kungfu load`, and non-loopback `http://` URLs are rejected.

### Upgrading from v0.8.x

1. Re-register any custom catalogs previously stored only in `state.json`:
   ```bash
   kungfu catalog add spf13/go-skills
   kungfu catalog add google/skills
   ```
2. Run `kungfu update` once to rewrite cached catalog files in the new unified cache format (`~/.cache/kungfu/catalogs/`).
3. Optional: remove the obsolete `load:` section from `~/.config/kungfu/config.yaml` to silence the unknown-key warning.

### Security

- Hardened HTTP client against SSRF (blocking private LAN, link-local, metadata, multicast, and unspecified IPs), HTTPS-to-HTTP redirect downgrades, and cross-host `Authorization`/`Cookie` header leakage.
- Confined `.tar.gz` and `.zip` archive extraction with Go's `os.OpenRoot`, rejecting symlinks, hardlinks, path traversal, decompression bombs, and excessive file counts while stripping group/world write permissions.
- Enforced strict skill and catalog name validation, symlink-safe disk reconciliation, bounded cross-process state locks, `0700`/`0600` cache and config permissions, atomic installs with rollback, and absolute-shebang resolution in `kungfu run`.

### Fixed

- Fixed `@catalog` lookup for GitHub `owner/repo` catalogs (`kungfu load go@spf13/go-skills`, `kungfu learn go@spf13/go-skills`, `kungfu load go/references/guide.md@spf13/go-skills`), `@default` lookup, and unambiguous short repo names (`go@go-skills` matching `spf13/go-skills`, failing fast if ambiguous).
- Fixed multi-catalog shadowing and cache staleness:
  - `Catalog.All()` now retains identically named skills from distinct catalogs so `kungfu find <skill>@<catalog>` and `kungfu list` never drop shadowed skills, and both `list --json` and `find --json` include `source` provenance.
  - Stopped scanning `~/.cache/kungfu/skills/` as a local skill source so deleted or stale cached skills cannot resurface as local items.
  - Added digest verification for cached non-archive remote skills in `kungfu run` and subpath loads so stale cached files never shadow newer remote versions.
  - Named catalogs older than 24 hours auto-refresh when targeted by `load`, `find`, `show`, `learn`, or `run`, and trigger a stale-cache warning on `stderr` during unscoped `list` and `find`.
  - Added `stderr` drift warnings when a locally installed skill in `.agents/skills/` is older than the catalog version during `load` and `run`.
- Fixed `kungfu load` pagination dropping blank lines, splitting Markdown code fences across pages, or truncating `kungfu load --raw` by default.
- Fixed RRF search `docID` collisions across catalogs, exact-match precedence, and deterministic tie-breaking.
- Added signal forwarding (`SIGINT`/`SIGTERM`) to `kungfu run` and explicit `%SystemRoot%\System32\taskkill.exe` process-tree cleanup on Windows.

### Changed

- Upgraded minimum Go toolchain to Go 1.27 (`go.mod`).
- Kept both `default` and `kungfu` built-in catalogs always registered and protected from `kungfu catalog remove`.
- Added `-ldflags` version injection support (`internal/version.Version`).
