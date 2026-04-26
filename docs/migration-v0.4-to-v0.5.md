# Migrating from dotkeeper v0.4 to v0.5

v0.5 is a breaking release. The underlying sync engine — embedded Syncthing
and staggered git backup — is unchanged. What breaks is the config layout
and the CLI: the imperative mutating commands (`add`, `remove`, `join`,
`pair`, `install-timer`, `stop`, `sync`) are deprecated and replaced by a
declarative reconciler model. They still ship in v0.5.0 binaries but will
be removed in v0.6.0. If you have scripts or shell aliases calling them,
plan to migrate now.

This guide covers what changed, how to run the one-time migration, and how
to roll back if something goes wrong.

---

## Why the change

Three architectural decisions drive v0.5. The ADRs have the full reasoning;
here is the short version.

**[ADR 0001](adr/0001-per-repo-config.md) — Per-repo `dotkeeper.toml` as
authoritative config.** The `dotkeeper.toml` v0.4 wrote into each repo was a
passive breadcrumb — just a log of who added it and when. In v0.5 it is the
actual config for that repo: the Syncthing folder ID, which peers receive it,
git backup policy, and per-repo ignore patterns. The central `[[repos]]` list
is removed from `config.toml`.

**[ADR 0002](adr/0002-machine-state-split.md) — `config.toml` splits into
`machine.toml` and `state.toml`.** v0.4's `config.toml` held machine identity,
mesh topology, and shared settings in one mutable file, synced between machines
via Syncthing. That produced write races. v0.5 separates what you author
(`machine.toml`, never synced) from what the tool manages (`state.toml`,
never hand-edited).

**[ADR 0003](adr/0003-reconciler-loop.md) — Reconciler loop replaces mutating
CLI.** Instead of imperative commands poking Syncthing directly, you edit a
file and run `dotkeeper reconcile`. The reconciler computes `diff(desired,
observed)` and applies the result idempotently. Drift between declared state
and actual state is detected and corrected on every pass.

**[ADR 0004](adr/0004-scan-root-discovery.md) — Scan-root discovery.** Without
a central `[[repos]]` list, dotkeeper finds managed repos by walking the
directories you declare as scan roots in `machine.toml`. Clone a repo that
already has a `dotkeeper.toml` under a scan root and dotkeeper picks it up
automatically — no `dotkeeper add` required.

---

## What's removed

The following CLI commands are scheduled for removal. In v0.5.0 they remain
present but deprecated; planned removal is v0.6.0. Each entry explains the
v0.5 replacement you should migrate to now.

### `dotkeeper add <path>`

Wrote a `[[repos]]` entry into `config.toml` and called the Syncthing API.
Both actions no longer reflect how v0.5 manages state.

**Replacement:** Drop a `dotkeeper.toml` into the repo, commit it, push. If
the repo is under a scan root, dotkeeper finds it on the next pass. If it is
outside all scan roots, run `dotkeeper track <path>` to register the path in
`state.toml`'s `tracked_overrides`.

### `dotkeeper remove <name>`

Deleted the `[[repos]]` entry from `config.toml`.

**Replacement:** Delete `dotkeeper.toml` from the repo, commit, push. The
reconciler detects the file is gone and stops managing the repo. To stop syncing
without removing the file, set `share_with = []` in `[sync]`, commit, and
reconcile. For out-of-scan-root repos, use `dotkeeper untrack <path>`.

### `dotkeeper join <DEVICE-ID>`

Ran a multi-step interactive setup: init the machine, add the peer in
Syncthing, wait for `config.toml` to arrive.

**Replacement:** Run `dotkeeper init` on the new machine, copy its device ID
from `dotkeeper identity`, add a `[[peers]]` entry in `state.toml` on an
existing machine, and run `dotkeeper reconcile`. Once `state.toml` syncs to
the new peer via Syncthing, run `dotkeeper reconcile` there too.

### `dotkeeper pair`

A recovery hatch for when the CLI got out of sync with Syncthing.

**Replacement:** `dotkeeper reconcile`. It is the full replacement and is
idempotent by design.

### `dotkeeper install-timer`

Installed the systemd/launchd/cron timer for git backup.

