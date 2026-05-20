# Changelog

All notable changes to dotkeeper are documented in this file.

The format follows [Keep a Changelog v1.1.0](https://keepachangelog.com/en/1.1.0/).
dotkeeper adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.9.3] - 2026-05-20

### Performance

- **Consolidated default ignore patterns.** A CPU profile against the
  running daemon showed `ignore.Matcher.Match` accounting for 32.6%
  of total CPU, with the supporting glob engine and string-search
  internals pushing matching-related work past 50%. The dominant
  per-pattern cost was glob matching, multiplied by enumerated
  variant families in the prior default list. Collapses:
  - 8 sqlite variants (`*.sqlite3`, `*.sqlite3-journal`, … ) → `*.sqlite*`
  - 4 vim/nvim swap variants (`*.swp`, `*.swo`, `.*.swp`, `.*.swo`) →
    `*.sw[op]` + `.*.sw[op]` (two patterns, character class instead
    of separate strings)
  - 2 Python bytecode variants (`*.pyc`, `*.pyo`) → `*.py[co]`
  - 2 log variants (`*.log`, `*.log.*`) → `*.log*`

  Net pattern count: 64 → 51. Expected matcher-time reduction is
  30-50% in steady state on dev trees with many files.

  Pattern order is also load-bearing now: the highest-frequency
  matches (`.git`, dotkeeper/Syncthing control files) stay at the top
  of the list so the matcher's first-hit-wins short-circuits early.
  Documented in the file header.

  Two new regression tests pin the consolidation against accidental
  re-expansion: one asserts the consolidating globs are present,
  one asserts the old enumerated variants are absent. A deliberate
  future split-back-out would have to amend both.

## [0.9.2] - 2026-05-20

### Performance

- **Daemon self-applies nice=19 / ioprio=idle on every thread at
  startup.** The packaged systemd user unit already enforces this via
  `Nice=`, `IOSchedulingClass=`, and `CPUWeight=10`, but containers,
  manual `dotkeeper start` in a dev loop, and third-party packagers
  that ship a stripped-down unit all bypassed it. The embedded
  Syncthing scanner is heavy enough that running at default priority
  is user-visible on weaker hardware. Implementation iterates
  `/proc/self/task` because Linux setpriority is per-thread despite
  the `PRIO_PROCESS` name (man 2 setpriority NOTES), and Go's runtime
  has already created GOMAXPROCS worker threads by the time `main()`
  is entered. Non-Linux platforms compile to a no-op — operators on
  the BSDs / macOS / Windows are expected to lean on
  launchd / rc.d / container scheduling.

- **Tightened default `.stignore` patterns.** Added the
  language-server and tooling caches observed dominating Syncthing's
  index and rescan footprint on active development trees:
  `.zig-cache`, `.rust-analyzer`, `.ccls-cache`, `.clangd`,
  `.ipynb_checkpoints`, `playwright-report`, `test-results`. A
  pinning test in `internal/config/ignore_test.go` catches accidental
  removal during future refactors. (Operator-side flakes/Nix
  configurations that mirror the in-Go list need a follow-up commit
  to stay aligned; the canonical list is now the Go default.)

## [0.9.1] - 2026-05-17

### Fixed

- **Auto-backup no longer races active git workflows.** When the user is
  mid-rebase, mid-merge, mid-cherry-pick, mid-revert, or mid-bisect,
  the scheduled `git add -A` + `git commit` would land in the middle of
  the user's session — collapsing a `MERGE_MSG`, committing between
  conflict resolutions, or producing a confusing "auto: scheduled
  backup" commit halfway through an interactive rebase. v0.9.1 detects
  the in-progress markers git itself maintains (`rebase-merge/`,
  `rebase-apply/`, `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`,
  `BISECT_LOG`) and defers that repo's backup to the next reconcile
  tick. Slot timing is not "skipped" — the next quiet observation
  fires the backup, still within the configured interval.

### Security

- Bumped `github.com/Azure/go-ntlmssp` from 0.1.0 to 0.1.1 (closes the
  panic-on-malformed-NTLM-challenge advisory). Transitive dep via
  `go-ldap`; not on any code path dotkeeper exercises today, but
  closing the alert keeps the supply chain clean.

