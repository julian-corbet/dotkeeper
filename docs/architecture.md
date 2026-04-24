# dotkeeper v0.5 Architecture

A walkthrough of the design decisions that shape v0.5 — synthesised from
[ADR 0001](adr/0001-per-repo-config.md),
[ADR 0002](adr/0002-machine-state-split.md),
[ADR 0003](adr/0003-reconciler-loop.md), and
[ADR 0004](adr/0004-scan-root-discovery.md).
The ADRs are the authoritative decision record; this document is the readable
narrative.

---

## At a glance

- dotkeeper syncs your repos and dotfiles across machines — P2P real-time sync
  via embedded Syncthing, plus staggered git backup for history and rollback.
- **v0.4** stored all settings in a central `config.toml` that the CLI mutated.
  One file, everything in it, write races across machines.
- **v0.5** replaces that with a reconciler model: desired state lives in
  config files, the daemon continuously converges the running system to match
  it. No more imperative `add`/`remove`/`join`.
- Per-repo config travels with the repo (`dotkeeper.toml` in the repo root),
  machine-level config is local to each machine (`machine.toml`). Neither
  file is the same file — no more write races.
- Discovery is opt-in at two levels: you declare which directories to scan,
  and each repo opts in by committing a `dotkeeper.toml`.

---

## The reconciler model

The central idea in v0.5 is that dotkeeper behaves like Flux or ArgoCD: instead
of executing mutations in response to commands, it continuously computes the
gap between what you declared and what is actually running, then closes it.

```
reconcile():
    desired  = read_config()      # machine.toml + every tracked dotkeeper.toml
    observed = query_state()      # Syncthing REST API + git + filesystem
    actions  = diff(desired, observed)
    for action in actions:
        apply(action)
    record_observation(observed)
```

`diff()` is a pure function. Given the same inputs it always returns the same
list of actions. `apply()` is where side effects happen, and each action is
idempotent — applying it twice is the same as applying it once. That property
is what makes it safe to run reconcile on a timer, on every file change, and
on demand, all without risk of double-applying anything.

This is pull-based, not push-based. The daemon does not wait to be told what
to do. It reads the declared state, reads the observed state, and acts on the
difference. If something changes outside dotkeeper's knowledge — a Syncthing
folder edited by hand, a peer pairing revoked — the next reconcile notices
and either corrects the drift or surfaces it.

### Why this matters

In v0.4, drift was invisible. A Syncthing folder edited outside dotkeeper
would not be noticed until something broke. In v0.5, `diff()` always
computes desired-vs-observed, so drift is detected on the next reconcile
pass — by default within five minutes, or instantly if an inotify event fires.

Testability also improves. `diff()` takes data in, returns actions out. No
Syncthing, no git, no filesystem needed for unit tests. The side-effectful
`apply()` layer is tested separately.

### Triggers

Four ways to invoke reconcile:

| Trigger | When |
|---------|------|
| inotify watcher | On any change to `machine.toml`, a tracked `dotkeeper.toml`, or `.git/HEAD` |
| Periodic timer | Every `machine.reconcile_interval` (default 5 min) — safety net |
| `dotkeeper reconcile [<path>]` | Manual one-shot, blocks until complete |
| Local webhook (future) | `POST /reconcile` on a local HTTP socket |

The inotify path fires in milliseconds. The timer catches anything inotify
missed — network-delivered changes, remote Syncthing events, startup gaps.

### Kindred systems

The reconciler pattern is the same one used by Flux CD, ArgoCD, Kubernetes
controllers, Nix, home-manager, and systemd unit management. If you have
used any of those, the mental model transfers. A key difference: dotkeeper's
authoritative state is distributed across the repos themselves (via
`dotkeeper.toml`), not pulled from a single remote URL.

See [ADR 0003](adr/0003-reconciler-loop.md) for the full rationale, consequences,
and alternatives considered.

---

## Two kinds of state, two locations

Not all state is the same. Some of it is declarative — you author it, you
own it, you might even generate it with Nix. Some of it is tool-owned — the
daemon writes it as it runs, and you should not touch it. Keeping both in the
same file creates races and conflicts between the human editor and the tool.
v0.5 separates them.

### `machine.toml` — declarative, human- or Nix-authorable

Location: `$XDG_CONFIG_HOME/dotkeeper/machine.toml`
(usually `~/.config/dotkeeper/machine.toml`)

This file contains everything that describes this machine's identity and
preferences. It has no secrets. On Nix machines, home-manager generates it;
on non-Nix machines, `dotkeeper init` writes an initial draft that you edit
with `$EDITOR`.

