# dotkeeper

[![Release](https://img.shields.io/github/v/release/julian-corbet/dotkeeper?sort=semver&logo=github)](https://github.com/julian-corbet/dotkeeper/releases/latest)
[![CI](https://github.com/julian-corbet/dotkeeper/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/julian-corbet/dotkeeper/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/julian-corbet/dotkeeper)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/julian-corbet/dotkeeper.svg)](https://pkg.go.dev/github.com/julian-corbet/dotkeeper)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/12584/badge)](https://www.bestpractices.dev/projects/12584)
[![Built with Syncthing](https://img.shields.io/badge/built%20with-Syncthing-6eabe0?logo=syncthing&logoColor=white)](https://syncthing.net/)

**Real-time peer-to-peer sync for your code, dotfiles, and notes — with git history underneath.**

Close the laptop, open the desktop, keep working. Edits propagate in sub-seconds across every paired machine. Git auto-commits on a schedule so you have rollback. No central server, no cloud account, no subscription.

## Is this for you?

dotkeeper is built for the person who works across two or more machines (laptop + desktop, dev box + lab box, multiple sites) and wants:

- **Sub-second sync** of working-tree edits across paired machines, without manual `git push`/`pull`
- **Git history** kept automatically — auto-commit on a schedule, staggered so two machines never race
- **No central infrastructure** — pure peer-to-peer over Syncthing's open mesh
- **Declarative config** — `.dotkeeper.toml` per repo, `machine.toml` per machine, reconciler keeps the live system aligned
- **Single binary, no daemons to manage** — embedded Syncthing means one process, not two

It's NOT for: collaborative editing (use git for that), large binary asset distribution (use a CDN), one-laptop-only setups (you don't need it).

## How it works

dotkeeper is one Go binary that runs as a daemon. It combines:

- **Embedded Syncthing** — real-time P2P file sync over the open Syncthing mesh
- **Reconciler loop** — pure `Diff(desired, observed) → Plan` that continuously converges the live system to what your TOML configs declare
- **Multi-transport routing** — picks per change between Syncthing, `git push` over SSH (Tailscale-resolved), and Mutagen (when installed). A self-tuning cost model learns the right choice per `(transport, peer, repo)` triple over time
- **Staggered git backup** — auto-commits + pushes on a schedule, slot offsets keep machines from racing
- **Performance budgets in CI** — hot-path benchmarks gate merges, so regressions can't ship

### The reconcile loop

```
desired  = read_config()      # machine.toml + local .dotkeeper.toml files
observed = query_state()      # Syncthing REST API + git + filesystem
actions  = diff(desired, observed)
for action in actions:
    apply(action)
```

`diff()` is a pure function — same inputs always produce the same plan. `apply()` is where side effects happen (add Syncthing folder, auto-commit dirty repo, push backup); every action is idempotent. The daemon triggers reconcile on inotify events (file changes land in milliseconds), on a periodic timer (default five minutes, safety net), and on explicit `dotkeeper reconcile` invocations.

### Multi-transport routing

Three ways to move a change between peers coexist:

- **Syncthing's BEP gossip** — universal fallback, always available
- **`git push` over SSH** (Tailscale-resolved hostnames) — fastest for small commits
- **Mutagen** sync sessions — fastest for small-file workloads when `mutagen` is on `PATH`

A cost model picks per change. It starts with sensible priors per transport family, learns from every observed transfer, and (since v1.1.22) actively benchmarks idle tuples in the background so the model self-tunes even for repos that rarely change. See `dotkeeper transport repos` for the current per-(transport, peer, repo) predictions.

`.git/` directories are excluded from Syncthing sync. Each machine keeps its own independent git history; histories converge via git remotes or the GitSSH transport, never by syncing raw git objects. This keeps the mesh fast and avoids the class of problems that come from syncing `.git/` directly.

## Quick start

### Install

**Arch Linux (AUR):**

```bash
paru -S dotkeeper-bin          # pre-built binary
paru -S dotkeeper-git          # builds from main HEAD
```

**macOS / Linux (Homebrew):**

```bash
brew tap julian-corbet/dotkeeper
brew install dotkeeper
```

**From source — use `make`, not `go build` directly:**

```bash
git clone https://github.com/julian-corbet/dotkeeper.git
cd dotkeeper
make build && make install
```

Or download a pre-built binary from [Releases](https://github.com/julian-corbet/dotkeeper/releases).

> **Why `make build` and not `go build ./...`?**
>
> dotkeeper embeds Syncthing as a library. Syncthing's `lib/api` package
> expects generated web-GUI assets (`gui.files.go`) that dotkeeper has no
> use for and does not ship. A plain `go build ./cmd/dotkeeper` therefore
> fails with `undefined: auto.Assets`. Pass `-tags noassets` to skip the
> assets path:
>
> ```bash
> go build  -tags noassets ./cmd/dotkeeper        # build the daemon
> go test   -tags noassets ./...                  # run the test suite
> go install -tags noassets github.com/julian-corbet/dotkeeper/cmd/dotkeeper@latest
> ```
>
> The `Makefile`, `Dockerfile`, CI workflows, and `release.yml` all set
> this tag automatically — when contributing, prefer `make build`/`make
> test` so the tag is never forgotten. If your IDE or linter shows the
> `auto.Assets` error, configure it to pass `-tags noassets` to gopls.

### First machine

```bash
# Generate Syncthing identity, write initial state.toml, scaffold machine.toml.
dotkeeper init

# Print this machine's Syncthing device ID — you'll need it when adding peers.
dotkeeper identity

# Edit machine.toml to set your name, slot, peers, and scan roots.
$EDITOR ~/.config/dotkeeper/machine.toml
```

If `machine.toml` is generated by Home Manager or another declarative system,
run `dotkeeper init --no-service` after activation. dotkeeper preserves the
generated file and only creates runtime Syncthing identity/state.

A minimal `machine.toml` looks like:

```toml
schema_version = 2
name = "desktop"
slot = 0

[discovery]
scan_roots = [
  "~/Documents/GitHub",
  "~/.config/nvim",
]

[[peers]]
name = "laptop"
device_id = "<LAPTOP-DEVICE-ID>"
learned_at = 2026-01-01T00:00:00Z
```

For each repo you want to manage, run `dotkeeper track`:

```bash
dotkeeper track ~/Documents/GitHub/my-project
```

That writes a local `.dotkeeper.toml`, adds dotkeeper/Syncthing control files to `.git/info/exclude`, and writes a managed `.stignore` so those files do not cross the mesh.

```toml
# ~/Documents/GitHub/my-project/.dotkeeper.toml
schema_version = 2

[repo]
name = "my-project"
added = "2026-01-01T00:00:00Z"
added_by = "desktop"

[sync]
syncthing_folder_id = "my-project-a1b2c3"
share_with = ["desktop", "laptop"]
```

Then run the daemon:

```bash
dotkeeper start    # or: systemctl --user start dotkeeper-syncthing.service
```

dotkeeper walks your scan roots, finds local `.dotkeeper.toml` files, and starts managing those repos.

### Each additional machine

```bash
# On the new machine:
dotkeeper init
dotkeeper identity    # copy this device ID

# On an existing machine, add the peer declaratively in machine.toml
# or imperatively:
dotkeeper peer add laptop <DEVICE-ID>
dotkeeper reconcile

# Back on the new machine, materialize local repo configs with Nix/Home Manager
# or run dotkeeper track for the repo paths this machine should manage:
dotkeeper track ~/Documents/GitHub/my-project
dotkeeper reconcile
```

Because `.dotkeeper.toml` is intentionally not synced, each machine needs its own local copy. Nix/Home Manager is the intended low-friction path for repeatable personal topology; `dotkeeper track` is the portable manual path.

### Nix / Home Manager topology

For declarative setups, keep the public dotkeeper program generic and put your
private sync topology in your own flake. A practical pattern is denylist-first:
scan a small set of roots for Git repos, generate local `.dotkeeper.toml` files
for everything discovered, and list only the exceptions.

```nix
{
  scanRoots = [
    "~/Documents/GitHub"
    "~/.config"
  ];

  dontSync = {
    all = [
      "~/Documents/GitHub/dotkeeper"
      "~/Documents/GitHub/archive"
      "~/Documents/GitHub/example-rag/workspace"
    ];

    laptop = [
      "~/Documents/GitHub/video-renderer"
    ];
  };
}
```

Whole denied repos are left unmanaged on that machine. Denied paths inside a
managed repo should be emitted as `[sync].ignore` patterns, so generated data can
stay local while the repo itself still syncs. See
[`docs/examples/home-manager-denylist.nix`](docs/examples/home-manager-denylist.nix)
for a full generic activation sketch.

## Commands

| Command | Purpose |
|---------|---------|
| `dotkeeper init` | First-run setup: generate Syncthing identity, write `state.toml`, scaffold `machine.toml` |
| `dotkeeper start` | Run the daemon (invoked by systemd) |
| `dotkeeper reconcile` | Force a reconcile pass now |
| `dotkeeper status` | Snapshot: last reconcile time, tracked repos, mesh peers, pending work |
| `dotkeeper health` | Operational dashboard: degraded reasons, recent errors, lagging backups, warning kinds; `--explain` for guidance, `--watch=DURATION` for dashboard mode, `--json` for machine-readable |
| `dotkeeper identity` | Print this machine's Syncthing device ID and name |
| `dotkeeper peer add/list/remove` | Manage imperative peers in `state.toml`; declarative peers can live in `machine.toml` |
| `dotkeeper track <path>` | Bootstrap local `.dotkeeper.toml`, local excludes, and tracked override state |
| `dotkeeper untrack <path>` | Deregister a tracked override |
| `dotkeeper doctor` | Run self-diagnostic health checks; `--json` for machine-readable output |
| `dotkeeper conflict list` | List Syncthing sync-conflict files across all managed folders |
| `dotkeeper conflict resolve-all` | Batch auto-resolve trivial conflicts (dedup + text merge) |
| `dotkeeper conflict keep <path>` | Delete the conflict variant, keep the current file |
| `dotkeeper conflict accept <path>` | Replace current file with the conflict variant and commit |
| `dotkeeper transport list` | List configured transports and which are currently available |
| `dotkeeper transport status [peer]` | Per-peer reachability + cross-repo cost-model parameters |
| `dotkeeper transport repos` | Per-(transport, peer, repo) cost-model predictions; supports `--peer` and `--transport` filters |
| `dotkeeper bench-now [--folder=PATH]` | Operator-triggered transport benchmark; runs one 64 KB probe per (synchronous transport, peer) pair and prints measured ms/KB |
| `dotkeeper bare-init [--peer=NAME] [--host=USER@ADDR]` | Configure peer-side git repos for direct `git push` (sets `receive.denyCurrentBranch=updateInstead`) |
| `dotkeeper version` | Print dotkeeper version |

## Architecture

- [docs/architecture.md](docs/architecture.md) — full walkthrough: reconciler model, config file roles, discovery, command reference
- [ADR 0001](docs/adr/0001-per-repo-config.md) — why local per-repo `.dotkeeper.toml` is authoritative
- [ADR 0002](docs/adr/0002-machine-state-split.md) — `machine.toml` vs `state.toml`: declarative vs tool-owned
- [ADR 0003](docs/adr/0003-reconciler-loop.md) — pure `Diff(desired, observed) → Plan` reconciler
- [ADR 0004](docs/adr/0004-scan-root-discovery.md) — how repos are discovered without `dotkeeper add`
- [ADR 0005](docs/adr/0005-state-toml-locking.md) — cross-process state.toml locking
- [ADR 0006](docs/adr/0006-syncthing-v2-migration.md) — Syncthing v2 migration and the pseudo-version pin

## Status

**v1.1.x** is the current release line. Headline pieces since v1.0:

- **v1.1.0 – v1.1.12** — `dotkeeper health` operational dashboard (`--explain`, `--watch`, top warning kinds, JSON output)
- **v1.1.14** — autoAccept storm fix (folder-membership is opt-in per machine, ending the multi-thousand-errors-per-hour Syncthing ClusterConfig loop on partial-overlap fleets)
- **v1.1.15** — opt-in `/debug/pprof` listener (`[debug] pprof_address` in `machine.toml`)
- **v1.1.16** — Diff loop dropped from O(N²) to O(1) repo lookups
- **v1.1.17** — `hashers=1` canonical default (cold-start scans no longer pin multiple cores)
- **v1.1.18** — perf-budget gate in CI (regressions in hot-path benchmarks fail the build)
- **v1.1.19** — `MutagenTransport` with detect-and-fallback
- **v1.1.20** — CostModel keyed per `(transport, peer, repo)`; per-repo learning + cross-repo aggregate fallback
- **v1.1.21** — per-family priors (`mutagen+*`, `git-ssh+*`, `syncthing`) tuned for cold-start routing
- **v1.1.22** — active transport benchmarker (background loop self-tunes the cost model per tuple every 24 h)
- **v1.1.23** — `dotkeeper transport repos` per-folder visibility surface

See [CHANGELOG.md](CHANGELOG.md) for full release notes.

### Performance baseline

Idle steady-state CPU is ~0% — fsWatcher catches changes, no periodic scanner runs, no per-tick scheduler churn. Cold-start scan (daemon launch or wake-from-suspend) is bounded by the `hashers=1` cap so it spreads across time instead of pinning cores. Hot-path Diff is ~190 µs/op for a 30-folder fleet, gated by CI to stay under 285 µs/op.

## Port isolation

dotkeeper runs its embedded Syncthing on separate ports so it does not conflict with a system-installed Syncthing instance.

| Resource | System Syncthing | dotkeeper |
|----------|-----------------|-----------|
| API | 127.0.0.1:8384 | 127.0.0.1:18384 |
| Sync | :22000 | :12000 |
| Discovery | :21027 | :11027 |
| Config | `~/.config/syncthing` | `~/.local/share/dotkeeper/syncthing` |

## Requirements

- **Go 1.26+** (build only)
- **git**
- **Service manager** — systemd (Linux), launchd (macOS), or cron (BSD/fallback)
- Internet access for cross-network sync (LAN-only also works)

## License

Copyright (C) 2026 Julian Corbet

This program is free software: you can redistribute it and/or modify it under the terms of the GNU Affero General Public License as published by the Free Software Foundation, version 3.

See [LICENSE](LICENSE) for the full text.

Syncthing (embedded) is licensed under the Mozilla Public License 2.0.