**Replacement:** `dotkeeper reconcile`. Timer installation is now a reconcile
action: if the desired state includes a timer and it is not installed, reconcile
installs it.

### `dotkeeper stop`

Stopped the embedded Syncthing service.

**Replacement:** Stop the service through your system's service manager:
`systemctl --user stop dotkeeper` on Linux,
`launchctl unload ~/Library/LaunchAgents/dotkeeper.plist` on macOS.

### `dotkeeper sync`

Ran a one-shot git backup across all managed repos.

**Replacement:** `dotkeeper reconcile`. Git backup is now a reconcile action.
The reconciler detects repos with unpushed commits and pushes them as part of
the regular pass.

---

## What's added

| Command | Purpose |
|---------|---------|
| `dotkeeper reconcile [<path>]` | Force a reconcile pass; replaces `pair`, `sync`, `install-timer` |
| `dotkeeper identity` | Print this machine's Syncthing device ID and configured name |
| `dotkeeper track <path>` | Register a repo outside all scan roots (writes to `state.toml`) |
| `dotkeeper untrack <path>` | Remove a tracked override |

---

## Automatic migration on first run

When v0.5 encounters a v0.4 install (a `config.toml` in `~/.config/dotkeeper/`
with no `schema_version` field), it detects this and runs a one-time migration
shim. You can also trigger it explicitly:

```bash
dotkeeper reconcile --migrate-from-v0.4
```

The migration runs these steps in order:

1. **Backs up** `~/.config/dotkeeper/config.toml` to
   `~/.config/dotkeeper/config.toml.v0.4-backup`.

2. **Writes `machine.toml`** to `~/.config/dotkeeper/machine.toml`, deriving
   `scan_roots` from the unique parent directories of all repos listed in
   `[[repos]]`.

3. **Writes `state.toml`** to `~/.local/state/dotkeeper/state.toml`, extracting
   the `[machines]` table (as `[[peers]]` entries), the Syncthing device ID,
   and any cached observation state.

4. **Upgrades each `dotkeeper.toml`** in the repos listed under `[[repos]]`
   in the old `config.toml`. The v0.4 breadcrumb held only `[repo]` with
   `name`, `added`, and `added_by`. Migration rewrites the file to the v0.5
   schema (see worked example below) and sets `schema_version = 2`.

5. **Removes `config.toml`** once migration is confirmed successful.

After migration, `dotkeeper reconcile` runs normally. Run `dotkeeper status`
to verify all repos and peers are visible.

> Mixed-version meshes (some machines on v0.4, some on v0.5) work at the
> Syncthing wire level, but v0.4 machines keep reading `config.toml`, which
> is no longer the source of truth once any peer migrates. Upgrade all
> machines promptly.

---

## Worked example

### v0.4 state

`~/.config/dotkeeper/config.toml`:

```toml
[sync]
git_interval = "daily"
slot_offset_minutes = 5
auto_resolve_conflicts = true

[syncthing]
ignore = [".git", "*.sync-conflict-*", "node_modules"]

[machines.desktop]
hostname = "CORBET-CACHYOS"
slot = 0
syncthing_id = "AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA"

[machines.laptop]
hostname = "CORBET-ELITEBOOK"
slot = 1
syncthing_id = "BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB"

[[repos]]
name = "dotfiles"
path = "~/Documents/GitHub/dotfiles"
git = true

[[repos]]
name = "nvim"
path = "~/.config/nvim"
git = true
```

`~/Documents/GitHub/dotfiles/dotkeeper.toml` (v0.4 breadcrumb):

```toml
[repo]
name = "dotfiles"
added = "2026-01-15T09:30:00Z"
added_by = "desktop"
```

### After migration

`~/.config/dotkeeper/machine.toml`:

```toml
# dotkeeper machine config (v2) — declarative, non-secret
schema_version = 2
name = "desktop"
slot = 0
default_commit_policy = "manual"
default_git_interval = "daily"
default_slot_offset_minutes = 5
reconcile_interval = "5m"
default_share_with = []

[discovery]
scan_roots = [
    "~/Documents/GitHub",
    "~/.config/nvim",
]
exclude = []
scan_interval = "5m"
scan_depth = 3
```