### Maintenance

- Upgraded `docker/login-action` from `v3.5.0` to `v4.1.0` (Node 20 →
  Node 24). The June 2026 GitHub Actions deprecation no longer affects
  the release pipeline.
- Made the `make build` / `-tags noassets` requirement impossible to
  miss in the README so a fresh contributor's first `go build ./...`
  no longer produces a cryptic `undefined: auto.Assets` error without
  a paper trail to the fix.

## [0.9.0] - 2026-05-17

### Changed

- **Default git backup interval is now `daily`, not `hourly`.** Hourly
  pushes concentrated CPU and disk work too often on weak hardware,
  especially when other tools (browser sessions, language servers,
  swap-warm processes) were already contending for the system. Slot
  staggering still applies — three machines run at offsets within the
  24 h window — so backups are spread out, not clustered. Existing
  `machine.toml` files that explicitly set `default_git_interval =
  "hourly"` keep the old behaviour; only the unset/default case
  changes. Per-repo `interval = "..."` overrides are unaffected.

### Added

- **Aggressive resource de-prioritisation, end to end.** dotkeeper must
  never cause user-visible stutter on a client system, even on weak
  hardware under load. Two new layers cooperate to enforce this:
  - A new system-wide systemd user unit at
    `/usr/lib/systemd/user/dotkeeper.service` (shipped by the deb/rpm
    packages) runs the daemon under `Nice=10`, `IOSchedulingClass=idle`,
    `CPUWeight=10`, `IOWeight=10`, `MemoryHigh=512M`, `MemoryMax=1G`.
    Users on systemd hosts can `systemctl --user enable --now dotkeeper`.
  - Every git subprocess dotkeeper spawns (in `gitsync`, `reconcile`,
    and `doctor`) is funnelled through a new `internal/procnice`
    package that prepends `nice`/`ionice` wrappers to the command,
    so CPU and I/O priority are established before `exec(2)` replaces
    them with the real binary — race-free, applied at the child's very
    first instruction. A post-`Start()` syscall fallback covers the
    rare case where the wrapper binaries aren't on PATH. No-op on
    non-Linux.

  Slot timing is **not** affected. dotkeeper still fires each machine's
  backup at its scheduled offset; the kernel scheduler simply yields
  the daemon's work whenever user processes want the CPU or disk.

### Performance

These changes are silent: same behaviour, fewer cycles per reconcile.

- **Triple `git` call collapsed to one in `queryRepoGitState`.** The
  observed-state collector used to invoke `git rev-parse HEAD`,
  `git status --porcelain`, and (when dirty) `git status --porcelain
  -z` — three subprocesses per tracked repo per reconcile. v0.9 issues
  a single `git status --porcelain=v2 --branch` and reads HEAD oid,
  dirty flag, and per-file mtimes from one process. Drops 2N git
  fork+execs per reconcile tick for N tracked repos.
- **mtime-cache for TOML config reads.** `NewDesiredProvider` no
  longer reparses `machine.toml`, `state.toml`, and every
  `.dotkeeper.toml` under the scan roots on each reconcile. A new
  `configCache` keyed by `(mtime, size)` returns the previously-parsed
  value when the file hasn't changed. Skips one stat + read + TOML
  parse per tracked repo per tick on the steady-state path.
- **`stclient` memoises hot endpoints.** `GetStatus` is cached for the
  client's lifetime (MyID is immutable while Syncthing is running);
  `GetConfig` caches the raw response and invalidates on `SetConfig`,
  so a reconcile that adds a device and a folder goes from 3 GETs to
  1; `GetConnections` is cached with a 30 s TTL to smooth bursts of
  fsnotify-driven reconciles without ever masking a real peer-loss
  event for more than half the reconcile interval.
- **`applyGitCommitDirty` short-circuits when `git add -A` produces an
  empty index.** Skips the second `stagedDeletionsApplier` call (one
  `git diff --cached`) for repos where the dirty signal was a
  timestamp-only change.

## [0.8.2] - 2026-05-17

### Security

