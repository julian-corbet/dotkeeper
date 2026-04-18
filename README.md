# dotkeeper

[![Release](https://img.shields.io/github/v/release/julian-corbet/dotkeeper?sort=semver&logo=github)](https://github.com/julian-corbet/dotkeeper/releases/latest)
[![License](https://img.shields.io/github/license/julian-corbet/dotkeeper)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/julian-corbet/dotkeeper.svg)](https://pkg.go.dev/github.com/julian-corbet/dotkeeper)
[![Built with Syncthing](https://img.shields.io/badge/built%20with-Syncthing-6eabe0?logo=syncthing&logoColor=white)](https://syncthing.net/)

Sync your repos and dotfiles across machines. Close the laptop, open the desktop, keep working.

dotkeeper combines **embedded Syncthing** for real-time P2P file sync with **git auto-backup** for history and rollback. Single binary, no external dependencies beyond git.

## The problem

You have two computers. You want the same code, configs, and dotfiles on both. Git requires manual commits and pushes. Syncthing alone has no history or rollback. Dotfile managers require manual commands and don't do real-time sync.

No existing tool combines P2P real-time sync with git history. dotkeeper does.

## How it works

```
Machine A                          Machine B
┌──────────────┐    Syncthing     ┌──────────────┐
│ edit file     │ ──── P2P ─────▶ │ file appears  │
│               │   (seconds)     │               │
│ git backup    │ ── push ──▶ GitHub ◀── pull ── │ git backup    │
│ (daily)       │                          (daily)│
└──────────────┘                  └──────────────┘
```

- **Syncthing** (embedded, isolated) syncs file changes in real-time over LAN or internet
- **Git backup** auto-commits and pushes on a staggered schedule so machines never collide
- `.git/` is excluded from Syncthing — each machine maintains its own git state
- Every managed repo gets a `dotkeeper.toml` log file for resilience and discoverability

## Quick start

### Build and install

```bash
git clone https://github.com/julian-corbet/dotkeeper.git
cd dotkeeper
make build && make install
```

Or download a binary from [Releases](https://github.com/julian-corbet/dotkeeper/releases).

> **Note — building with `go install`**
>
> dotkeeper embeds Syncthing as a library, which transitively pulls in
> `lib/api`. That package expects a generator-produced `auto.Assets`
> symbol (Syncthing's web GUI). Since dotkeeper only uses the REST
> API, always build with the `noassets` tag:
>
> ```bash
> go install -tags noassets github.com/julian-corbet/dotkeeper/cmd/dotkeeper@latest
> ```
>
> The `Makefile`, `Dockerfile`, and release workflow all set this tag
> automatically. A naked `go build ./cmd/dotkeeper` will fail with
> `undefined: auto.Assets` — this is expected.

### First machine

```bash
dotkeeper init
# prints your device ID and a join command for the second machine

dotkeeper add ~/Documents/GitHub/my-project
dotkeeper add ~/.config/nvim
dotkeeper install-timer
```

### Second machine

```bash
dotkeeper join <DEVICE-ID-FROM-FIRST-MACHINE>
# connects, syncs config, configures repos automatically

dotkeeper install-timer
```

That's it. Both machines sync in real-time via Syncthing, with git backups on schedule.

## Commands

| Command | Description |
|---------|-------------|
| `dotkeeper init` | Initialize this machine (identity, Syncthing, config) |
| `dotkeeper join <ID>` | Join an existing setup by pairing with another machine |
| `dotkeeper add <path>` | Add a repo or directory to sync |
| `dotkeeper remove <name>` | Stop syncing a repo |
| `dotkeeper pair` | Re-apply config (add devices and folders to Syncthing) |
| `dotkeeper sync` | Run git backup now |
| `dotkeeper status` | Show full status |
| `dotkeeper install-timer` | Install scheduled git backup (systemd/launchd/cron/schtasks) |
| `dotkeeper version` | Print dotkeeper version |
| `dotkeeper start` | Start embedded Syncthing in foreground (for systemd) |
| `dotkeeper stop` | Stop the Syncthing service |

## Configuration

### Three config files

| File | Location | Synced? | Purpose |
|------|----------|---------|---------|
| `machine.toml` | `~/.config/dotkeeper/` | No | Local machine identity (name, slot) |
| `config.toml` | `~/.config/dotkeeper/` | Yes (Syncthing) | Shared settings: machines, repos, ignore patterns |
| `dotkeeper.toml` | In each managed repo | Yes (git) | Per-repo log: which machines, when last synced |

### Backup schedule (`git_interval`)

| Value | Schedule |
|-------|----------|
| `hourly` | Every hour |
| `2h`, `6h`, `12h` | Every N hours |
| `daily` | Once per day (default) |
| `weekly` | Once per week |
| `monthly` | Once per month |

Each machine is offset by `slot * slot_offset_minutes` to avoid push conflicts.

### Ignore patterns

dotkeeper ships with smart defaults that sync lockfiles and editor configs while excluding build artifacts, caches, and conflict-prone state files. See `dotkeeper.toml.example` for the full list.

## Features

- **Single binary** — Syncthing embedded as a Go library, no separate install
- **Fully isolated** — own ports (18384, 12000, 11027), own config, won't interfere with system Syncthing
- **Works anywhere** — local discovery on LAN, global discovery + relay + NAT traversal when remote
- **Smart defaults** — syncs lockfiles and editor configs, excludes build artifacts and volatile state
- **Per-repo breadcrumbs** — `dotkeeper.toml` in each repo tracks sync state for resilience
- **Staggered git backup** — each machine gets a time slot, no push conflicts

## Port isolation

| Resource | System Syncthing | dotkeeper |
|----------|-----------------|-----------|
| API | 127.0.0.1:8384 | 127.0.0.1:18384 |
| Sync | :22000 | :12000 |
| Discovery | :21027 | :11027 |
| Config | ~/.config/syncthing | ~/.local/share/dotkeeper/syncthing |

## Requirements

- **Go 1.23+** (build only)
- **git**
- **Service manager** — systemd (Linux), launchd (macOS), Task Scheduler (Windows), cron (BSD/fallback)
- Internet access for cross-network sync (LAN-only also works)

## License

Copyright (C) 2026 Julian Corbet

This program is free software: you can redistribute it and/or modify it under the terms of the GNU Affero General Public License as published by the Free Software Foundation, version 3.

See [LICENSE](LICENSE) for the full text.

Syncthing (embedded) is licensed under the Mozilla Public License 2.0.