```toml
name = "elitebook"
slot = 1

default_commit_policy    = "on-idle"
default_git_interval     = "daily"
default_slot_offset_minutes = 5
default_share_with       = ["desktop", "corbet-server"]

[discovery]
scan_roots = [
  "~/Documents/GitHub",
  "~/.config/nvim",
  "~/.agent",
]
exclude = [
  "~/Documents/GitHub/some-third-party-fork",
]
scan_interval = "5m"
```

The `[discovery]` section is covered in depth below. Everything else here is
a default that repos can override in their own `dotkeeper.toml`.

### `state.toml` — tool-owned, contains secrets

Location: `$XDG_STATE_HOME/dotkeeper/state.toml`
(usually `~/.local/state/dotkeeper/state.toml`)

This file is owned exclusively by dotkeeper. You should not hand-edit it. It
contains:

- This machine's Syncthing private key and derived device ID
- The peer directory — a map of `{name, device_id, learned_at}` tuples, built
  up as pairings happen at runtime
- `tracked_overrides` — paths outside scan roots registered via
  `dotkeeper track`
- Cached observed state per tracked repo: last-reconciled commit, last-pushed
  commit, last-backup timestamp
- Cached peer state: last-seen timestamps, connection attempts

The XDG split keeps the two files in different directories for a reason. XDG
`$XDG_CONFIG_HOME` is for configuration you author; `$XDG_STATE_HOME` is for
state the application manages. Tools that conflate these two force awkward
merge logic when the app needs to write something without clobbering a user
edit.

The Syncthing private key lives in `state.toml` (not `machine.toml`) because
files in the Nix store are world-readable. Anything home-manager touches could
land in `/nix/store`; a private key there would be a serious leak.

**Backup story:** back up `machine.toml` — it's declarative and easy to
re-author. Do not back up `state.toml` separately; keys can be regenerated
(at the cost of re-pairing), and caches rebuild automatically. If you want
persistent Syncthing identity across machine rebuilds, copy the private key
manually or use sops-nix.

See [ADR 0002](adr/0002-machine-state-split.md) for full rationale and
alternatives considered.

---

## Per-repo config: `dotkeeper.toml`

Every repo that wants dotkeeper management carries a `dotkeeper.toml` at its
root. The presence of the file is the opt-in signal. Its absence means
dotkeeper ignores the repo, regardless of where it lives on disk.

Because `dotkeeper.toml` is tracked in git and lives inside the Syncthing-
managed folder, it propagates to every peer automatically — no separate
configuration step on each machine.

### Schema

```toml
[repo]
syncthing_folder_id = "dotkeeper-abc123"   # stable; generated on first track
share_with          = ["desktop", "elitebook", "corbet-server"]
                      # omit = "all known peers"; explicit list = subset

[sync]
commit_policy      = "on-idle"   # manual | on-idle | timer
git_interval       = "daily"     # inherits machine default if omitted
slot_opt_out       = false       # true = skip git backup on this repo

[ignore]
# patterns appended to machine-level defaults in .stignore
extra = ["*.generated.go", "testdata/fixtures/"]
```

Most of the interesting fields have machine-level defaults in `machine.toml`
and are optional here. A minimal `dotkeeper.toml` that says "manage this repo
with all defaults" looks like:

```toml
[repo]
syncthing_folder_id = "my-project-a1b2c3"
```

### What this file is not

Per-repo `dotkeeper.toml` does not hold machine identity, the peer directory,
Syncthing keys, or anything else that is machine-local or secret. That all
stays in `machine.toml` and `state.toml`. The split is intentional: the repo
file describes the repo, the machine files describe the machine.

In v0.4 the central `config.toml` held per-repo settings, machine identity,
and mesh topology together. Two machines would both try to write that file
via Syncthing, producing races. Per-repo config in the repo sidesteps the
problem entirely: the file only changes when the repo's management settings
change, and the git history records who changed it and when.

See [ADR 0001](adr/0001-per-repo-config.md) for the full rationale.

---

## Discovery

dotkeeper needs to find `dotkeeper.toml` files. Three sources feed the
tracked set:

### 1. Scan-root walk (primary)

`machine.toml` declares a list of scan roots. On startup and on each
`scan_interval` tick (default 5 min), dotkeeper walks each root up to a
configurable depth (default 3) and looks for `<dir>/dotkeeper.toml`. Every
directory found that way, where this machine qualifies (name is in the
`share_with` list, or the list is absent), becomes a tracked repo.

