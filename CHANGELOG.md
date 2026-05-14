# Changelog

All notable changes to dotkeeper are documented in this file.

The format follows [Keep a Changelog v1.1.0](https://keepachangelog.com/en/1.1.0/).
dotkeeper adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- All config-file writes are now atomic (write-temp + `fsync` + `rename`),
  so concurrent readers never see a half-written file and a crash mid-write
  cannot leave a torn file on disk. Applies to `state.toml`, `machine.toml`,
  `.dotkeeper.toml`, `.stignore`, `.git/info/exclude`, and merged conflict
  files.
- Concurrent `dotkeeper track`, `untrack`, and `peer add` invocations no
  longer race on `state.toml`. The read-modify-write cycle now runs under
  an exclusive advisory file lock (`flock(LOCK_EX)` on Linux/macOS,
  `LockFileEx` on Windows), serialising concurrent writers and preventing
  lost updates. Internal API: `config.MutateStateV2(func(*StateV2) error)`.
- Atomic-write temp files now end in `.tmp` so dotkeeper's default
  Syncthing ignore pattern (`*.tmp`) catches them before Syncthing can
  propagate the transient to peers.
- GitHub Dependabot vulnerability alerts and automated security fixes are
  now enabled on the public repository.

### Fixed

- `state.toml` could become invalid TOML when multiple `dotkeeper`
  invocations raced — now guarded by the new locking layer.
- Build on Windows is restored — the locking primitive previously used
  POSIX-only `golang.org/x/sys/unix` directly.
- `dotkeeper doctor` recovery hints for corrupt `state.toml` and
  `machine.toml` now give actionable instructions (back-up-then-remove
  for tool-owned `state.toml`, edit-or-restore for user-authored
  `machine.toml` — never delete).
- `internal/conflict` now fsyncs merged conflict files before rename
  (was missing — possible empty-file outcome on a power loss between
  write and rename on certain filesystems).

### Added

- New `tests/multipeer/` end-to-end suite: 13 scenarios — 5 happy-path
  (propagate A→B, propagate B→A, conflict round-trip, offline catch-up,
  track-after-pair) plus 8 adversarial (clock skew, mid-sync network
  partition, SIGKILL during reconcile, pathological filenames including
  emoji and 200-char names, 2000-file burst, concurrent track/untrack,
  three-way conflict, peer-flap × 5). Drives two real Syncthing peers
  across a Docker bridge.
- CI gate `multipeer-e2e` runs the suite on every pull request with the
  Go data-race detector enabled.
- CI gate `fuzz-smoke` runs every declared Go fuzz target for 20 seconds
  per pull request. Surfaces new crashes that randomised input finds but
  seed corpora miss.
- CI build step now cross-compiles to `darwin/amd64`, `darwin/arm64`, and
  `windows/amd64` to catch platform-specific imports.
- Standard test step now runs with `go test -race`.
- Branch protection on `main` requires the new gates before merge.
- Coverage at 100% for `parseGitInterval` and `repoBackupDue` (was 22%
  and 38% respectively).

## [0.6.1] - 2026-05-13

### Breaking

- Per-repo config is now `<repo>/.dotkeeper.toml`, local to each machine and
  excluded from both Git and Syncthing. `<repo>/dotkeeper.toml` is no longer
  read as repo config.

### Fixed

- Fixed the macOS launchd service manager build so release artifacts compile
  across the full supported platform matrix.
- Enforce dotkeeper-managed `.stignore` files during reconcile so repo roots
  never sync `.git`, `node_modules`, build outputs, Syncthing temp files, or
  sync-conflict artifacts across peers.
- Repair missing Syncthing folder marker directories during reconcile. If a
  managed folder loses its `.dkfolder` marker, dotkeeper now recreates it
  instead of leaving Syncthing in a folder-marker error state.

### Changed

- Vulnerability disclosure now goes through the [GitHub Security
  Advisories form](https://github.com/julian-corbet/dotkeeper/security/advisories/new)
  instead of email. See [SECURITY.md](SECURITY.md) for the full
  policy.
- `dotkeeper track <path>` now bootstraps local excludes immediately after
  writing `.dotkeeper.toml`.
- `dotkeeper doctor` now flags dotkeeper/Syncthing local metadata that has been
  accidentally added to Git.
- Documentation now shows a denylist-first Nix/Home Manager pattern for keeping
  private repo topology outside public dotkeeper history.
- Public release metadata now compares directly from v0.5.0 to v0.6.1.

### Added

- `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1).

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

[Unreleased]: https://github.com/julian-corbet/dotkeeper/compare/v0.6.1...HEAD
[0.6.1]: https://github.com/julian-corbet/dotkeeper/compare/v0.5.0...v0.6.1
[0.5.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/julian-corbet/dotkeeper/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/julian-corbet/dotkeeper/releases/tag/v0.1.1