- `govulncheck` now reports "No vulnerabilities found." against a fresh
  build, clearing the stdlib advisories v0.8.0 + v0.8.1 carried
  (`GO-2026-4971`, `4918`, `4981`, `4986`, `4982`, `4980`, `4977`,
  `4976` — all `net`, `net/http`, `net/mail`, `html/template`). Driven
  by bumping the `go` directive in `go.mod` from `1.26.2` to `1.26.3`.

### Fixed

- `release.yml` now uses `PACKAGES_TOKEN` instead of `GITHUB_TOKEN` when
  invoking `gh release create`. GitHub deliberately does not propagate
  events triggered by the built-in `GITHUB_TOKEN` to downstream
  workflows, which meant `release: published` never fired for
  `docker.yml` and v0.8.0 / v0.8.1 needed a manual draft-toggle to
  ship their Docker image. v0.8.2 is the first release that propagates
  end-to-end on its own.
- `docker.yml` workflow_dispatch path now produces proper `vX.Y.Z` and
  `X.Y` tags. Previously `type=semver` in the metadata-action only
  inspected `github.ref` (which stays at `refs/heads/main` on a manual
  dispatch from main), so a manual rebuild emitted only the `latest`
  tag and silently dropped the versioned tags. A new "Resolve version
  from ref" step normalises `inputs.ref || github.ref` into explicit
  `type=raw` tag inputs.

## [0.8.1] - 2026-05-16

### Fixed

- `dotkeeper start` now routes its `slog` output to stdout instead of
  stderr, so the dup2 in `engine.Start` captures it alongside Syncthing's
  output in `~/.local/state/dotkeeper/syncthing.log`. v0.8.0 silently
  regressed this: because Syncthing v2 also uses `log/slog` and our
  `slog.SetDefault` intercepts it, all log output went to stderr → the
  systemd journal, and the file stopped growing. The journal still
  captured everything in v0.8.0, but anyone tailing `syncthing.log`
  saw nothing after the upgrade.

## [0.8.0] - 2026-05-16

### Changed

- **Embedded Syncthing is now v2.1.0** (was v1.30.0). This is the first
  dotkeeper release on the Syncthing v2 line. ADR 0006 records the full
  rationale and migration mechanics.
- Syncthing's per-folder database backend is now SQLite (was LevelDB). On
  first launch, dotkeeper migrates the existing LevelDB database to SQLite
  via Syncthing's `TryMigrateDatabase`. The migration is a one-shot
  operation; subsequent launches go straight to SQLite. dotkeeper uses
  the pure-Go SQLite driver (`modernc.org/sqlite`); release binaries
  remain `CGO_ENABLED=0` and platform coverage is unchanged.
- Syncthing log lines in `~/.local/state/dotkeeper/syncthing.log` now use
  Syncthing v2's structured `slog` format — `2026-05-16 12:34:56 INFO …`
  instead of the v1.x prefix-less plain text. Anyone post-processing
  this file with a grep / awk pipeline should re-check the parser.
- Deleted-item retention in the embedded Syncthing database is configured
  as "no auto-prune" (`retention=0`), preserving the v1.x "kept forever"
  behaviour. Syncthing v2's stock default of ~15 months would silently
  expire deletion records on long-disconnected peers — surprising for
  dotkeeper's small fleets.

### Security

- `govulncheck` no longer reports the long-standing `quic-go` advisories
  GO-2025-4017 (was reachable) and GO-2025-4233 (module-only). Both were
  fixed in `quic-go` v0.54.1 and v0.57.0 respectively, and reach dotkeeper
  via the bump to Syncthing v2.1.0 / quic-go v0.59.0.

### Fixed

- The dependabot security PR that previously blocked on the v1.30 quic-go
  pin (#17) is now obsolete and was closed in favour of this release.

## [0.7.0] - 2026-05-15

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

[Unreleased]: https://github.com/julian-corbet/dotkeeper/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.6.1...v0.7.0
[0.6.1]: https://github.com/julian-corbet/dotkeeper/compare/v0.5.0...v0.6.1
[0.5.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/julian-corbet/dotkeeper/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/julian-corbet/dotkeeper/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/julian-corbet/dotkeeper/releases/tag/v0.1.1
