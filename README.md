# dotkeeper

[![Release](https://img.shields.io/github/v/release/julian-corbet/dotkeeper?sort=semver&logo=github)](https://github.com/julian-corbet/dotkeeper/releases/latest)
[![License](https://img.shields.io/github/license/julian-corbet/dotkeeper)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/julian-corbet/dotkeeper.svg)](https://pkg.go.dev/github.com/julian-corbet/dotkeeper)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/12584/badge)](https://www.bestpractices.dev/projects/12584)
[![Built with Syncthing](https://img.shields.io/badge/built%20with-Syncthing-6eabe0?logo=syncthing&logoColor=white)](https://syncthing.net/)

Sync your repos and dotfiles across machines. Close the laptop, open the desktop, keep working.

## What it does

dotkeeper keeps your code, configs, and dotfiles in sync across however many machines you use. It combines **embedded Syncthing** for real-time P2P file sync with **staggered git backup** for history and rollback. You declare what you want synced in plain TOML files; a reconciler loop continuously converges the live system to match. Single binary, no external dependencies beyond git.

Each repo you want managed gets a `dotkeeper.toml` committed at its root. That file is the opt-in signal: it says which machines should receive the repo, how the git backup should run, and which Syncthing folder backs it. Per-machine settings — your machine's name, slot number, and which directories to scan for repos — live in `~/.config/dotkeeper/machine.toml`. You edit files; dotkeeper handles the rest.

## Why I built it

I run four machines: a desktop, a laptop, a NAS, and a VPS. I want the same repos, dotfiles, and editor configs on all of them without manual `git push`/`git pull` ceremony and without Dropbox-style centralisation that would compromise the git history. Syncthing gives me real-time P2P sync; git gives me rollback and auditability. No existing tool combined both. I built dotkeeper to wire them together with as little ongoing maintenance as possible.

The v0.5 design — per-repo config that travels with the repo, a reconciler loop instead of imperative commands — came from v0.4 hitting write races and silent drift. The architecture document explains the reasoning in full.

## How it works

dotkeeper runs as a daemon. At its core is a reconcile loop:

```
desired  = read_config()      # machine.toml + every tracked dotkeeper.toml
observed = query_state()      # Syncthing REST API + git + filesystem
actions  = diff(desired, observed)
for action in actions:
    apply(action)
```

`diff()` is a pure function. Given the same inputs it always returns the same list of actions. `apply()` is where side effects happen — adding a Syncthing folder, auto-committing a dirty repo, pushing a backup — and every action is idempotent. The daemon triggers reconcile on inotify events (file changes land in milliseconds), on a periodic timer (default five minutes, safety net), and on explicit `dotkeeper reconcile` invocations.

The mesh itself is a Syncthing P2P topology — no central hub. Every peer syncs directly with every other peer. Each machine also runs staggered git backup: machines are assigned slot numbers (0, 1, 2, ...) and back up at different offsets within the configured interval, so no two machines ever race to push the same branch.

`.git/` directories are excluded from Syncthing sync. Each machine keeps its own independent git history and converges through GitHub, not through syncing raw git objects. This keeps the Syncthing mesh fast and avoids the class of problems that come from syncing `.git/` directly.

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

**From source:**

```bash
git clone https://github.com/julian-corbet/dotkeeper.git
cd dotkeeper
make build && make install
```

Or download a pre-built binary from [Releases](https://github.com/julian-corbet/dotkeeper/releases).

> **Note — `go install`**
>
> dotkeeper embeds Syncthing as a library, which requires a build tag to suppress
> the Syncthing web GUI assets. Always build with `-tags noassets`:
>
> ```bash
> go install -tags noassets github.com/julian-corbet/dotkeeper/cmd/dotkeeper@latest
> ```
>
> A naked `go build ./cmd/dotkeeper` will fail with `undefined: auto.Assets`.
> The `Makefile`, `Dockerfile`, and release workflow all set this tag automatically.

### First machine

```bash
# Generate Syncthing identity, write initial state.toml, scaffold machine.toml.
dotkeeper init

# Print this machine's Syncthing device ID — you'll need it when adding peers.
dotkeeper identity

# Edit machine.toml to set your name, slot, and scan roots.
$EDITOR ~/.config/dotkeeper/machine.toml
```

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
```

For each repo you want to manage, drop a `dotkeeper.toml` at its root, commit it, and push:

```toml
# ~/Documents/GitHub/my-project/dotkeeper.toml
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
dotkeeper start    # or: systemctl --user start dotkeeper
```

dotkeeper walks your scan roots, finds every committed `dotkeeper.toml`, and starts managing those repos. No `dotkeeper add` per repo.

### Each additional machine

```bash
# On the new machine:
dotkeeper init
dotkeeper identity    # copy this device ID

# On an existing machine — add the new peer's device ID to state.toml:
# [[peers]]
# name = "laptop"
# device_id = "<DEVICE-ID>"
# learned_at = 2026-01-01T00:00:00Z
# Then:
dotkeeper reconcile

# Back on the new machine — once state.toml syncs via Syncthing:
dotkeeper reconcile
```

The new machine picks up all managed repos via Syncthing delivery and scan-root discovery. No per-repo ceremony on the new machine.

## Commands

| Command | Purpose |
|---------|---------|
| `dotkeeper init` | First-run setup: generate Syncthing identity, write `state.toml`, scaffold `machine.toml` |
| `dotkeeper start` | Run the daemon (invoked by systemd) |
| `dotkeeper reconcile [<path>]` | Force a reconcile pass now; optional path limits scope |
| `dotkeeper status` | Snapshot: last reconcile time, tracked repos, mesh peers, pending work |
| `dotkeeper identity` | Print this machine's Syncthing device ID and name |
| `dotkeeper track <path>` | Register a repo outside any scan root |
| `dotkeeper untrack <path>` | Deregister a tracked override |
| `dotkeeper doctor` | Run self-diagnostic health checks; `--json` for machine-readable output |
| `dotkeeper logs` | Tail the journal for the dotkeeper systemd unit |
| `dotkeeper conflict list` | List Syncthing sync-conflict files across all managed folders |
| `dotkeeper conflict resolve-all` | Batch auto-resolve trivial conflicts (dedup + text merge) |
| `dotkeeper conflict keep <path>` | Delete the conflict variant, keep the current file |
| `dotkeeper conflict accept <path>` | Replace current file with the conflict variant and commit |
| `dotkeeper version` | Print dotkeeper version |

## Architecture

- [docs/architecture.md](docs/architecture.md) — full walkthrough: reconciler model, config file roles, discovery, command reference
- [ADR 0001](docs/adr/0001-per-repo-config.md) — why per-repo `dotkeeper.toml` is authoritative
- [ADR 0002](docs/adr/0002-machine-state-split.md) — `machine.toml` vs `state.toml`: declarative vs tool-owned
- [ADR 0003](docs/adr/0003-reconciler-loop.md) — pure `Diff(desired, observed) → Plan` reconciler
- [ADR 0004](docs/adr/0004-scan-root-discovery.md) — how repos are discovered without `dotkeeper add`

## Status

The reconciler, config schema, and ADRs are done. `internal/reconcile/` has live `Diff()`, action types, and a `Reconciler` struct. What remains for the full v0.5 cut is wiring the reconciler into `cmd/dotkeeper/main.go` and refactoring `internal/gitsync/`, `internal/stengine/`, and `internal/service/` from v0.4's imperative model into apply primitives the reconciler calls. The Syncthing engine, conflict resolution, and git backup all work — the direction of control is changing, not the underlying primitives.

## Port isolation

dotkeeper runs its embedded Syncthing on separate ports so it does not conflict with a system-installed Syncthing instance.

| Resource | System Syncthing | dotkeeper |
|----------|-----------------|-----------|
| API | 127.0.0.1:8384 | 127.0.0.1:18384 |
| Sync | :22000 | :12000 |
| Discovery | :21027 | :11027 |
| Config | `~/.config/syncthing` | `~/.local/share/dotkeeper/syncthing` |

## Requirements

- **Go 1.23+** (build only)
- **git**
- **Service manager** — systemd (Linux), launchd (macOS), or cron (BSD/fallback)
- Internet access for cross-network sync (LAN-only also works)

## License

Copyright (C) 2026 Julian Corbet

This program is free software: you can redistribute it and/or modify it under the terms of the GNU Affero General Public License as published by the Free Software Foundation, version 3.

See [LICENSE](LICENSE) for the full text.

Syncthing (embedded) is licensed under the Mozilla Public License 2.0.
