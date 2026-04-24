# ADR 0002 — Machine-local state split: declarative vs tool-owned

**Status:** Accepted
**Date:** 2026-04-24

## Context

ADR 0001 moves per-repo config out of a central file and into each repo's
own `dotkeeper.toml`. What's left — machine identity, mesh peer directory,
defaults, cached state — still needs a home. That residue is not uniform in
how it should be written or read:

- **Name and slot** want to be declaratively set, possibly nix-generated.
- **The Syncthing private key** is a secret; it must never appear in a
  world-readable nix store path or in any file a human is likely to commit.
- **The peer directory** (name→device-ID map) grows at runtime as pairings
  happen — not declarable in advance.
- **Default commit policy and default git interval** are nice to set
  declaratively; they fall through to repos that don't override.
- **Cached observed state** (last-reconciled commit per repo, last-seen
  peer timestamps) is pure runtime cache.

Lumping these into one file blurs the line between "author me" and "don't
touch me."

## Decision

Two files, following the XDG Base Directory specification:

### `$XDG_CONFIG_HOME/dotkeeper/machine.toml` — declarative, non-secret

Human- or Nix-authorable. Contains:

- `name` — this machine's name in the mesh (`elitebook`, `desktop`, ...)
- `slot` — staggered git-backup slot (0-based, unique per machine)
- `default_commit_policy` / `default_git_interval` / `default_slot_offset_minutes` — inherited by repos that don't override
- `default_share_with` — list of device names to share newly-discovered repos with by default
- `[discovery]` — scan roots and exclusions (see ADR 0004)

Never contains secrets. On Nix machines, home-manager generates this file.
On non-Nix machines, `dotkeeper init` writes an initial draft; humans edit
via `$EDITOR`.

### `$XDG_STATE_HOME/dotkeeper/state.toml` — tool-owned, contains secrets

Owned exclusively by dotkeeper. Contains:

- This machine's Syncthing private key (or a reference to it)
- This machine's Syncthing device ID (derived public identity)
- Peer directory — map of known `{name, device_id, learned_at}` tuples, populated by `dotkeeper pair`
- `tracked_overrides` — paths outside scan roots that have been explicitly registered via `dotkeeper track`
- Cached observed state per tracked repo: last-reconciled commit, last-pushed commit, last-backup timestamp
- Cached peer state: last-seen timestamps, connection attempts

Never hand-edited. Created on `dotkeeper init`, mutated by the tool as it
runs. Not backed up separately; a lost `state.toml` is recoverable by
re-running `dotkeeper init` (new Syncthing identity — requires re-pairing)
and re-tracking repos (trivial if they're under scan roots).

## Rationale

**XDG separates config from state for a reason.** Config is what you author;
state is what the app does with it. Tools that conflate them force the app
to be careful about not clobbering user edits, which creates locking or
marker-based-merge ugliness.

**Secrets cannot live in a Nix-generated file.** Files in `/nix/store` are
world-readable (by design — the store is a cache). Anything the home-manager
module touches could end up there. So the Syncthing private key must live in
a file the tool creates at runtime, not one the nix activation writes.

**The peer directory is discovered, not declared.** You learn a peer's
device ID when you pair with them (or they pair with you). It can't be in
a declared config because the information doesn't exist until the action
happens.

**Cached state must be local.** Observed-commit hashes, last-seen
timestamps, etc. are per-machine and shouldn't propagate. They belong in
the state dir.

## Consequences

**Nix's role is sharply bounded.** Home-manager writes `machine.toml`. That
file has no secrets, no mutable tool state. Everything else is
tool-territory.

**Bootstrap sequence on a new machine:** `dotkeeper init` creates
`state.toml` (generates Syncthing identity), then `dotkeeper identity`
prints the device ID so you can add it to the peer list on another
machine via the usual "pair" pattern. `machine.toml` either exists already
(Nix wrote it via home-manager) or gets populated interactively.

**CLI commands that formerly mutated `config.toml`** (`dotkeeper pair`,
etc.) now mutate `state.toml` and are legitimate — these are runtime
identity actions, not configuration edits.

**Backup story is simple:** back up `machine.toml` (trivial; it's
declarative — Nix-derived or easily re-authored). Don't back up
`state.toml` (keys can be regenerated; caches rebuild). If you need
persistent identity across machine rebuilds, copy the Syncthing private
key manually or use sops-nix to encrypt it into the flake.

## Alternatives considered

**Single file with sections.** Rejected: mixes authorable and tool-owned
content, invites CLI-vs-editor races, complicates nix integration.

**Keep everything in `$HOME/.local/share/dotkeeper`.** Rejected: conflates
config and state; XDG spec exists precisely to prevent this.

**Derive Syncthing identity from SSH host key** (the pattern sops-nix uses
via `ssh-to-age`). Attractive — would let the identity live in nix — but
Syncthing wants its own key format and we'd need a conversion layer. Punt
to a follow-up ADR if someone wants it.

## See also

- [ADR 0001](0001-per-repo-config.md) — why per-repo config is authoritative
- [ADR 0003](0003-reconciler-loop.md) — how state is consumed
- [ADR 0004](0004-scan-root-discovery.md) — where `[discovery]` in `machine.toml` is used