This is the common case. Most users work out of one or two project
directories. Declaring those as scan roots means any repo with a
`dotkeeper.toml` under them is automatically managed — no `dotkeeper add`
per repo per machine.

Two opt-ins are required:

1. The directory is under a declared scan root (user's choice).
2. The repo has a committed `dotkeeper.toml` (repo owner's choice).

A random clone — a third-party library, a read-only mirror — has no
`dotkeeper.toml`, so dotkeeper leaves it alone. This prevents the "my entire
`node_modules` got synced" class of footgun.

### 2. Syncthing-delivered folders

When a paired peer shares a Syncthing folder with this device, dotkeeper checks
the folder root for a `dotkeeper.toml` on acceptance. If present and this
machine qualifies, the repo is tracked without requiring it to be under a scan
root. This is how a new machine coming into the mesh picks up repos it has
never seen before.

### 3. Explicit `dotkeeper track <path>`

For repos that live outside any scan root. The path is written to
`state.toml`'s `tracked_overrides` list. Subsequent reconciles treat it as
tracked regardless of scan-root membership. `dotkeeper untrack <path>` removes
the override.

This covers edge cases: a repo in `/opt/`, a monorepo root at an unusual
depth, or a directory the user doesn't want in a scan root for other reasons.

All three sources converge on the same check: "this repo has a `dotkeeper.toml`
and this machine should manage it." The entry point only determines how the
path entered consideration.

### Why not walk `$HOME`?

Walking `$HOME` is expensive and imprecise. It picks up `~/.cache`,
`~/Downloads`, backup directories, and anything else that happens to contain
a git repository. Scan roots give you the same "just declare it once" ergonomic
without the scope creep.

See [ADR 0004](adr/0004-scan-root-discovery.md) for alternatives considered.

---

## Commands

v0.5 shrinks the CLI surface considerably. The imperative mutation commands
(`add`, `remove`, `join`, `pair`, `install-timer`, `stop`, `sync`) go away.
Their jobs become either "edit a file and commit" or "what the reconciler does
continuously."

What remains:

| Command | Purpose | Replaces (v0.4) |
|---------|---------|-----------------|
| `dotkeeper init` | First-run setup: generate Syncthing identity, write initial `state.toml`, scaffold `machine.toml` | `dotkeeper init` |
| `dotkeeper start` | Run the daemon (invoked by systemd) | `dotkeeper start` |
| `dotkeeper reconcile [<path>]` | Force a reconcile pass now; optional path limits scope | `dotkeeper pair` + `dotkeeper sync` |
| `dotkeeper status` | Snapshot: last reconcile time, tracked repos, mesh peers, pending work | `dotkeeper status` |
| `dotkeeper identity` | Print this machine's Syncthing device ID | (new) |
| `dotkeeper track <path>` | Register a repo outside scan roots | `dotkeeper add` (for out-of-root paths) |
| `dotkeeper untrack <path>` | Deregister a tracked override | `dotkeeper remove` |
| `dotkeeper doctor` | Run self-diagnostic health checks | `dotkeeper doctor` (added in v0.4.x) |
| `dotkeeper logs` | Tail the journal for the dotkeeper systemd unit | (convenience wrapper) |

Adding a repo to the mesh in v0.5: drop a `dotkeeper.toml` into the repo,
commit it, push. The reconciler finds it on the next scan or inotify event.
No `dotkeeper add`. Removing: delete the file, commit, push. No `dotkeeper remove`.

---

## How a change flows through the system

### "I edit `machine.toml`"

1. You save `machine.toml`.
2. inotify fires. The daemon wakes within milliseconds.
3. `read_config()` re-reads `machine.toml` and all tracked `dotkeeper.toml`
   files.
4. `query_state()` queries Syncthing REST API + git to build the current
   observed state.
5. `diff(desired, observed)` computes what needs to change. If you added a new
   scan root, actions include "walk new root, register any `dotkeeper.toml`
   found." If you changed the default git interval, actions include "update
   the backup schedule for all repos that don't override it."
6. `apply()` executes each action. Each is idempotent.
7. `record_observation()` writes updated cache to `state.toml`.

If you make the same edit twice, the second reconcile finds no diff and
produces no actions. Nothing breaks.

### "I drop a `dotkeeper.toml` into a new repo"

```
# In ~/Documents/GitHub/new-project:
cat > dotkeeper.toml << 'EOF'
[repo]
syncthing_folder_id = "new-project-x9y8z7"
EOF
git add dotkeeper.toml && git commit -m "chore: opt into dotkeeper"
```

Two paths bring this to the daemon's attention:

**inotify path (if the repo is already tracked or under a watched scan root):**
The file creation triggers an inotify event. The daemon reads the new file,
verifies this machine qualifies, adds the repo to the tracked set, and issues
the apply actions to register the Syncthing folder and set up git backup.

**Scan path (otherwise):**
On the next `scan_interval` tick, the daemon walks scan roots, finds the new
`dotkeeper.toml`, and proceeds as above.

Once tracked, the daemon sets inotify watches on this repo's `dotkeeper.toml`
and `.git/refs/` so future changes to either propagate immediately.

On peer machines: when the git push lands on the remote and Syncthing delivers
the file tree, those machines' daemons find the `dotkeeper.toml` in the
Syncthing-delivered folder, check that their machine name qualifies, and begin
managing the repo — no manual step needed on the remote side.

---

## What you don't worry about

**Write races on shared config.** In v0.4, multiple machines could race to
write `config.toml` via Syncthing. In v0.5, per-repo config files live in
their repos; they propagate through git (which is designed for concurrent
editing), not as bare files. `machine.toml` and `state.toml` are local to
each machine and are never synced.

**State drift between machines.** Each machine reconciles its own state against
its own files. There is no shared "current state" that gets stale. If machine A
is offline for a week, machine B keeps reconciling against its local files.
When A comes back, Syncthing delivers the accumulated file changes, and A's
reconciler applies them.

**Re-declaring repos on each new machine.** Drop `dotkeeper.toml` into a repo
once, push it. Every machine with that scan root picks it up. New machines
joining the mesh pick it up via Syncthing delivery. No per-machine
`dotkeeper add` ceremony.

**Reconciler safety.** Because `apply()` is idempotent and `diff()` is
stateless, a crashed daemon, a rebooted machine, or a network partition during
apply does not corrupt anything. The next reconcile recomputes from scratch and
finishes what was interrupted.

**Backup strategy.** `machine.toml` is declarative and easy to re-author or
regenerate from a Nix flake. `state.toml` is ephemeral — lose it, run
`dotkeeper init` on the recovered machine, and re-pair. Per-repo
`dotkeeper.toml` is in git, backed up with the repo.

---

## Where this lives in the code

For contributors navigating the codebase:

| Package | Role |
|---------|------|
| `internal/config/` | Reading and validating `machine.toml`, `state.toml`, and per-repo `dotkeeper.toml`. The types that feed `read_config()`. |
| `internal/reconcile/` | `diff()` pure function, action types, `Apply` stub, and the `Reconciler` that glues them. The unit-testable core. |
| `internal/gitsync/` | Git backup logic — commit, push, staggered slot scheduling. Becomes an apply primitive in v0.5. |
| `internal/stengine/` | Embedded Syncthing lifecycle — start, stop, health. |
| `internal/stclient/` | HTTP client for the Syncthing REST API. Used by `query_state()` to build observed state. |
| `internal/conflict/` | Syncthing conflict-file detection and auto-resolution (inotify watcher, dedup, `git merge-file`). |
| `internal/service/` | Daemon entry point, inotify watcher setup, reconcile loop scheduling. |
| `internal/doctor/` | Health checks for `dotkeeper doctor`. |

To add a new reconcilable property: add it to the appropriate config struct in
`internal/config/`, add an action type in `internal/reconcile/`, implement
the action in the relevant apply package, and wire the diff logic.

The `internal/reconcile/` package has skeleton types, `Diff`, a `StubApplier`,
and a `Reconciler`, but is not yet wired into `cmd/dotkeeper/main.go`. That
wiring — plus refactoring `internal/gitsync/`, `internal/stengine/`, and
`internal/service/` from v0.4's imperative model into apply primitives the
reconciler calls — is the remaining v0.5 work.

---

## Links

- [ADR 0001](adr/0001-per-repo-config.md) — Per-repo `dotkeeper.toml` as authoritative config
- [ADR 0002](adr/0002-machine-state-split.md) — Machine-local state split: declarative vs tool-owned
- [ADR 0003](adr/0003-reconciler-loop.md) — Reconciler model: pure function over observed+desired
- [ADR 0004](adr/0004-scan-root-discovery.md) — Scan-root–based repo discovery
- [README](../README.md) — Quick start, install, commands (current v0.4 surface)
- Migration guide — to be added when v0.5 ships
