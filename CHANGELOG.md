# Changelog

All notable changes to dotkeeper are documented in this file.

The format follows [Keep a Changelog v1.1.0](https://keepachangelog.com/en/1.1.0/).
dotkeeper adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.0] - 2026-04-26

### Added

- Per-repo `dotkeeper.toml` as authoritative config, committed at the repo root and
  carried by git — the opt-in signal that a repo should be managed (ADR 0001).
- Machine-local state split: `machine.toml` in `$XDG_CONFIG_HOME/dotkeeper/` for
  declarative per-machine settings; `state.toml` in `$XDG_STATE_HOME/dotkeeper/`
  for tool-owned runtime state. No more shared mutable config synced across peers
  (ADR 0002).
- Pure-function reconciler loop: `Diff(Desired, Observed) → Plan` with idempotent
  `Apply()` — safe to run on inotify events, a periodic timer, and on demand without
  risk of double-applying anything (ADR 0003).
- Scan-root–based repo discovery: declare which directories to walk; dotkeeper finds
  every committed `dotkeeper.toml` automatically — no `dotkeeper add` per repo
  (ADR 0004).
- New v0.5 schema types: `MachineConfigV2`, `RepoConfigV2`, `StateV2`.
- New subcommands: `dotkeeper reconcile`, `dotkeeper identity`, `dotkeeper track`,
  `dotkeeper untrack`.
- Daemon mode: `dotkeeper start` now drives the reconciler with `fsnotify` triggers
  plus a periodic `time.Ticker` as a safety net.
- Architecture documentation (`docs/architecture.md`) and four ADRs covering all
  major design decisions.
- Homebrew formula auto-publish CI workflow triggered on release tags.

### Changed

- README rewritten around the declarative model and v0.5 quick-start workflow.
- `dotkeeper doctor` updated to validate v0.5 schema types.

### Removed

- v0.4 imperative subcommands: `add`, `remove`, `join`, `pair`, `sync`,
  `install-timer`, `stop`.
- v0.4 `SharedConfig` schema (superseded by per-repo `dotkeeper.toml` and
  machine-local `state.toml`).

> **Upgrade note:** dotkeeper has no production users at v0.4.0; no migration
> tooling is provided. When upgrading, wipe `~/.config/dotkeeper/` and
> `~/.local/state/dotkeeper/` and re-run `dotkeeper init`.

## [0.4.0] - 2026-04-21

### Added

- `dotkeeper doctor` self-diagnostic subcommand with `--json` output.
- AUR auto-publish CI workflow triggered on release tags.

### Fixed

- Disabled QUIC listener in embedded Syncthing by default to avoid port conflicts.

## [0.3.0] - 2026-04-19

### Added

- `dotkeeper conflict keep <path>` and `dotkeeper conflict accept <path>` manual
  resolver commands; both accept `--all` to process every pending conflict in one
  invocation.

## [0.2.0] - 2026-04-19

### Added

- Auto-resolve for trivial sync conflicts: hash-identical dedup and 3-way
  `git merge-file` text merge; clean merges produce a scoped auto-commit.
- `dotkeeper conflict list` and `dotkeeper conflict resolve-all` subcommands.
- OpenSSF Best Practices badge.
- CodeQL static analysis workflow.
- golangci-lint as a hard CI gate with pinned version.
- Test coverage reporting with CI upload.
- Contributor-facing docs: `CODEOWNERS`, discussion templates, expanded
  `CONTRIBUTING.md` with test policy and PR workflow.
- Mermaid architecture diagram replacing ASCII in README.

### Changed

- Makefile: version injected from `git describe --tags` via `-X main.version` ldflag.

### Fixed

- Reconcile Syncthing-delivered content before `git pull` to prevent stale-ref
  errors on fast-forward.

## [0.1.2] - 2026-04-19

### Security

- Documented known unresolved `quic-go` advisories in `SECURITY.md`.
- Locked down workflow permissions to least-privilege.
- Pinned base Docker images by digest.
- Upgraded dependencies to clear five of seven CVE advisories.

## [0.1.1] - 2026-04-18

### Fixed

- Release workflow: narrow `upload-artifact` globs to explicit extensions to
  prevent staging directories from appearing in GitHub Releases.
- Release workflow: pinned nFPM to v2.46.3 with exact asset filename to fix
  silent 404 on `latest` URL.
- CI: bumped `actions/upload-artifact`, `actions/download-artifact`, and
  `actions/setup-go` to current stable versions.

[Unreleased]: https://github.com/julian-corbet/dotkeeper/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/julian-corbet/dotkeeper/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/julian-corbet/dotkeeper/releases/tag/v0.1.1
