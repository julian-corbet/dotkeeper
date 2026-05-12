# ADR 0004 — Scan-root–based repo discovery

**Status:** Accepted, revised for v0.6
**Date:** 2026-04-24

## Context

ADR 0001 makes each managed local repo copy self-describing via a
`.dotkeeper.toml` file at its root. That's the local opt-in marker. But how
does dotkeeper find those files? Three shapes of discovery are plausible:

1. **Walk the entire home directory** looking for `.dotkeeper.toml`. Safe
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

dotkeeper's `machine.toml` (see ADR 0002) declares a list of scan roots:

```toml
[discovery]
scan_roots = [
  "~/Documents/GitHub",
  "~/Projects",
]
exclude = [
  "~/Documents/GitHub/some-third-party-fork",
]
scan_interval = "5m"
```

On startup and on each `scan_interval` tick, dotkeeper walks each scan
root (bounded depth — say 3, configurable) looking for
`<any-dir>/.dotkeeper.toml`. For each found, it reads the file, checks
resolves its `share_with` peer names through the declarative/imperative peer
roster, and tracks the repo when it applies to this machine. Empty
`share_with` inherits `machine.toml` defaults; empty defaults mean all known
peers. Unchanged compared to the previous pass: no action.

Changes trigger reconcile for the affected repo. Removal of local
`.dotkeeper.toml` means the repo no longer declares desired dotkeeper state on
that machine.

### Other discovery entry points

The scan is the primary mechanism. Two other sources feed the tracked set:

1. **Existing Syncthing folders.** Syncthing folders are observed so dotkeeper
   can detect drift. They become desired dotkeeper repos only when local
   `.dotkeeper.toml` also exists or the path is explicitly tracked.
2. **Explicit `dotkeeper track <path>`.** For repos outside any scan
   root. Creates/fills local `.dotkeeper.toml` when needed, writes local
   Git/Syncthing excludes, then writes the path to `state.toml`'s
   `tracked_overrides` list. Subsequent reconciles treat it as tracked
   regardless of scan-root membership. `dotkeeper untrack <path>` removes
   the override.

All three sources converge on the same check: "this repo has a
`.dotkeeper.toml` and this machine should manage it." The entry point just
determines how the path entered consideration.

## Rationale

**Bounded cost.** Scan roots cap the walk. With a handful of root
directories, the initial and periodic scan is cheap even on a laptop.

**Explicit consent at two levels.** (1) A directory is scanned because the
user declared it a scan root. (2) A repo under a scan root is managed because a
local `.dotkeeper.toml` exists. Two opt-ins, each explicit, prevent the "my
entire home got synced" footgun.

**Zero ceremony for the common case.** The common case is "I work out of
`~/Documents/GitHub/`, and I want every repo there with local
`.dotkeeper.toml` to be managed." That's one scan root entry and a file in each
repo. Nix/Home Manager can generate those files; `dotkeeper track` can create
them manually.

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
filesystems, `.dotkeeper.toml` is the canonical name; variants are not
matched.

**Performance of the inotify watcher graph scales with tracked-repo
count, not with scan-root contents.** Inotify watches are set per tracked
repo (on its `.dotkeeper.toml` and `.git/`), not on every file under scan
roots. The scan is the expensive part, and it runs at `scan_interval`,
not continuously.

## Alternatives considered

**Walk `$HOME`.** Rejected: too expensive, picks up unwanted directories,
no consent at the root level.

**Require `dotkeeper track` for every repo.** Rejected as the only path:
linear friction per repo per machine. It remains the portable manual path,
while declarative generation can materialize local files.

**Implicit enrollment on any `.git` directory** under scan roots.
Rejected: removes the per-repo opt-in; vendored dependencies and
read-only checkouts would get enrolled. The `.dotkeeper.toml` marker is
the consent step.

**Recursive-sync from peers** (a peer announces a folder, dotkeeper
auto-accepts and enrolls under scan root). Rejected as a source of desired
repo config because `.dotkeeper.toml` is intentionally not synced.

## See also

- [ADR 0001](0001-per-repo-config.md) — the `.dotkeeper.toml` marker
- [ADR 0002](0002-machine-state-split.md) — where `scan_roots` is declared
- [ADR 0003](0003-reconciler-loop.md) — what reconcile does once a repo is tracked