`~/.local/state/dotkeeper/state.toml`:

```toml
# dotkeeper state (v2) — tool-owned, do not hand-edit
schema_version = 2
syncthing_device_id = "AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA"
tracked_overrides = []

[[peers]]
name = "laptop"
device_id = "BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB"
learned_at = 2026-01-15T09:30:00Z
```

`~/Documents/GitHub/dotfiles/dotkeeper.toml` (upgraded to v0.5):

```toml
# Managed by dotkeeper — https://github.com/julian-corbet/dotkeeper
# This file is tracked in git. Its presence opts this repo into dotkeeper management.
schema_version = 2

[repo]
name = "dotfiles"
added = "2026-01-15T09:30:00Z"
added_by = "desktop"

[sync]
syncthing_folder_id = "dotfiles-a1b2c3"
ignore = []
share_with = []

[commit]
policy = ""
idle_seconds = 0

[git_backup]
interval = ""
skip_slots = []
```

Empty string fields mean "inherit from `machine.toml` defaults." The migration
generates a `syncthing_folder_id` for repos that didn't have one. `share_with`
left empty means "all known peers" — equivalent to the v0.4 behaviour.

### Commit the upgraded files

Each repo's `dotkeeper.toml` was rewritten. Commit and push in each repo so
the upgrade propagates to other machines via git:

```bash
cd ~/Documents/GitHub/dotfiles
git add dotkeeper.toml
git commit -m "chore: upgrade dotkeeper.toml to v0.5 schema"
git push
```

---

## Rollback

If something goes wrong and you need to go back to v0.4:

1. **Reinstall the v0.4 binary** from the
   [Releases page](https://github.com/julian-corbet/dotkeeper/releases).

2. **Restore `config.toml`:**

   ```bash
   cp ~/.config/dotkeeper/config.toml.v0.4-backup ~/.config/dotkeeper/config.toml
   ```

3. **Remove the v0.5 files** (v0.4 does not read them):

   ```bash
   rm ~/.local/state/dotkeeper/state.toml
   ```

4. **Reinstall the timer:**

   ```bash
   dotkeeper install-timer
   ```

All your files, Syncthing sync state, and git history are preserved. The
upgraded `dotkeeper.toml` files in each repo won't break v0.4 — it reads only
`[repo].name`, `[repo].added`, and `[repo].added_by` and ignores everything
else — but if other machines have already pulled the upgraded files, a rollback
on one machine creates a schema mismatch. The mismatch is harmless in practice.

---

## FAQ

**Do I have to upgrade all machines at once?**

No, but do it quickly. Mixed-version meshes work at the Syncthing level.
The risk is that v0.4 machines keep reading the old `config.toml`, which is
no longer maintained once any machine migrates. A v0.4 machine that comes
online weeks after migration will have stale mesh topology until it is upgraded.

**Will the migration touch my actual repo files?**

No. Migration rewrites only the `dotkeeper.toml` files at repo roots and the
dotkeeper config files in `~/.config/dotkeeper/` and
`~/.local/state/dotkeeper/`. Your actual source files, git history, and
Syncthing sync state are untouched.

**What happens if migration fails partway through?**

The backup at `config.toml.v0.4-backup` is written before anything else is
modified. If migration fails, restore it:

```bash
cp ~/.config/dotkeeper/config.toml.v0.4-backup ~/.config/dotkeeper/config.toml
```

Then file a bug with the error output.

**My repo lives outside any scan root. What do I do?**

Run `dotkeeper track <path>` after migration. This writes the path to
`state.toml`'s `tracked_overrides` list. The reconciler treats it as tracked
on all subsequent passes, regardless of scan-root membership.

**I had ignore patterns in `config.toml`'s `[syncthing]` block. Where do they go?**

Per-repo ignore patterns go in `[sync].ignore` inside that repo's
`dotkeeper.toml`. Machine-level defaults live in the Syncthing `.stignore`
files that dotkeeper manages. The migration moves the shared ignore list from
`config.toml` into the baseline `.stignore` that the reconciler writes for
each Syncthing folder. Review `dotkeeper status` after migrating to confirm the
patterns are in effect.
