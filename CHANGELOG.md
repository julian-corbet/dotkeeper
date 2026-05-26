# Changelog

All notable changes to dotkeeper are documented in this file.

The format follows [Keep a Changelog v1.1.0](https://keepachangelog.com/en/1.1.0/).
dotkeeper adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.2.3] - 2026-05-26

Docs + CI release. No source changes; binary identical to v1.2.2.
Tagged primarily to validate the new Alpine native-build workflow
end-to-end on a real release event.

### Added

- **First-class Alpine packaging.** `alpine/APKBUILD` + a dedicated
  `.github/workflows/alpine.yml` build the package using Alpine's
  native `abuild` toolchain in an `alpine:edge` container on every
  `release: published`. Distinct from the `nfpm`-produced `.apk`
  in `release.yml` — the native one carries proper Alpine metadata
  and is the version suitable for upstream submission into
  `aports/community`. Two arches (`x86_64`, `aarch64`); native
  `.apk` uploaded to the release page alongside the existing
  artifacts. Acts on item 5 of @elrido's review in #82.

### Changed

- **Workflow hardening** (acts on #82 items 1–3):
  - Both `ci.yml` and `release.yml` switch from
    `permissions: read-all` to `permissions: {}`. The repo is
    public so anonymous reads cover everything CI needs; nothing
    writes via `GITHUB_TOKEN` (release uses `PACKAGES_TOKEN`).
  - All `actions/checkout` invocations gain
    `persist-credentials: false`. Defense in depth.
  - `ci.yml`'s "Configure git identity for tests" step picks up
    an inline comment explaining why it's needed and why the
    identity never leaves the runner.

- **Makefile is self-documenting** (acts on #82 item 4). Each
  target carries an inline `## description`; `make help` (or just
  `make`, now the default goal) prints them.

- **README + landing site refreshed for v1.2.x.** Phase 2 surfaces
  in the "How it works" + "Subscriptions" sections of README;
  hero demo terminal on dotkeeper.corbet.ch shows the actual
  onboarding flow (`offers` → `subscribe` → reconcile); commands
  tables on both surfaces pick up `subscribe`, `unsubscribe`,
  `subscriptions list`, `offers`, `health`, `transport repos`,
  `bench-now`, `bare-init`.

## [1.2.2] - 2026-05-25

### Added

- **Subscription reconcile + offers discovery** (Phase 2, final
  piece). Subscriptions declared in `machine.toml` or via
  `dotkeeper subscribe` now actually do something:

  1. Each reconcile, dotkeeper reads Syncthing's pending-folders
     API and the declared subscriptions, runs the pure
     `subscribe.Resolve` matcher, and emits `AcceptSubscription`
     actions for matches.
  2. The applier creates the local path, writes a
     `.dotkeeper.toml` stub so the next discovery scan registers
     the folder normally, adds the Syncthing folder with the
     offering peer in share-with. Syncthing's BEP gossip seeds
     the working tree from the peer.
  3. Subscriptions stay in three categories — accepted, pending
     (no matching offer yet), ambiguous (multiple distinct
     folder IDs claim the same canonical) — surfaced via
     `subscribe.Resolve`'s Result.

- **`dotkeeper offers`** CLI lists folders peers are advertising
  that this machine could subscribe to. Output includes a
  shell-paste-ready `dotkeeper subscribe …` action column.
  Onboarding aid for the 2-to-200 device scale: see what's
  available, paste the line for each folder you want.

- **`internal/subscribe`** package — pure-function matching
  engine. O(N+M) in offers + subscriptions via hashmap
  lookups. Eight test cases cover happy path, pending,
  ambiguous, already-local skip, name-based fallback for
  non-git folders, mixed subscription types, and empty-label
  offer rejection.

### Notes

- Auto-clone (git clone before mounting) is intentionally
  deferred — Syncthing seeds the working tree from the peer's
  files, which covers content. A follow-up PR will run
  `git clone` first when the subscription's canonical names a
  reachable remote, giving the new machine full git history out
  of the box.

## [1.2.1] - 2026-05-25

### Added

- **Subscription schema + CLI** (Phase 2, second piece).
  Declarative subscriptions live in `machine.toml`:

  ```toml
  [[subscribe]]
  canonical = "github.com/julian-corbet/cv"
  # path = "..." optional override; defaults to mirror convention
  ```

  Imperative subscriptions go into `state.toml` via three new
  CLI subcommands:

  - `dotkeeper subscribe <git-url>` — any URL form (HTTPS, SCP,
    ssh://) is normalised via `gitident.Canonical` before being
    stored. Accepts `--name NAME` for non-git folders, `--path`
    to override the mirror-convention default.
  - `dotkeeper unsubscribe <git-url-or-name>` — removes an
    imperative subscription. Declarative entries in `machine.toml`
    must be removed there.
  - `dotkeeper subscriptions list` — shows the merged
    `(declarative + imperative)` list with a SOURCE column so
    operators can see which entries come from where.

  `config.MergeSubscriptions(declarative, imperative)`
  deduplicates by identity (canonical URL first, name fallback);
  declarative wins on conflict — Nix-managed config is the source
  of truth and can't be silently overridden by a CLI-added entry.

  No reconcile-side behaviour yet — subscriptions are parsed,
  validated, persisted, and listed. Provisioning + auto-clone
  arrive in the next PR.

## [1.2.0] - 2026-05-25

### Added

- **Git-remote URL as canonical folder identity** — Phase 2 of
  the receiver-decides-what-to-sync architecture, foundation
  piece. Every dotkeeper-tracked git repo now carries a
  `[git]` section in its `.dotkeeper.toml` with the remote URL
  and its canonical form (e.g. `github.com/julian-corbet/cv`).

  The canonical form is computed by `internal/gitident.Canonical`
  and collapses every git URL syntax for the same upstream repo
  into one identity string: HTTPS variants, SCP-style
  (`git@host:path`), ssh-URL form with or without explicit
  default ports, `git://`. Two operators with the same repo
  arrive at the same canonical regardless of which URL syntax
  their git client recorded.

  `dotkeeper track` populates the field at track time from the
  local working tree's `origin` remote. Non-git folders (dotfiles
  dirs, scratch areas) legitimately leave `[git]` unset and fall
  back to name-based identity.

  Syncthing folder labels now carry the canonical URL when
  available so peers' ClusterConfigs advertise the load-bearing
  identity. This is the wire-level primitive that the subscription
  matcher (next PR) reads to decide whether a peer's offered
  folder matches a local subscription.

  No migration required: existing folders retain their current
  `dk-<name>-<hash>` IDs and continue working; the canonical URL
  is added alongside as label metadata. This is the bump to v1.2.x
  because the schema evolves (new `[git]` section), not because
  behaviour changes (yet — subscriptions arrive in v1.2.1+).

## [1.1.23] - 2026-05-25

### Added

- **`dotkeeper transport repos`** — per-(transport, peer, repo)
  cost-model visibility. Where `dotkeeper transport status`
  shows the cross-repo aggregate ("is this transport reachable
  at all"), this command shows the per-folder prediction grid:
  one row per (peer, transport, repo) triple with PRED@1KB and
  PRED@1MB columns derived from the cost model's current
  parameters. Lets operators see what dotkeeper would route a
  change through and which tuples haven't accumulated
  observations yet.

  Supports `--peer=NAME` and `--transport=NAME` filters to
  narrow output on large fleets.

  Known limitation (called out in --help): the CLI builds a
  fresh transport manager, so predictions reflect the
  bootstrap priors rather than the running daemon's learned
  state. CostModel persistence across processes is a planned
  follow-up; until then, the daemon's INFO logs
  (`propagator: pushed`) carry the live observations.

## [1.1.22] - 2026-05-24

### Added

- **Active transport benchmarker.** Background goroutine in the
  daemon that periodically measures synchronous-transport cost
  per `(transport, peer, folder)` tuple. Without this, the cost
  model only learns from organic traffic; folders that rarely
  change never accumulate enough observations to overcome the
  bootstrap prior, and routing decisions for those folders stay
  frozen. With it, the model self-tunes for every folder, every
  peer, every transport, with no operator intervention.

  Design constraints (in priority order):

  1. **Invisible to the user.** Probe files live under a
     `<folder>/.dkbench/` subdir, gitignored by dotkeeper's
     default ignore set and stignore'd so Syncthing doesn't
     gossip them. `defer`-driven cleanup runs even on
     PropagateChange error — no orphaned bytes.
  2. **Non-disruptive.** Skip if the tuple saw organic traffic
     in the last 30 min. Skip if the cost model has converged
     (≥ 20 effective samples). Skip if the transport is
     unreachable per current Routes table.
  3. **Bounded cost.** 24 h cadence per tuple, 64 KB probe
     payload, no-op for async transports (Syncthing can't be
     timed inline).
  4. **Just works.** ON by default — adopters don't have to
     find or set a config knob.

- **`dotkeeper bench-now [--folder=PATH]`** CLI subcommand. The
  operator-on-demand counterpart to the daemon's loop. Runs one
  probe per (synchronous transport, paired peer) pair against
  the named folder (or every managed folder), prints a table of
  observed ms/KB throughput, and updates the cost model so
  subsequent routing reflects the fresh numbers. Use when you
  want to investigate a routing decision or measure a new peer
  without waiting for the 24 h periodic cycle.

- **`.dkbench` excluded from defaults.** Added to both
  `DefaultSyncIgnorePatterns` and `DefaultGitExcludePatterns`
  so probe files never leave the local machine via either
  Syncthing or git.

This is the fourth and final piece of the Mutagen +
auto-benchmark series. Combined with the per-repo CostModel
keying (#76) and per-family priors (#78), the cost model now
self-tunes per `(transport, peer, repo)` triple — picking the
right tool for every job, automatically improving over time.

## [1.1.21] - 2026-05-24

### Changed

- **`DefaultPriorFor` now matches by transport-name prefix.**
  Previously the function had explicit `case "syncthing"` then
  fell through to a single catch-all default for everything else.
  Mutagen entered the mix in v1.1.19 — without a tuned prior the
  cost model's first-decision routing treated Mutagen like an
  unknown transport (conservative defaults), so Syncthing always
  won the cold start even when Mutagen was the better fit.

  New per-family priors:

  | Transport family | SetupMS | MS/byte | Effective throughput |
  |-----|-----|-----|-----|
  | `syncthing` | 1500 | 0.00002 | ~50 MB/s |
  | `mutagen+*` | 100 | 0.00005 | ~20 MB/s |
  | `git-ssh+*` | 200 | 0.0002 | ~5 MB/s |
  | fallback | 500 | 0.001 | ~1 MB/s |

  The Mutagen prior is tuned to make the cold-start decision pick
  Mutagen for sub-KB changes and Syncthing for multi-MB ones;
  observations refine the actual cross-over point per
  `(transport, peer, repo)` triple. Third piece of the
  Mutagen + auto-benchmark series.

## [1.1.20] - 2026-05-24

### Changed

- **CostModel now keyed by `(transport, peer, repo)`** instead of
  `(transport, peer)`. A transport's observed cost varies
  meaningfully by repo: Mutagen syncs a tiny `.agent` folder in
  30 ms but a 1 GB asset folder in 12 s. Without the per-repo
  dimension the cost model averaged those and routed both repos
  to whichever transport won the geometric mean — rarely the right
  choice for either.

  `repoID=""` is the cross-repo aggregate slot. Read by `Predict`
  as a fallback when a specific per-repo tuple has no observations
  yet (first sync of a fresh folder). Every `RecordTransfer` with
  a non-empty `repoID` mirrors the observation into the aggregate
  slot so a brand-new folder's first sync still gets the fleet's
  accumulated wisdom for that `(transport, peer)` path.

  `RecordTransfer` and `ModelParametersFor` both take an additional
  `repoID` parameter. Existing callers (`main.go` propagator path,
  `dotkeeper status` CLI) pass `folder.ID` for the propagator and
  `""` for the operator-facing aggregate view. Second piece of the
  Mutagen + auto-benchmark series; active benchmarking and
  cold-start priors follow.

## [1.1.19] - 2026-05-24

### Added

- **`MutagenTransport`** — a fourth transport that propagates
  folder changes via Mutagen sync sessions over SSH. Mutagen has
  lower per-change overhead than Syncthing's BEP gossip on
  small-file workloads when peers are SSH-reachable, because it
  skips the index-database + block-hash dance. The transport is
  detect-and-fallback: if the local `mutagen` CLI is not on PATH,
  `Available()` returns false and the Manager picks another
  transport. Zero operator opt-in needed.

  `PropagatesSynchronously()` is true: `PropagateChange` invokes
  `mutagen sync flush` and blocks until the session has applied
  the queued change, so the cost model receives a real duration
  signal (unlike Syncthing whose async BEP gossip returns in µs).

  Sessions are created lazily by `EnsurePeerReachability` using a
  deterministic name derived from `(folder.ID, peer.Name)`;
  re-creation is idempotent (Mutagen's "session already exists"
  error is treated as success).

  Wired into the daemon's transport list when the Tailscale
  resolver is available — same SSH path as `GitSSHTransport`.
  This is the first piece of the Mutagen + auto-benchmark series;
  per-repo cost-model keying and active benchmarking land in
  follow-up PRs.

### Changed

- **Perf-budget gate now requires `DK_PERF_GATE=1`** to fire.
  Concurrent multi-package `go test ./...` doubles per-test
  ns/op due to system contention, causing false-positive
  failures. The CI step still gates merges (it sets the env
  var); bare local `go test ./...` skips with a clear message.

## [1.1.18] - 2026-05-24

### Added

- **Perf-budget gate in the standard test suite.**
  `TestPerfBudgets` runs each hot-path benchmark
  (`BenchmarkDiff*` / `BenchmarkBuildDesired30Repos`) and fails
  the build when ns/op exceeds a documented budget. Budgets are
  padded ~1.5× over current measured baseline so CI noise doesn't
  flake the gate, but tight enough to catch a real regression
  before merge. Failing a budget gate forces the PR author to
  either (a) fix the regression or (b) bump the budget in the
  same PR with a justification — making the budget trail itself
  the perf history of the daemon.

  `TestPerfBudgetsSelfCheck` covers the harness so a wrong-
  direction comparison or muted assertion would itself fail.

  Runs as part of `go test ./...` — no new CI job to maintain.

## [1.1.17] - 2026-05-24

### Performance

- **Cap per-folder hasher count at 1.** Syncthing's upstream default
  is `hashers: 0` which resolves to `min(GOMAXPROCS, 8)` — so a
  30-folder cold start or wake-from-suspend can briefly pin 8
  cores while every folder hashes its files in parallel. dotkeeper
  is not latency-sensitive: an initial scan that takes 30 s
  instead of 10 s is invisible; a 90% multi-core spike for 10 s
  is exactly the "dotkeeper made my system feel slow" symptom we
  exist to avoid. Setting `<hashers>1</hashers>` smears the same
  total scan work across time without changing correctness, and
  fsWatcher-driven realtime sync is unaffected because that path
  doesn't touch hashers at all.

  Mechanism mirrors the v0.9.7 `rescanIntervalS=0` migration:
  `CanonicalHashers` constant + drift detector +
  `UpdateSyncthingFolderSchedule` action. Upgrade-time migration
  runs once per folder on the first reconcile, flipping older
  installs from auto to 1 without operator action.

## [1.1.16] - 2026-05-24

### Performance

- **`Diff` no longer does O(N²) repo lookups.** The per-folder loop
  used to call a linear `observedRepoByPath` scan once per folder,
  so a 30-folder × 30-tracked-repo install paid 900 string-equality
  checks per reconcile. Hoisted to a precomputed `map[path]RepoObs`
  built once before the loop, dropping steady-state `Diff` from
  ~205 µs/op to ~186 µs/op on the 30-repo / 5-peer fixture (Intel
  Core Ultra 7 258V) — modest absolute cost today, but the saving
  compounds with repo count. Behaviour is unchanged: the missing-
  repo fallback (empty `RepoObs{Path: df.path}`) is preserved.

### Added

- **Reconcile benchmarks.** Four `BenchmarkDiff*` /
  `BenchmarkBuildDesired30Repos` cover the per-tick hot paths
  (steady-state, cold start, one-repo-changed, BuildDesired).
  Budgets documented in the file header so future regressions
  surface as test signal rather than mystery user reports about
  the fan spinning up.

## [1.1.15] - 2026-05-24

### Added

- **Opt-in `/debug/pprof` listener.** Setting
  `[debug] pprof_address = "127.0.0.1:6060"` in `machine.toml`
  makes the daemon bind a loopback HTTP server that serves the
  Go runtime's standard pprof endpoints (CPU, heap, goroutine,
  mutex, block, threadcreate). Operators can then run
  `go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30`
  to capture a CPU profile while dotkeeper is under load.

  Mutex and block profiling are enabled at the lightest non-zero
  sample rates on startup, so contention is captureable without
  toggling a separate handler.

  Off by default — the endpoints expose goroutine stack traces
  (potential path leak) and profiling itself perturbs the
  workload. Bind failures log a WARN and the rest of the daemon
  continues; observability surfaces must never tank the daemon.
  Context cancellation triggers a 5 s graceful drain so an
  in-flight profile capture finishes.

  This is the first step in the perf-investigation series.
  Future releases will measure with this, then optimise the
  highest-impact paths.

## [1.1.14] - 2026-05-24

### Fixed

- **Auto-accept ERROR storm on partial-overlap fleets.** dotkeeper's
  `AddDevice` used to set `autoAcceptFolders=true` on every peer
  it paired with, but never populated `<defaults><folder><path>`.
  The combination guarantees auto-accept can never succeed: every
  ClusterConfig from a peer announcing a folder the local side
  hasn't subscribed to produces a `Failed to auto-accept folder
  due to path conflict` ERROR plus an `Unexpected folder ID in
  ClusterConfig` WARN. On real two-machine installs with a few
  desktop-only repos, this generated 1000+ ERRORs/hour, which in
  turn pegged a CPU core and tripped `dotkeeper health`'s
  `degraded` threshold.

  Fix is two-part:
  1. `AddDevice` now defaults `autoAcceptFolders=false`. Folder
     membership is opt-in per machine — receivers explicitly join
     what they want. Matches Syncthing's offer/accept model
     without the implicit-spread footgun.
  2. A one-shot daemon-startup migration
     (`MigrateDisableAutoAcceptFolders`) walks every existing
     device and flips `autoAcceptFolders` to false in a single
     `SetConfig` call. Idempotent: no PUT issued when nothing
     needs migrating. Upgrading installs immediately benefit
     without operator intervention.

  Architectural note: declarative subscription
  (`dotkeeper accept <folder-id> --path <path>`) and surfacing
  offered-but-not-subscribed folders in `dotkeeper status` /
  `health` are queued as a follow-up — this release just removes
  the ERROR storm at its source.

- **`LowerSelf` race in `internal/procnice`.** Go runtime
  occasionally spawned an OS thread (GC worker, sysmon,
  scavenger) between `os.ReadDir("/proc/self/task")` and the
  per-TID `setpriority` call, leaving that thread at its
  original niceness. Surfaced as flaky
  `TestLowerSelfNicesAllThreads`. Now does two enumeration
  passes; the second catches any thread that appeared during
  the first. Threads created during the second pass are
  short-lived runtime helpers — vanishingly unlikely to
  affect anything operator-visible.

## [1.1.13] - 2026-05-24

### Security

- **Test fixtures used real device-ID prefixes from the author's
  fleet.** Six conflict-related test files
  (`cmd/dotkeeper/{conflict,e2e_conflict}_test.go`,
  `internal/conflict/{parser,resolver_manual,scanner,watcher}_test.go`)
  embedded the 7-character prefixes that Syncthing displays in
  sync-conflict filenames. While insufficient to impersonate a
  peer (which requires the corresponding TLS private key), the
  leak unnecessarily disclosed which two peers the maintainer
  was running and would have enabled correlation if the same
  prefixes surfaced in unrelated contexts. Replaced with
  obviously-synthetic `AAAAAAA` / `BBBBBBB` test fixtures.
- **CHANGELOG re-leaked a sanitised email.** The 1.0.3 entry that
  documented removing a private routing alias from
  `.github/workflows/aur.yml` cited the literal removed value
  in its explanation, defeating the purpose of the original fix.
  Rewrote the entry to describe the change abstractly.

## [1.1.12] - 2026-05-24

### Added

- **`dotkeeper health --explain` now covers four more warning
  patterns observed in real-world logs.** Coverage was previously
  biased toward push/conflict scenarios; production observation
  surfaced folder-error and handshake failure modes that operators
  were hitting with no guidance:
    - `Folder is in error state` — what triggers the error state,
      how to recover (fix root cause + Override Changes / restart).
    - `Error on folder` — drill into the per-folder log, common
      causes (unreadable file, bad permissions, misreporting FS).
    - `Failed initial scan` — root doesn't exist, no read
      permission, or broken Stat (some FUSE mounts).
    - `Failed to exchange Hello messages` — TLS / BEP-protocol
      setup failed; clock skew (>15 min) or incompatible
      Syncthing major version are the persistent-case causes.

## [1.1.11] - 2026-05-24

### Added

- **`dotkeeper health --watch=DURATION`** clears the screen and
  re-renders the report every DURATION (e.g. `30s`, `2m`).
  Useful for a tmux pane or dashboard that should always show
  the current operational state without an operator typing the
  command manually. Render errors are logged inline and the
  loop continues — a dashboard black-screening because of one
  bad tick is worse than a stale one. Ctx.Done propagates so
  Ctrl-C and daemon-shutdown exit cleanly. Watch mode exits 0
  on cancel; single-shot mode (default) keeps the existing
  exit-1 on degraded behaviour for systemd-timer wrapping.

## [1.1.10] - 2026-05-24

### Changed

- **`dotkeeper health` degraded footer enumerates the
  triggering conditions.** Old output was `[dotkeeper] degraded
  — see above`, forcing the operator to re-scan the report to
  find which threshold tripped. New output:
    ```
    [dotkeeper] degraded because:
      - 26 ERROR-level log entries in the last hour
      - 5 repo(s) with git activity newer than the last backup
    ```
  Reasons render in operationally-most-actionable-first order
  (recent errors → push failures → lagging backups → never-
  backed-up) so the operator's eye lands on the most urgent
  fix-it surface first.
  The trigger logic itself consolidates into `degradedReasons()`
  — single source of truth for "what counts as degraded."

## [1.1.9] - 2026-05-24

### Added

- **`dotkeeper health --explain`** prints a one-line
  operator-facing explanation for each recognised
  warning/error message kind in the report. Initial
  known-pattern table covers eight Syncthing/dotkeeper messages
  most likely to appear on a healthy fleet (ClusterConfig folder
  mismatches, path-conflict on auto-accept, flip-flopping
  listener, per-file sync failure, abandoned index handler,
  propagator no-route, missing-version, deleted-dir-contains-
  ignored-files). Turns the health command from "what's
  happening" into "what's happening AND what to do about it".
  Unknown patterns are silently skipped; the mode is opt-in
  help, not noise.

### Changed

- **Test fixture scrub:** `cmd/dotkeeper/transport_cli_test.go`
  was using the maintainer's personal username `alice` as an SSH
  user in synthetic test cases. The v1.0.1 scrub (PR #48) caught
  the same pattern in `gitssh_test.go` but missed this file.
  Replaced with synthetic `alice` throughout.

## [1.1.8] - 2026-05-24

### Added

- **`dotkeeper health` reports binary version + daemon PID/start
  time.** The new "Daemon" section shows which version produced
  the report and whether the `dotkeeper start` process is
  currently running (with PID and uptime). Useful for:
  - Correlating downstream alerts ("ErrorsLastHour=N from build
    X started at Y") against specific shipped fixes — the
    health command's signal interpretation has shifted across
    recent releases.
  - Distinguishing "daemon up but reporting issues" from
    "daemon dead" without a separate `pgrep`.
  JSON exposes `build` and `daemon-pid` / `daemon-started-at`
  fields for tooling. Health renders "not running (no
  `dotkeeper start` process found)" when no daemon is present —
  the command must work during outages.

## [1.1.7] - 2026-05-24

### Added

- **Per-warning-kind last-hour split.** The top-warnings
  breakdown introduced in v1.1.6 now shows each kind as
  `24h-total / last-1h-subset`, so an operator can distinguish
  chronic historical patterns ("351 / 0" → file the ticket
  later) from currently-flapping issues ("12 / 12" → started
  recently, investigate now). Same recency-aware signal-quality
  approach the v1.1.5 last-hour error split applied at the
  aggregate level, now per-kind.

## [1.1.6] - 2026-05-24

### Added

- **`dotkeeper health` shows top warning kinds.** The 24h
  warning count is now supplemented with a top-5 breakdown by
  message kind. An operator looking at "Warnings in log: 360"
  used to have no way to tell whether that was one chronic
  problem or 360 distinct issues — same number, completely
  different triage. Now:
    ```
    Warnings in log:         366
      Top warning kinds:
          324  Unexpected folder ID in ClusterConfig; ensure …
           24  Abandoning old index handler in favour of new …
            9  Failed to sync
            ...
    ```
  Structured `TopWarningKinds[]` field in JSON output for
  downstream tooling.

## [1.1.5] - 2026-05-24

Same false-positive class as v1.1.4, this time for the
`Errors in log` health signal.

### Fixed

- **Historical log errors no longer trigger degraded status.**
  A `dotkeeper health` against a fleet whose log still contains
  the `.claude/worktrees` pre-v1.0.1 entries was showing "Errors
  in log: 337" and reporting degraded — even though those errors
  were from before the underlying bugs were fixed. Same
  signal-quality problem as the dormant-repo case: counting
  historical events permanently marks a now-healthy daemon as
  degraded and trains operators to ignore the command.
  Split the error counting: `ErrorCount` stays the 24h total
  (for display context), and a new `ErrorsLastHour` field is
  the `degraded()` trigger. Text output shows
  `Errors (24h / last 1h): N / M` so operators see both the
  context and the actionable subset.

## [1.1.4] - 2026-05-24

Fixes a false-positive degradation signal in `dotkeeper health`.

### Fixed

- **Dormant repos no longer flagged as stale.** Previous bucketing
  classified any backup older than 7 days as `Very stale` and
  triggered `degraded` status. On the production fleet this
  flagged 16 archived projects with no recent git activity — the
  backup WAS correctly current; there just wasn't anything new to
  back up. Operators learned to ignore the signal.
  Rework the bucketing to compare backup-at against the git HEAD
  commit timestamp:
    - **Idle** (new): backup is old by age, but git is also old —
      the backup is correctly current.
    - **Lagging (1-7d / >7d)**: git activity is newer than the
      backup — the actual operational concern.
    - **LaggingBackups[]** (new JSON field): names the repos
      with the worst lag, worst-first; renders as a "Lagging
      backups" table in text mode.
  `degraded()` now triggers only on actual lagging (or
  never-backed) repos. 10-minute grace window prevents flapping
  during normal user-edit-then-reconcile cycles.

## [1.1.3] - 2026-05-24

Same-day patch on v1.1.2: closes a startup-race window where the
peer-presence tracker would stay silent for the first 5 minutes
after every daemon start.

### Fixed

- **Peer-presence tracker missed Syncthing's first startup window.**
  Observed in production: the daemon's reconcile loop starts
  ~4 seconds before Syncthing's HTTP API binds, so the tracker's
  initial `GetConnections` call hit `connect: connection refused`,
  silently failed (DEBUG log), and waited the full reconcile
  interval (5 min default) before retrying. During that window
  `state.LastSeenPeers` stayed empty and `dotkeeper health`
  rendered connected peers as "never seen" — exactly the
  operational blind spot the tracker exists to eliminate.

  Add a short-backoff retry loop before settling into the regular
  ticker: 2s, 5s, 10s, 30s. Caps at the regular interval so
  steady-state traffic is unchanged. Stops retrying as soon as one
  update succeeds.

## [1.1.2] - 2026-05-24

Closes the loop on the v1.1.1 health-command work: the daemon now
populates `state.LastSeenPeers` so forensic queries ("when did I
last sync with peer X?") have a useful answer even when the
daemon is down.

### Added

- **Peer-presence tracker.** On every reconcile tick the daemon
  queries Syncthing's `/rest/system/connections`, identifies the
  currently-connected peers, and persists their device IDs +
  observation timestamp to `state.LastSeenPeers` via
  `MutateStateV2`. Runs in its own goroutine so a slow API call
  can't stall reconcile; best-effort so a flaky API can't crash
  the daemon.
  Combined with the v1.1.1 live-connection lookup,
  `dotkeeper health` now always shows useful peer freshness data:
  live observation when the daemon is up, cached state observation
  when the daemon (or Syncthing) is down.

## [1.1.1] - 2026-05-24

Same-day follow-up to v1.1.0: fixes a misleading "never seen"
peer status in `dotkeeper health` and adds a property pin on the
v1.0.2 conflict-resolver safeguard.

### Fixed

- **`dotkeeper health` reported every peer as "never seen"** on
  installs where the reconcile cycle hadn't populated
  `state.LastSeenPeers`. The command now also queries
  Syncthing's `/rest/system/connections`. Priority order is
  live-observation > cached state > zero, so the timestamp
  reflects current connectivity when the daemon is up and
  falls back to whatever the cache has when the daemon is down.

### Added

- **`TestResolverNeverRevertsHistoricalContent`** —
  property-style pin on the invariant the v1.0.2 stale-peer
  safeguard exists to enforce: no auto-resolved commit may
  contain content that already exists at an earlier commit in
  the file's history. 30 random {history, theirs} iterations
  per run, fixed PRNG seed so failures are reproducible.

## [1.1.0] - 2026-05-24

Minor release: new operational-health CLI command, hardened CI,
and baseline microbenchmarks for hot paths.

### Added

- **`dotkeeper health`** — operational health snapshot. Where
  `dotkeeper status` answers "is this configured correctly?",
  `health` answers "is the configured state actually working?".
  Shows repo freshness (age buckets: fresh / 1–7d stale / >7d
  stale / never), per-peer last-seen timestamps, and a 24h tail
  of the syncthing.log for conflict-resolve / push-failure /
  ERROR / WARN counts. Supports `--json` for downstream tooling
  and exits nonzero when degraded so it can be wrapped in
  systemd timers or `... || alert-me` chains. Designed so that
  silent-degradation classes (the cost-model poisoning, the
  stale-peer revert) would surface in a daily health report
  rather than only at audit time.

- **Microbenchmarks for daemon hot paths.**
  `internal/transport/bench_test.go` and
  `internal/config/bench_test.go` pin order-of-magnitude targets
  for `CostModelPredict` (<100 ns), `Manager.Route` (<1 µs),
  `RecordTransfer` (<500 ns), `LoadMachineConfigV2` (<200 µs).
  The repo had no benchmarks at all before this; future
  refactors that add a syscall or O(n) loop to a hot path will
  show up at review time.

- **Coverage fill for paths flagged by the v1.0.3 audit**:
  `RemovePeerReachability` idempotent + non-recoverable paths,
  simultaneous peer+folder freshness pickup, conflict-file
  vanish-during-read.

### Changed

- **Fuzz smoke per-target budget** bumped from 20s → 30s.
  Intermittent "context deadline exceeded" with the 20s budget
  on `FuzzMachineConfigV2RoundTrip` /
  `FuzzExpandContractPath` — the harness's internal deadline
  raced with the last iteration's completion. 30s leaves
  enough slack while keeping the smoke job under 5 minutes
  total.

- **`FuzzMachineConfigV2RoundTrip` hardened** against silent
  stalls: `name` input bounded at 4096 bytes, env-mutation
  section serialised with a process-local mutex to prevent
  GOMAXPROCS goroutines from racing on `t.Setenv`.

## [1.0.3] - 2026-05-23

Third patch on release day. Tightens info-leak hygiene and
stabilises the fuzz smoke harness.

### Changed

- **AUR committer identity scrubbed.** `.github/workflows/aur.yml`
  switches the `git config user.email` and both PKGBUILD
  `# Maintainer:` lines from a private routing alias to
  `julian-corbet@users.noreply.github.com` (the canonical GitHub
  identity for automated commits). Functionally equivalent; the
  noreply form discloses less about the maintainer's
  infrastructure to readers of public workflow logs and published
  PKGBUILDs.

### Fixed

- **`FuzzMachineConfigV2RoundTrip` stalled CI smoke**. Without an
  input-size bound the fuzzer eventually generated multi-MB
  `name` strings whose TOML round-trip dominated the per-target
  20s smoke deadline. Without intra-process serialisation of the
  `t.Setenv("XDG_CONFIG_HOME", …)` call, the GOMAXPROCS goroutines
  in a single fuzz worker raced on the process-global env var and
  could end up doing file I/O under each other's tmp directories.
  Bounded `name` to 4096 bytes (well above any realistic machine
  name) and serialised the env-mutating section with a process-
  local mutex; cross-process parallelism is preserved.

## [1.0.2] - 2026-05-23

Second patch on release day. Two latent bugs surfaced during v1.0.1
operation; both now fixed with regression tests, plus
release-process documentation.

### Fixed

- **Stale-peer auto-revert in the conflict resolver.** When a
  Syncthing sync-conflict file represents an older version of a
  file from a peer that hasn't caught up yet, and the local file
  is unchanged from HEAD, `git merge-file ours base theirs`
  trivially treats `theirs` as a clean merge and silently
  overwrites the canonical local content with the stale version,
  committing the revert as `auto: resolve sync conflict in <file>`.
  Symptom in the wild: during a multi-file release a not-yet-
  caught-up peer briefly came online and produced a sync-conflict
  per touched file; the resolver auto-merged every one and the
  local tree ended up with a long string of revert commits.
  Safeguard: when `ours == base`, return `ActionKeep` without
  merging. Pinned by
  `TestResolveTextMergeKeepsWhenLocalMatchesHEAD`.

- **`daemonPropagator` peer and folder staleness.** Peers added to
  `machine.toml` and folders added via `dotkeeper track` after the
  daemon started were silently ignored by the propagator until
  restart — `Syncthing` still carried changes via BEP gossip, but
  the `git push` path never fired. Replaced the captured snapshots
  with `peersSource` / `foldersSource` callbacks queried fresh on
  every `PropagateNewCommit`. Pinned by
  `TestDaemonPropagatorPicksUpPeerAddedAfterConstruction` and
  `TestDaemonPropagatorPicksUpFolderAddedAfterConstruction`.

### Added

- **CONTRIBUTING.md "Release process" section** documenting the
  pre-release checklist (CI green, CHANGELOG, SECURITY.md table),
  the annotated-tag command, what each downstream workflow (AUR,
  Homebrew, Docker) produces, and the post-release verification
  steps.

- **`TestDiscoverWithCancelledContextStillCompletes`** pins the
  suspend-then-resume race: `Manager.Discover` called with a
  pre-cancelled context must return promptly and leave the routes
  table well-formed so the next live-context call can repopulate
  cleanly.

## [1.0.1] - 2026-05-23

Patch release. Four production bugs surfaced in the v1.0.0 multi-transport
pipe after real-world use; this release fixes all of them, hardens the
test surface against regression, and tightens CI's gofmt enforcement.

### Fixed

- **Cost-model poisoning by SyncthingTransport.** Syncthing's
  `PropagateChange` is a no-op (BEP gossip runs in the embedded daemon),
  so the call returns in microseconds regardless of how long the actual
  propagation takes. The propagator was feeding that ~µs elapsed into
  `Manager.RecordTransfer`, which taught `CostModel` that Syncthing is
  infinitely fast. After one observation `Manager.Route` picked Syncthing
  for every change, defeating the v1.0 router. `Transport` now exposes
  `PropagatesSynchronously() bool`; the propagator skips `RecordTransfer`
  for transports that report false.

- **GitSSHTransport hardcoded `refs/heads/main`.** Pushes to peers were
  always landing on `refs/heads/main` regardless of the local branch.
  For any repo on `master`, `dev`, or any other branch, the peer's
  working tree silently stopped receiving updates via this transport
  (Syncthing still kept files in sync but the git ref diverged). Now
  resolves the destination via `git symbolic-ref --short HEAD` and falls
  back to `main` only when HEAD is detached.

- **`remoteURL` over-bracketed `host:port` addresses.** Single-colon
  addresses were treated as IPv6 literals and wrapped in brackets,
  producing `ssh://[host:port]/path` which SSH treats as unresolvable.
  Current shipping resolvers (Tailscale) never emit a port so this never
  bit, but any future static-config resolver returning `host:port` would
  have hit it. Bracket only when `Count(addr, ":") >= 2` (IPv6 always
  has at least two colons).

- **`.claude/worktrees/` propagating via Syncthing.** Each Claude Code
  agent run creates a nested `git worktree` under `.claude/worktrees/`.
  Without an anchored ignore entry, Syncthing tried to replicate the
  transient worktrees across peers and then perpetually flapped on
  `delete dir: directory has been deleted on a remote device but
  contains ignored files` warnings every time an agent finished
  locally. Added `.claude/worktrees` to `DefaultSyncIgnorePatterns`; the
  rest of `.claude/` (settings.json, agents/, hooks/, CLAUDE.md) keeps
  syncing normally.

### Added

- **gofmt as a hard CI gate.** The lint workflow now runs
  `gofmt -l $(git ls-files '*.go')` and fails on any non-empty output.
  `golangci-lint` doesn't enforce gofmt by default, so drift was
  accumulating silently.

- **CI status badge in README.** Visible signal that `main` is currently
  passing without clicking through to Actions.

- **End-to-end propagator test** wires the real `Manager`, real
  `GitSSHTransport`, real `git push`, real `daemonPropagator`, and real
  `estimateLastCommitSize` against two real git repos, then asserts the
  destination's working tree contains the new file with the right
  content. Catches regressions across every layer of the multi-transport
  pipe.

- **Regression tests** pinning each fix above so a future refactor has
  to update the test deliberately rather than reintroduce the bug
  silently.

### Changed

- **`Transport` interface gains `PropagatesSynchronously() bool`.**
  Required addition for the cost-model fix; implementations must declare
  whether their `PropagateChange` returns a meaningful duration.

- **SECURITY.md supported versions table** updated to cover 0.9.x and
  1.0.x.

- **Repo-wide `gofmt`** applied to clear pre-existing drift.

- **Test fixtures in `internal/transport/gitssh_test.go`** scrubbed of a
  personal home directory and username; replaced with synthetic
  `/srv/example/repo` and `alice`.

## [1.0.0] - 2026-05-21

**dotkeeper goes multi-transport.** The Syncthing-only era ends with
this release. The full v1.0.0 release ships not just the framework
(below) but also actively uses it: every successful auto-commit
fans out to every paired peer via the Manager's per-change routing
decision, the picked transport executes the push, observed elapsed
time feeds back into the cost model, and a new `dotkeeper bare-init`
CLI configures peer-side repos so direct `git push` updates the
peer's working tree. End-to-end integration tests prove a commit on
machine A appears in machine B's working tree via the new transport
path.

### Added

- **`internal/transport.Manager` — the multi-transport orchestrator.**
  Owns a list of `Transport` implementations and maintains, per
  paired peer, a `Routes` snapshot of which transports are
  currently reachable. Exposes `Route(change, peer)` — a pure
  compute call that returns the optimal `Transport` for one
  specific change based on its size and the peer's known routes.

- **`Transport` is event-driven, not periodic.** Reachability
  discovery (`Discover`) runs at daemon startup, at peer pairing,
  on wake-from-suspend, and on explicit operator request. There is
  no background polling — the assumption is that the set of
  transports that *can* reach a given peer is topology, which
  doesn't change every five minutes. What changes per-change is
  *which* transport is optimal for *this* payload size, and that
  decision is microseconds of arithmetic against the cached
  topology.

- **`CostModel` — auto-adapting routing brain.** Per-(transport,
  peer) linear regression of `cost(bytes) = setupMS +
  bytes * msPerByte`. Parameters are seeded from sensible priors
  (each transport supplies its own — git-ssh is "fast setup, slow
  throughput"; Syncthing is "slow setup, fast throughput") and
  updated on every successful transfer via
  `Manager.RecordTransfer(transport, peer, size, elapsed)`.
  Exponential decay with a 1-day half-life means the model adapts
  to changing conditions (network rerouted, peer load shifted)
  without manual intervention.

  No hardcoded size thresholds. The router asks each available
  transport for its `Predict(sizeBytes)` and picks the minimum.
  Crossover between transports happens *where the math says they
  cross over*, learned from the actual fleet's behaviour rather
  than guessed at design time.

- **`GitSSHTransport`** — first non-Syncthing transport. Manages a
  per-peer git remote (`git remote add` / `set-url`), probes
  reachability via `ssh peer true`, and propagates changes via
  `git push <peer-remote> <commit>:refs/heads/main`. The peer-side
  endpoint is a bare repository at
  `~/.local/share/dotkeeper/repos/<folder-id>.git` on the peer
  host; v1.0.0 documents this prerequisite, v1.1+ will ship a
  `dotkeeper bare-init` helper that provisions it automatically
  over SSH.

- **`TailscaleResolver`** — first non-Syncthing peer-discovery
  mechanism. Parses `tailscale status --json` to map peer names to
  TailscaleIPs. Cached for 30s between invocations so probe-driven
  resolution doesn't re-fork the CLI on every reconcile. Available
  on Linux, macOS, Windows, and BSDs — Tailscale ships an
  identical CLI on all of them and dotkeeper uses only the JSON
  output, which is a stable cross-version contract.

- **`dotkeeper transport` CLI subtree.** Two verbs in v1.0.0:
  - `dotkeeper transport list` — every transport configured in this
    build with its current Available() state.
  - `dotkeeper transport status [peer]` — per-peer route table
    showing reachability, probe RTT, and the cost model's learned
    parameters (setup ms, throughput MB/s, effective sample count).

  v1.1+ adds `rediscover`, `prefer`, and `parameters` for forcing
  refresh and inspecting individual models.

### Design notes (for operators and contributors)

- **Topology vs policy.** v1.0.0's split between "what's reachable"
  (discovered, cached) and "what's optimal right now" (computed
  per-change) is deliberate. Re-probing latency every five minutes
  is wasted work because it doesn't change every five minutes. What
  *does* change between changes is the payload size, and that's
  what the per-change router responds to.

- **Why no hardcoded thresholds.** The naive design ("git for files
  under 1 MB, Syncthing above") is fast to write and wrong
  everywhere except the development machine it was tuned on.
  Different fleets have different network characteristics; the
  crossover where transport A overtakes transport B for size X is a
  function of the actual measurable performance, not a function we
  can guess. The cost-model approach learns it.

- **Boundary of "transport."** The Transport interface is
  *peer-facing* — anything that moves a change from machine A to
  machine B. Local Syncthing operations (pause, schedule, manual
  rescan) stay in `internal/stclient`. Cleanly separating these
  two concerns means the transport graph and the daemon-management
  graph evolve independently.

- **No periodic background work.** v0.9.x added several background
  loops (auto-pause, smart rescan). v1.0.0 deliberately adds
  *none*. Probing happens on events; routing happens per-change;
  the daemon idles cleanly between activities.

### Tests

- **`costmodel_test.go`** — regression math, convergence under
  noisy observations, decay behaviour, edge cases (negative
  observations dropped, zero-variance input falls back to prior,
  Predict never returns negative, prior weight controls
  convergence speed).

- **`manager_test.go`** — Discover populates routes, unavailable
  transports skipped, probe-error handling, Route picks
  lowest-cost (tiny payload → git-ssh, huge payload → Syncthing),
  routing adapts after RecordTransfer feedback, InvalidatePeer
  drops cached entry, tie-breaking favours earlier transport,
  per-(transport, peer) model isolation, concurrency safety under
  -race.

- **`gitssh_test.go`** — Name composition, Available delegates to
  resolver, EnsurePeerReachability tries set-url then add,
  surfaces non-recoverable errors verbatim, wraps unknown-resolver
  errors with ErrPeerUnknown, Probe round-trips, returns
  ErrUnreachable on SSH failure, PropagateChange constructs the
  right push args, surfaces git's stderr verbatim, remote name
  sanitisation, remote URL composition with and without explicit
  user.

- **`tailscale_test.go`** — JSON parsing happy path, case-
  insensitive hostname matching, offline peers still resolve to
  their IPs (reachability is the probe's job), unknown peers
  return ErrPeerUnknown, malformed JSON returns
  ErrResolverUnavailable, absent binary fails Available, cache
  honored within TTL. Skip on Windows because the shell-script
  stub doesn't translate; resolver logic itself is platform-
  independent.

### Reconcile integration (completes v1.0.0)

- **`RealApplier.Propagator`** — new field on the reconcile
  applier. After every successful `GitCommitDirty`, the applier
  invokes `Propagator.PropagateNewCommit(ctx, folderPath)`. The
  daemon-side `daemonPropagator` resolves the folder, asks the
  `Manager` to route the change based on estimated size (from
  `git diff --shortstat`), calls the picked transport's
  `PropagateChange`, and feeds the observed elapsed time back to
  `Manager.RecordTransfer` so the cost model learns from every
  real push.

- **`dotkeeper bare-init [--peer=NAME] [--host=USER@ADDR]`** —
  operator CLI that configures peer-side repos for direct push.
  SSHs to each peer and runs
  `git config receive.denyCurrentBranch updateInstead` in every
  tracked folder. With that setting, a push to the peer's
  currently-checked-out branch atomically updates the working
  tree — no separate bare repo, no post-receive hook, no second
  authoritative copy of the data. Idempotent.

  Address resolution ladder: explicit `--host` override → any
  registered resolver that knows the peer (v1.0.0 has Tailscale;
  v1.1+ adds mDNS and static-hub variants) → the peer name as a
  hostname (so users with `/etc/hosts` entries, `~/.ssh/config`
  Host blocks, or LAN-local DNS need no dotkeeper-side resolver
  configured at all).

- **`GitSSHTransport.remoteURL` targets the working tree
  directly.** Earlier drafts pointed at a separate bare-repo
  path; the v1.0.0 final design pushes to the peer's working
  tree path directly (the v1.0.0 mirror-paths constraint), using
  updateInstead semantics for atomic working-tree updates.

- **Three end-to-end integration tests** in
  `internal/transport/integration_test.go` prove the full path
  works: file:// push to an updateInstead-configured repo
  updates the working tree; the negative control (no
  updateInstead) shows git refuses; the full
  `GitSSHTransport.PropagateChange` code path delivers a commit
  to a real local destination via a URL-rewriting wrapper around
  the exec runner.

- **`TestRealApplierFiresPropagatorAfterCommit`** pins the
  reconcile seam — catches future refactors that would silently
  regress dotkeeper to Syncthing-only propagation.

### Known limitations / v1.1+ roadmap

- **Mirror-paths constraint.** v1.0.0 assumes the same folder
  path on every peer (an existing dotkeeper convention via
  `scan_roots`). v1.1+ adds per-peer path maps for users with
  diverging layouts.

- **No mDNS or static-hub Resolver variants yet.** Only
  `git-ssh+tailscale` and `syncthing` are registered. The
  Resolver interface is designed to take additional resolvers
  without changes to the GitSSHTransport itself; v1.1+ adds the
  variants.

- **Manager doesn't persist learned cost-model parameters.**
  Restarting the daemon resets every model to its prior. Adding
  state.toml persistence for the cost-model parameters is v1.1+;
  the cost is "first ~20 transfers after restart route based on
  defaults" — benign because the defaults are sensible and
  convergence is fast.

- **Propagator fires on every `GitCommitDirty` action, including
  no-op (clean-repo) ones.** Cost is one SSH probe per peer per
  reconcile cycle (~5 min). The optimisation to only fire on
  HEAD-advance is a follow-up; v1.0.0 prioritises correctness
  over the wasted probes.

## [0.9.9] - 2026-05-21

### Added

- **`internal/transport` package — Transport abstraction for peer
  change propagation.** Defines the `Transport` interface every
  inter-peer transport implementation must satisfy: `Name`,
  `Available`, `EnsurePeerReachability`, `RemovePeerReachability`,
  `Probe` (RTT measurement), and `PropagateChange` (the active-push
  path, a no-op for transports whose backing system gossips by
  itself).

  v0.9.9 ships exactly one implementation, `SyncthingTransport`,
  which wraps the existing `stclient` calls so the seam is exercised
  without changing user-visible behaviour. v1.0.0 will add a
  `GitSSHTransport` plus a `TransportManager` that probes both for
  each peer and picks the fastest reachable path.

  The package's dependency surface is deliberately narrow: it owns a
  small `SyncthingClient` interface defined in
  `internal/transport/syncthing.go` rather than importing
  `internal/stclient` directly. Compile-time conformance
  (`var _ transport.SyncthingClient = (*stclient.Client)(nil)`) in
  `cmd/dotkeeper/main.go` guarantees the real client satisfies the
  abstraction; tests don't need a real Syncthing.

### Changed

- Daemon startup now logs `transports available: syncthing` after
  Syncthing's API binds. Operator-facing surface for "what's
  available right now" until the v1.0.0 `dotkeeper transport list`
  CLI lands.

### Design notes (for v1.0.0 readers)

This release is intentionally a pure-refactor stepping stone. None
of the reconcile action handlers have been migrated to call
`Transport.EnsurePeerReachability` yet — they continue to invoke
`stclient` directly. The migration happens in v1.0.0 alongside the
introduction of multiple transports, where the choice "which
transport for this peer" becomes a real decision.

The Transport interface boundary is "anything that talks to a
**peer**" — Syncthing folder-device lists, SSH endpoints. Operations
that talk to the **local Syncthing** (pause/unpause/schedule/
rescan) stay in `internal/stclient` regardless. Cleanly separating
these two concerns is the structural payoff of the refactor.

### Tests

- `internal/transport/syncthing_test.go` — 12 test cases covering:
  Name/Available, EnsurePeerReachability idempotency, the "folder
  not found" path, empty DeviceID rejection, nil-client safety,
  RemovePeerReachability idempotency, Probe round-trip
  measurement, ErrUnreachable when the client is unset, ping
  error propagation, no-op PropagateChange, SetConfig error
  propagation, and a compile-time interface-conformance assertion.

## [0.9.8] - 2026-05-21

### Fixed

- **Cold-start rescan storm shipped in v0.9.7.** A fresh daemon's
  in-memory rescan log was empty, which the v0.9.7 diff interpreted
  as "every folder was rescanned at the epoch — definitely overdue
  for backstop." Result: within seconds of daemon startup, one
  RescanFolderNow fired per managed folder. On a multi-folder fleet
  the burst briefly overwhelmed Syncthing's REST endpoint — one
  folder timed out at the default 5-second client deadline when
  22 rescans hit simultaneously during the v0.9.7 deploy.

  Two-layer fix:

  1. **Daemon-side seeding.** At startup the rescan log is now
     populated with `time.Now()` for every currently-managed folder,
     so the first reconcile observes "just rescanned" rather than
     "never rescanned." Subsequent daemon restarts behave the same
     way — a restart is not a reason to rescan; nothing meaningful
     has happened. The first backstop rescan fires one full backstop
     interval after the daemon's last start (7 days for reliable
     filesystems, 24h for unreliable).

  2. **Diff-side defensive nil-handling.** Zero `LastRescan` is now
     treated as "no information; defer this decision" rather than
     "epoch, overdue." This is the second line of defence if the
     daemon ever fails to seed for some reason, and it also fixes
     the one-shot `dotkeeper reconcile` CLI behaviour: the CLI
     doesn't carry a long-lived rescan log so it always passed
     `LastRescanByPath=nil` to Diff, which previously meant "fire
     RescanFolderNow for every folder on every CLI invocation."
     Now the CLI cleanly emits no rescans — backstop decisions are
     daemon-only.

  Side effect on the misleading log line: with no rescans fired,
  the "daily backstop (untrusted filesystem)" message no longer
  appears on btrfs (or any other reliable filesystem) during
  routine CLI use. The classification itself was always correct;
  the message only ever printed when a backstop *fired*, which on
  reliable filesystems should be once per week.

### Tests

- `TestDiffSmartRescan` updated:
  - "first-cycle (LastRescanByPath nil, no health)" inverted from
    `wantRescan: true` to `wantRescan: false`. The expectation
    encodes the fix.
  - New case "first-cycle with seeded LastRescan=now" confirms
    the seeded-baseline path: a daemon that has just seeded the
    rescan log on startup behaves identically to a daemon mid-way
    through a backstop interval.

## [0.9.7] - 2026-05-21

### Changed

- **Periodic Syncthing-driven rescans replaced by dotkeeper-driven
  reactive rescans.** Canonical `rescanIntervalS` flipped from
  86400 (daily) to **0** (no Syncthing-side periodic scan). The
  fsWatcher (inotify on Linux, FSEvents on macOS,
  ReadDirectoryChangesW on Windows) remains the real-time signal.
  Periodic safety-net rescans are now scheduled by dotkeeper itself
  in response to detectable conditions, not on a calendar.

  Existing folders carried over from any earlier release are
  migrated to the new value on first reconcile after upgrade by
  the v0.9.5 drift detector (extended to cover the new canonical).

### Added

- **Cross-platform filesystem-event-API reliability detection.**
  New `internal/watchhealth` package classifies each managed
  folder's storage backend at registration time:
  - **Linux**: `statfs(2)` magic number lookup. Reliable set:
    ext{2,3,4}, btrfs, xfs, zfs, tmpfs, f2fs, squashfs, overlayfs.
    Unreliable set: nfs, cifs, smbfs, 9p, fuse, fuseblk, virtiofs,
    reiserfs.
  - **macOS**: `statfs(2)` `Fstypename`. Reliable: apfs, hfs,
    msdos, exfat. Unreliable: nfs, smbfs, afpfs, webdav, autofs,
    osxfuse, macfuse. (Apple's FSEvents documentation explicitly
    states events are not generated for network mounts.)
  - **Windows**: `GetVolumeInformation` for the FS name plus
    `GetDriveType` to catch SMB-via-redirector cases where the
    name reports as "NTFS" but the volume is remote. Reliable:
    NTFS, ReFS, FAT32, exFAT. Unreliable: CIFS/SMB/NFS/WebDAV or
    any volume with `DRIVE_REMOTE`.
  - **Other (BSDs)**: returns `FilesystemUnknown`, which falls
    through to the conservative "treat as unreliable" branch in
    reconcile/diff. Future per-platform expansion is a one-file
    addition.

- **Watcher-overflow and watch-limit detection.** The watchhealth
  tracker accepts `MarkOverflow(path)` and `MarkWatchLimitHit(path)`
  signals; reconcile reads them via the `HealthQuerier` interface
  and emits one-shot `RescanFolderNow` actions. The signals are
  cleared after a successful rescan via the new `HealthResetter`
  interface so each detected miss triggers exactly one recovery
  rescan.

  v0.9.7 wires the watchhealth tracker into the daemon but does not
  yet plumb the overflow signal from fsnotify (Linux
  `IN_Q_OVERFLOW`, Windows `ERROR_NOTIFY_ENUM_DIR`) into
  `MarkOverflow`. The infrastructure is in place; the producer-side
  hook is a v0.9.8 finishing touch. In the meantime overflow
  recovery relies on the backstop interval.

- **Cross-platform suspend/resume detection** via a portable
  heartbeat goroutine that fires on the returned channel whenever
  the wall clock advances more than 2× the heartbeat interval
  between two consecutive ticks. Catches laptop suspend, VM pause,
  container clock jump, and host-side OS sleep with zero
  per-platform dependencies (no D-Bus, no Cocoa, no Win32). Misses
  precisely-timed wakes shorter than 2× the interval; with the
  default 30s heartbeat that means we miss a 50-second sleep but
  catch any 61-second-or-longer one — fine for real-world laptop
  suspends which are minutes to hours.

- **Backstop intervals.** Even when no watcher signal has fired,
  dotkeeper emits a `RescanFolderNow` for each folder on a
  classification-dependent interval:
  - **Reliable filesystems**: 7 days. Covers detector blind spots
    (unknown kernel bug, peer rewriting via a tool that bypassed
    the local kernel).
  - **Unreliable filesystems**: 24 hours. The only signal driving
    change detection on these mounts; there's no fsWatcher to fall
    back on.

- **`stclient.Client.ScheduleRescan(folderID)`** posts to
  Syncthing's `/rest/db/scan?folder=ID` endpoint. URL-encodes the
  folder ID so IDs containing slashes/special characters don't
  break the request.

- **`reconcile.RescanFolderNow` action** carries `FolderID`, `Path`
  (for the watchhealth Reset callback), and `Reason` (verbatim in
  the Describe() output, so log evidence cites the trigger:
  "rescan Syncthing folder X (reason: event queue overflow)").

### Tests

- `internal/watchhealth/tracker_test.go`:
  - Register classifies a real tmpfs/apfs/NTFS path as not-
    Unreliable (host-fs-resilient assertion).
  - Re-Register preserves pending flags (OverflowSeen,
    WatchLimitHit) — re-classification must not silently swallow a
    pending recovery signal.
  - Reset clears one-shot flags but preserves FilesystemType and
    LastReliableEventAt (facts, not pending signals).
  - Mark* on an unknown path is a no-op (race during startup
    between watcher registration and tracker registration).
  - SleepDetector inner loop tested with an injected synthetic
    tick channel so the test doesn't depend on wall-clock
    behaviour that real time.Ticker can't exhibit in CI (host
    doesn't suspend its own test runner).

- `internal/reconcile/diff_test.go`:
  - New `TestDiffSmartRescan` — 11-case matrix covering the
    state machine: wake-event priority, overflow, watch-limit-hit,
    reliable-FS backstop (weekly), unreliable-FS backstop (daily),
    unknown-FS-defaults-to-untrusted, paused folder never
    rescanned, first-cycle-with-no-history.
  - `TestDiffEmitsUpdateScheduleOnDrift` updated for the new
    canonical: 0 is now correct, 86400 is now drift.

- `fakeST` test double extended with `ScheduleRescan` and a
  `RescanRequested` audit log.

### Known limitations / deferred work

- **Overflow signal not yet plumbed from fsnotify**. The activity
  tracker drains fsnotify errors silently. Wiring those errors
  into `watchhealth.MarkOverflow` is mechanical but enough surface
  area that it gets its own change in v0.9.8.
- **In-memory rescan log resets on daemon restart**. After a
  restart every folder appears as "never rescanned" and the
  backstop fires on the first reconcile — one extra rescan per
  folder per restart. Benign; persisting to state.toml is v0.9.8.
- **Folder set captured at startup**. Same trade-off as v0.9.6:
  folders added after startup don't get watchhealth classification
  until the next service restart.

## [0.9.6] - 2026-05-21

### Added

- **Auto-pause idle folders.** A folder that has seen no
  Write/Create/Remove on the local filesystem for longer than
  `IdleThresholdForPause` (24 hours) is paused via Syncthing's
  per-folder `paused` flag. Pausing stops Syncthing's scanner,
  fsWatcher, and BEP gossip for that folder; the index DB stays on
  disk but is unloaded from memory. A paused folder that sees
  activity within `RecentActivityForUnpause` (1 minute) is unpaused
  immediately — the activity tracker emits a hint that triggers a
  reconcile within seconds of the user's first save.

  The motivation: on a fleet with many enrolled repos but only a
  handful actively worked on day-to-day (typical: 22 enrolled, 3
  used in any given week), the per-folder fixed cost — one worker
  thread per folder, even at idle — dominates the daemon's
  steady-state memory and contributes a noticeable share of CPU.
  Auto-pause collapses the active set to "what the user is actually
  touching."

  The user-perceived sync latency on a folder's first touch after
  pause is bounded by the reconcile debounce (~1 second) plus
  Syncthing's resume time (sub-second). Edits inside an unpaused
  folder are real-time as before.

### Implementation

- New `internal/activity` package: a `Tracker` that runs its own
  fsnotify watcher over every managed folder root, records the most
  recent observed event per root, and emits hints on a buffered
  channel. Independent of the conflict watcher because the two
  concerns (per-folder timestamps vs sync-conflict-file detection)
  evolve separately and we want each to survive the other's
  failures.

- `internal/reconcile`: new `PauseSyncthingFolder` and
  `UnpauseSyncthingFolder` actions; `FolderObs` gains `Paused`;
  `Observed` gains `LastActivityByPath` and `Now`. The new
  `autoPauseAction` helper implements the four-state state machine
  ((paused × active) → unpause; (!paused × idle) → pause; rest →
  no-op). `Diff` is still a pure function — `Now` is passed in
  rather than read inside.

- `internal/stclient`: new `Client.SetFolderPaused(folderID, paused)`
  toggles the folder's paused flag via the REST config endpoint.

- `cmd/dotkeeper/main.go`, `cmds_v5.go`: lifecycle wiring. The
  tracker starts alongside the conflict watcher; its hint channel
  is fanned into the existing reconcile-trigger machinery via a
  no-op `os.Chtimes` on `machine.toml`, which the existing fsnotify
  watcher already debounces and converts into a reconcile request.

### Known limitations

- The tracker's root set is captured at daemon startup. Folders
  added or removed *after* startup are not reflected until next
  restart. Working repos restart-survive intact; net result is "a
  newly-added folder doesn't auto-pause until next service
  restart." A reload-on-folder-set-change pass is feasible but
  deferred until a real impact appears.

- One-shot `dotkeeper reconcile` invocations pass nil for the
  activity tracker, so auto-pause decisions never fire from the
  CLI. Intentional: no-history means every folder would look idle
  since startup, and the diff would over-pause. Only the running
  daemon makes auto-pause decisions.

- The unpause path relies on the activity tracker seeing the event
  before Syncthing's own (now-stopped) fsWatcher would have. On a
  paused folder, Syncthing isn't watching, so dotkeeper's tracker
  is the only signal — which is the design, but worth knowing if
  the tracker fails (e.g. inotify watch limit hit). When that
  happens, the timer-driven reconcile (every 5 min by default)
  still picks up paused-but-active folders eventually; just not
  with sub-second latency.

### Tests

- `internal/activity/tracker_test.go` — fresh-startup seed, write
  detection, skipDirs exclusion (.git churn doesn't count as
  activity), auto-add of directories created post-`New()`,
  idempotent `Close`, unknown-root handling.
- `internal/reconcile/diff_test.go` — `TestDiffAutoPause` matrix
  covers all four state transitions plus the hysteresis band, the
  "tracker has no entry" branch, and the "tracker disabled
  entirely" branch.
- `fakeST` test double extended with `SetFolderPaused` and a
  `PausedSet` audit log so applier tests can assert end-to-end
  pause/unpause behaviour without a real Syncthing.

## [0.9.5] - 2026-05-20

### Fixed

- **Reconcile now actively migrates the folder scheduler fields on
  upgrade.** v0.9.4 changed the default `rescanIntervalS` from 60 to
  86400 and added the new value to the `AddOrUpdateFolder` merge
  path, but the merge path only fires when reconcile's diff decides
  the folder needs an add-or-update — which it doesn't when path,
  devices, and marker all look correct. The result: existing
  installs that upgraded from v0.9.3 saw the new code but no
  migration, and their folders stayed at `rescanIntervalS=60`
  indefinitely. The CPU win advertised by v0.9.4 simply did not
  apply on upgrade paths, only on fresh installs.

  v0.9.5 closes the gap with an explicit drift detector. The
  reconciler now reads each folder's `rescanIntervalS` and
  `fsWatcherEnabled` from Syncthing's REST config and compares them
  to the canonical values exported from `internal/stclient`
  (`CanonicalRescanIntervalS`, `CanonicalFsWatcherEnabled`,
  `CanonicalFsWatcherDelayS`). When they drift, the diff emits
  `UpdateSyncthingFolderSchedule`, and the applier calls a new
  `Client.UpdateFolderSchedule` method that PUTs corrected values
  to Syncthing while leaving all other folder fields untouched.
  First reconcile after upgrade migrates every existing folder.

  The check intentionally also fires when `rescanIntervalS=0`
  (Syncthing's "inotify only, no periodic rescan" mode), because
  while 0 is functionally reasonable on Linux, it's not the
  canonical dotkeeper default and should not be reached
  accidentally. A per-folder override knob (planned for a later
  release) is the right path for users who genuinely want 0 or
  3600.

### Tests

- `TestDiffEmitsUpdateScheduleOnDrift`: matrix of canonical vs
  drifted scheduler values, asserting the new action is emitted
  exactly when drift is observed.
- `fakeST` (the test double for `SyncthingClient`) now implements
  `UpdateFolderSchedule` and records the folder IDs it was called
  with, so applier tests can assert end-to-end migration behaviour
  without depending on a real Syncthing.
- Existing `TestDiff` cases that exercise the "folder otherwise
  consistent" path now set `RescanIntervalS` and `FsWatcherEnabled`
  to the canonical values explicitly, since the zero values would
  now (correctly) trigger the drift detector.

## [0.9.4] - 2026-05-20

### Performance

- **Folder rescan interval raised from 60 seconds to 24 hours.** The
  prior 60s value was a defensive holdover from early dotkeeper
  builds, before Syncthing's fsWatcher (inotify on Linux) was
  trusted. Per active folder this meant a full tree walk every
  minute, which dominated daemon CPU on installs with multiple
  tracked folders — visible in profiles as stat/readdir syscalls
  (`runtime.cgocall`) accounting for a large share of total time
  — for no operational benefit, because every real-time change
  was already being picked up by inotify within milliseconds.

  The new daily rescan is purely the safety-net path for the
  vanishingly rare case where inotify briefly drops events under
  extreme kernel memory pressure. fsWatcher remains enabled with a
  1-second coalescing delay, so the user-perceived sync latency is
  unchanged.

  The merge path of `AddOrUpdateFolder` now actively writes
  `rescanIntervalS` (and `fsWatcherEnabled`, `fsWatcherDelayS`) on
  every reconcile, so folders carried over from v0.9.3-and-earlier
  installs migrate automatically on first reconcile after upgrade.
  Without this, existing installs would have stayed at the old 60s
  value indefinitely.

  Per-folder override via `.dotkeeper.toml` is not implemented in
  v0.9.4 — the daily default is what the load-bearing fleet wants
  and a per-folder knob is straightforward to add when genuine
  demand appears.

### Tests

- `TestAddOrUpdateFolderMergesExisting` now asserts the migration
  path: a pre-existing `rescanIntervalS=60` must be overwritten to
  86400 on next reconcile, while unrelated custom fields survive
  the merge. Prevents accidental regression to "preserve user
  customisation" semantics for the scheduler-managed fields.

## [0.9.3] - 2026-05-20

### Performance

- **Consolidated default ignore patterns.** Profiling showed
  Syncthing's `ignore.Matcher.Match` and supporting glob engine
  accounting for a substantial share of total daemon CPU on
  active development trees, dominated by enumerated variant
  families in the prior default list. Collapses:
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
