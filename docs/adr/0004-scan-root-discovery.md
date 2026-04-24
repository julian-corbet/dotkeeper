# ADR 0004 — Scan-root–based repo discovery

**Status:** Accepted
**Date:** 2026-04-24

## Context

ADR 0001 makes each managed repo self-describing via a `dotkeeper.toml`
file at its root. That's the opt-in marker. But how does dotkeeper find
those files? Three shapes of discovery are plausible:

1. **Walk the entire home directory** looking for `dotkeeper.toml`. Safe
   marker (per ADR 0001 random clones are ignored), but walking `$HOME` is
   expensive and picks up directories the user may not expect
   (`~/.cache`, `~/Downloads`, backup directories).
2. **Explicit registration of every tracked repo** via `dotkeeper track
   <path>`. Predictable, but requires a per-repo gesture per machine per
   repo — friction scales linearly with repo count.
3. **Bounded filesystem scan under declared roots.** Balances convenience
   with safety: dotkeeper only walks directories the user explicitly
   nominated.

(Option 3) is what almost every filesystem-aware tool does: ripgrep only
searches where you tell it to search, `fd` respects `.gitignore` and the
cwd, etc.

## Decision

Dotkeeper's `machine.toml` (see ADR 0002) declares a list of scan roots:

```toml
[discovery]
scan_roots = [
  "~/Documents/GitHub",
  "~/.agent",
  "~/Projects",
]
exclude = [
  "~/Documents/GitHub/some-third-party-fork",
]
scan_interval = "5m"
```

On startup and on each `scan_interval` tick, dotkeeper walks each scan
root (bounded depth — say 3, configurable) looking for
`<any-dir>/dotkeeper.toml`. For each found, it reads the file, checks
whether this machine's name is in its `share_with` list (or the list is
unset, meaning "all peers"), and if so, tracks the repo. Unchanged
compared to the previous pass: no action.

Changes trigger reconcile for the affected repo. Removal of a
`dotkeeper.toml` (git deletion or rename) triggers untracking: dotkeeper
stops watching that repo and, per the configured policy, either keeps
the Syncthing folder in place (safe) or removes it (destructive — opt-in
via config flag).

### Other discovery entry points

The scan is the primary mechanism. Two other sources feed the tracked set:

1. **Syncthing-delivered folders.** When a paired peer shares a folder
   with this device, Syncthing announces it. Dotkeeper checks the folder's
   root for `dotkeeper.toml` on acceptance. If present and this machine
   qualifies, track it.
2. **Explicit `dotkeeper track <path>`.** For repos outside any scan
   root. Writes the path to `state.toml`'s `tracked_overrides` list.
   Subsequent reconciles treat it as tracked regardless of scan-root
   membership. `dotkeeper untrack <path>` removes the override.

All three sources converge on the same check: "this repo has a
`dotkeeper.toml` and this machine should manage it." The entry point just
determines how the path entered consideration.

## Rationale

**Bounded cost.** Scan roots cap the walk. With a handful of root
directories, the initial and periodic scan is cheap even on a laptop.

**Explicit consent at two levels.** (1) A directory is scanned because the
user declared it a scan root. (2) A repo under a scan root is managed
because the repo's owner committed a `dotkeeper.toml` into it. Two
opt-ins, each explicit, prevents the "my entire home got synced"
footgun.

**Zero ceremony for the common case.** The common case is "I work out of
`~/Documents/GitHub/`, and I want every repo there with a `dotkeeper.toml`
to be managed." That's one scan root entry and a file in each repo.
No per-repo `dotkeeper add`.

**Scan roots are declarative.** They live in `machine.toml`, which on Nix
machines is home-manager-generated from the flake's repo list. The flake
already knows where the user's project directories live; declaring them
as scan roots is a short hop.

## Consequences

**The `[discovery]` section becomes a contract between the user (or Nix)
and dotkeeper.** Adding a new project directory means extending
`scan_roots`.

**Depth limits matter for pathological layouts.** If someone has deeply
nested project dirs (10 levels deep), the default depth of 3 won't find
them; they'd bump the depth or use `dotkeeper track` explicitly.

**Symlinks need handling.** Default: follow symlinks up to one level deep
to avoid loops, unless they escape the scan root. This behavior needs
documentation.

**Case-sensitivity.** On case-insensitive filesystems (macOS HFS+,
Windows), the file name match is already fine. On case-sensitive
filesystems, `dotkeeper.toml` is the canonical name; variants are not
matched.

**Performance of the inotify watcher graph scales with tracked-repo
count, not with scan-root contents.** Inotify watches are set per tracked
repo (on its `dotkeeper.toml` and `.git/`), not on every file under scan
roots. The scan is the expensive part, and it runs at `scan_interval`,
not continuously.

## Alternatives considered

**Walk `$HOME`.** Rejected: too expensive, picks up unwanted directories,
no consent at the root level.

**Require `dotkeeper track` for every repo.** Rejected: linear friction
per repo per machine. Works against the "drop a `dotkeeper.toml` and go"
principle from ADR 0001.

**Implicit enrollment on any `.git` directory** under scan roots.
Rejected: removes the per-repo opt-in; vendored dependencies and
read-only checkouts would get enrolled. The `dotkeeper.toml` marker is
the consent step.

**Recursive-sync from peers** (a peer announces a folder, dotkeeper
auto-accepts and enrolls under scan root). Included as a discovery
entry point, but gated: auto-accept only for folders with a
`dotkeeper.toml` naming this machine, and subject to the machine's
auto-accept policy (off by default; opt-in in `machine.toml`).

## See also

- [ADR 0001](0001-per-repo-config.md) — the `dotkeeper.toml` marker
- [ADR 0002](0002-machine-state-split.md) — where `scan_roots` is declared
- [ADR 0003](0003-reconciler-loop.md) — what reconcile does once a repo is tracked
