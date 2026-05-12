# ADR 0001 — Local per-repo `.dotkeeper.toml` as authoritative config

**Status:** Accepted, revised for v0.6
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

Every local repo copy that wants dotkeeper management carries a
`.dotkeeper.toml` file at its root. That file is the authoritative source for
how dotkeeper handles that specific local copy. The presence of the file is the
opt-in signal; its absence means dotkeeper ignores the repo.

`.dotkeeper.toml` is local machine state. It must not be committed to Git or
synced by Syncthing. dotkeeper enforces this by writing `.git/info/exclude`
entries and a managed `.stignore`.

The per-repo `.dotkeeper.toml` holds:

- Syncthing folder ID and per-repo ignore patterns
- Which mesh devices should receive this repo (by name, not device ID)
- Per-repo commit policy (manual / on-idle / timer)
- Any per-repo schedule overrides (git interval, push slot opt-outs)
- Anything else specific to this repo

Machine-level and mesh-level state does NOT live in this file. That split is
defined in ADR 0002.

## Rationale

**Data lives with the local copy it describes.** A repo's local sync rules
belong inside that repo copy, not in a distant central registry. When a repo is
deleted locally, its dotkeeper config disappears with it.

**Private topology stays private.** Public repos must not expose personal
machine names, mesh topology, Syncthing policy, or dotkeeper local markers.
Keeping `.dotkeeper.toml` untracked and unsynced preserves that boundary.

**Nix can materialize repeatable local state.** Users who want reproducible
setup can generate `.dotkeeper.toml` on each machine from private flake data.
Users without Nix can run `dotkeeper track <path>`.

**Opt-in is explicit.** A random clone (a third-party library, a scratch
checkout) is a git repo but has no `.dotkeeper.toml` — dotkeeper ignores it.
Opt-in is a deliberate local file, not an environment-dependent accident.

**Removes write races on synced config.** Per-repo dotkeeper config no longer
crosses the Syncthing mesh as a bare file, so machines do not race on local
metadata.

## Consequences

**CLI shrinks substantially.** `dotkeeper add`, `dotkeeper remove`,
`dotkeeper join`, `dotkeeper pair` as state-mutators go away. Adding a repo to
the mesh becomes: create local `.dotkeeper.toml` with `dotkeeper track` or
generate it with Nix/Home Manager. Removing: delete the local file or run
`dotkeeper untrack`.

**Each machine needs local materialization.** Since `.dotkeeper.toml` is not
synced, a new machine must either generate it declaratively or run
`dotkeeper track` for the repo paths it should manage.

**Per-repo schema needs a complete design.** Making `.dotkeeper.toml`
authoritative requires formalizing the schema, documenting defaults, and
versioning.

**Cross-repo settings (global ignore patterns, default intervals)** move to
machine-local config (ADR 0002), inherited when a repo doesn't override.

## Alternatives considered

**Keep the central `config.toml`.** Rejected: conflates machine identity,
mesh topology, and per-repo rules into one mutable file that everything
fights over.

**Committed per-repo config.** Rejected: it leaks private topology into public
repos and gives Syncthing another local control file to distribute.

**Drop-in fragments** (`~/.config/dotkeeper/config.d/*.toml`, one per repo,
written by CLI or Nix). Rejected: repos are no longer self-contained because
their local policy lives outside the local repo copy.

**A dedicated config git repo** that every machine clones (pattern used
by ArgoCD, Flux). Rejected for dotkeeper's personal-scale use case: overkill
for the common path, though private Nix flakes can still generate local files
for users who want declarative topology.

## See also

- [ADR 0002](0002-machine-state-split.md) — what NOT to put in the per-repo file
- [ADR 0003](0003-reconciler-loop.md) — how the tool reacts to the file
- [ADR 0004](0004-scan-root-discovery.md) — how the tool finds the file
