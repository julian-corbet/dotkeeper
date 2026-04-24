# ADR 0001 — Per-repo `dotkeeper.toml` as authoritative config

**Status:** Accepted
**Date:** 2026-04-24

## Context

Today dotkeeper reads a single `~/.config/dotkeeper/config.toml` that holds
settings for every managed repo plus machine identity plus mesh topology.
The file is mutable by `dotkeeper add`, `dotkeeper remove`, `dotkeeper join`,
and `dotkeeper pair`. When that file propagates across the mesh via
Syncthing, several things happen at once: machine-identity bits mix with
mesh-topology bits mix with per-repo settings. A change on one machine races
with another machine's change. The file is authoritative for dotkeeper's
behavior, but dotkeeper itself is an author of it — so there's no external
source of truth a third party (Nix, a human editing with `$EDITOR`,
another machine's reconciliation) can own without fighting the CLI.

## Decision

Every repo that wants dotkeeper management carries a `dotkeeper.toml` file
at its root. That file is the authoritative source for how dotkeeper handles
that specific repo. The presence of the file is the opt-in signal; its
absence means dotkeeper ignores the repo.

The per-repo `dotkeeper.toml` holds:

- Syncthing folder ID and per-repo ignore patterns
- Which mesh devices should receive this repo (by name, not device ID)
- Per-repo commit policy (manual / on-idle / timer)
- Any per-repo schedule overrides (git interval, push slot opt-outs)
- Anything else specific to this repo

Machine-level and mesh-level state does NOT live in this file. That split is
defined in ADR 0002.

## Rationale

**Data lives with the thing it describes.** A repo's sync rules belong with
the repo. When the repo is cloned to a new machine, its configuration
travels with it — no need to manually re-declare it anywhere. When a repo
is deleted, its config disappears with it.

**Syncthing already carries the file.** Because `dotkeeper.toml` is tracked
in git and sits inside the managed folder, it propagates across peers
without a separate mechanism.

**Opt-in is explicit.** A random clone (a third-party library, a scratch
checkout) is a git repo but has no `dotkeeper.toml` — dotkeeper ignores it.
Opt-in is a deliberate commit, not an environment-dependent accident.

**Removes write races on a central file.** Per-repo config files are
modified by whoever owns the repo's content. Two machines aren't both
trying to write the same configuration file.

## Consequences

**CLI shrinks substantially.** `dotkeeper add`, `dotkeeper remove`,
`dotkeeper join`, `dotkeeper pair` as state-mutators go away. Adding a repo
to the mesh becomes: drop a `dotkeeper.toml` into it, commit. Removing:
delete the file, commit.

**A new `dotkeeper track <path>` is needed** for repos outside the declared
scan roots (see ADR 0004) — not to mutate per-repo state, but to register
the path in this machine's local state. For most repos, placing them under
a scan root makes `track` unnecessary.

**Per-repo schema needs a complete design** — today's `dotkeeper.toml` is
a breadcrumb with minimal content. Making it authoritative requires
formalizing the schema, documenting defaults, and versioning.

**Cross-repo settings (global ignore patterns, default intervals)** move to
machine-local config (ADR 0002), inherited when a repo doesn't override.

## Alternatives considered

**Keep the central `config.toml`.** Rejected: conflates machine identity,
mesh topology, and per-repo rules into one mutable file that everything
fights over.

**Drop-in fragments** (`~/.config/dotkeeper/config.d/*.toml`, one per repo,
written by CLI or Nix). Rejected: keeps the split-brain problem of "who
owns which fragment"; repos aren't self-contained because their config
lives outside them.

**A dedicated config git repo** that every machine clones (pattern used
by ArgoCD, Flux). Rejected for dotkeeper's personal-scale use case:
overkill; the repos themselves already are git repos that sync via
Syncthing, so using them as the config medium is zero new infrastructure.

## See also

- [ADR 0002](0002-machine-state-split.md) — what NOT to put in the per-repo file
- [ADR 0003](0003-reconciler-loop.md) — how the tool reacts to the file
- [ADR 0004](0004-scan-root-discovery.md) — how the tool finds the file
