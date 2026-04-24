# ADR 0003 — Reconciler model: pure function over observed+desired

**Status:** Accepted
**Date:** 2026-04-24

## Context

Today dotkeeper reacts imperatively. Commands like `add`, `remove`, `join`
mutate state and the tool performs matching side effects. This works for
small setups but has three problems as the system grows:

1. **Drift is invisible.** If someone edits a Syncthing folder outside
   dotkeeper, the tool doesn't notice unless something breaks.
2. **Testing is side-effect-heavy.** Each command is its own integration
   test; there's no single "apply config" function to unit-test.
3. **It doesn't match how Nix, systemd units, and other declarative
   tooling work**, so integrating with them is harder than necessary.

The rest of the stack this project lives in — Nix, home-manager,
system-manager, sops-nix, Syncthing itself — all follow a reconciler
pattern: a pure function from `(desired, observed) → actions` that's
idempotent and safe to run repeatedly. Dotkeeper should too.

## Decision

The core of dotkeeper becomes a reconcile loop:

```
reconcile():
    desired  = read_config()     # machine.toml + every tracked dotkeeper.toml
    observed = query_state()     # Syncthing REST API + git + filesystem
    actions  = diff(desired, observed)
    for action in actions:
        apply(action)
    record_observation(observed)
```

`diff()` is a pure function. `apply()` is the only side-effectful part and
is idempotent per action (applying the same action twice is a no-op).

### Triggers

Four ways to invoke reconcile:

1. **inotify watcher** on `machine.toml`, on every tracked repo's
   `dotkeeper.toml`, and on `.git/HEAD` / `.git/refs/` of every tracked
   repo. Changes trigger reconcile in milliseconds.
2. **Periodic timer** (`machine.reconcile_interval`, default 5 min) — a
   safety net in case an inotify event is missed or a remote change arrived
   without local filesystem activity.
3. **Explicit `dotkeeper reconcile [<path>]`** — one-shot apply, blocks
   until done, exits with success/failure codes suitable for scripting.
4. **Local webhook** (future): dotkeeper listens on a local HTTP socket;
   `POST /reconcile` triggers an immediate pass. Useful for git hooks, CI
   integrations, and other agents.

### CLI surface after this ADR lands

Shrinks dramatically. What remains:

- `dotkeeper start` — run the daemon (systemd target)
- `dotkeeper reconcile [<path>]` — force a pass
- `dotkeeper identity` — print this machine's Syncthing device ID
- `dotkeeper track <path>` — register a path outside scan roots
- `dotkeeper untrack <path>` — deregister a tracked override
- `dotkeeper status` — current state snapshot (last reconcile time, tracked repos, mesh peers, pending work)
- `dotkeeper doctor` — health checks
- `dotkeeper logs` — tail the journal for this unit

Removed: `add`, `remove`, `join`, `pair`, `install-timer`, `stop`, `sync`.
Their jobs are now either (a) edit a file and commit, or (b) what the
reconciler does continuously.

## Rationale

**Idempotency is the point.** A reconciler can run every minute, on every
file change, on explicit command — and doesn't break anything by running
twice. That's freedom: safe automation, safe retries, safe debugging.

**Drift detection is free.** Because `diff()` always computes
desired-vs-observed, "someone edited Syncthing outside dotkeeper" is
noticed immediately and corrected (or reported).

**Testability.** `diff()` is a pure function: feed it test data, assert on
the action list. No need to spin up Syncthing for unit tests.

**Matches the rest of the stack.** Nix rebuilds, home-manager switches,
system-manager applies, sops-nix decrypts — all reconcilers. Adding a fifth
reconciler that behaves the same way is cognitively cheap for the user.

## Consequences

**Internal refactor is real.** The Syncthing API calls, git wrappers, and
conflict-resolution code stay. What changes is the *direction of control*
— instead of CLI commands calling the primitives, the reconciler calls
them. New code path: `diff() → apply()`. Existing code becomes the apply
primitives.

**State observation needs to be cheap.** Every reconcile queries Syncthing
REST + git state. For a reconcile every 5 min with dozens of tracked repos,
this is fine. For every second it's not. Mitigations: cache observations
briefly, debounce inotify events.

**Apply failure handling matters.** Network hiccups, git remote down,
Syncthing busy. Each action needs to be retryable. Log the failure, move
on, the next reconcile picks it up. Don't abort the whole pass on one
action's failure.

**The daemon gets a bit more RAM.** State-observation cache, inotify
watchers per tracked repo, local HTTP server for webhooks. Not dramatic —
dotkeeper is Go, memory footprint stays small — but worth naming.

**Mutation CLI's removal is a breaking change.** Users with scripts that
call `dotkeeper add` have to migrate. Version this as v0.5 with a
migration note in the changelog; document the file-based replacement for
every removed command.

## Alternatives considered

**Keep imperative CLI, add a reconcile-on-timer.** Rejected: doesn't solve
the drift-invisible problem; two code paths (mutation vs reconciliation)
double the testing and bug surface.

**Turn dotkeeper into a pull-from-git-URL tool** (like Flux CD — controller
pulls a single manifest from a remote git URL). Rejected: per ADR 0001,
the config is per-repo and distributed across the mesh via Syncthing,
not centralized in one git URL.

**Event-sourced state with an append-only log.** Rejected: overkill for
this use case; the authoritative state is already in git, which is itself
event-sourced. Reinventing it inside the tool adds complexity without
value.

## See also

- [ADR 0001](0001-per-repo-config.md) — what "desired" comes from
- [ADR 0002](0002-machine-state-split.md) — what "observed" reads from
- [ADR 0004](0004-scan-root-discovery.md) — what "tracked" means and where reconcile looks
