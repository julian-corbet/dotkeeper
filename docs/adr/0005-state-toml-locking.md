# ADR 0005 — Atomic writes and exclusive flock for `state.toml`

**Status:** Accepted
**Date:** 2026-05-14

## Context

`state.toml` is dotkeeper's tool-owned runtime state (ADR 0002). It is
written by every CLI verb that mutates runtime state — `peer add`,
`peer remove`, `track`, `untrack` — and by the reconciler daemon on
every pass that updates observed-repo metadata. Multiple of these can
run concurrently against the same `state.toml`:

- A user runs `dotkeeper track /a & dotkeeper track /b` in one shell
  (two CLI processes against the same file).
- The daemon is running (`dotkeeper start`) and the user runs
  `dotkeeper peer add laptop XYZ`.
- A shell script automating dotkeeper invokes many subcommands in
  parallel.

The original implementation read `state.toml`, mutated the in-memory
`StateV2`, and called `os.WriteFile` to overwrite the file. Two failure
modes followed:

1. **Byte-level interleaving.** `os.WriteFile` is not atomic. If two
   processes wrote at roughly the same moment, the resulting bytes on
   disk were a mash of both — invalid TOML, e.g. `toml: line 8:
   expected '.' or '=', but got '"' instead`. After that, every
   subsequent `dotkeeper` invocation failed at config-load time and
   the user was effectively bricked until they hand-edited or deleted
   the file.
2. **Lost updates.** Even with atomic writes, two processes that each
   read the same prior state, made independent changes, and rewrote
   the file would clobber each other's update — last writer wins.

The race was discovered by `tests/multipeer:TestConcurrentTrackUntrack`
(PR #7), which spawned 20 concurrent `track`/`untrack` pairs and
reliably produced corrupted TOML.

## Decision

Two layered protections in `internal/config`:

### 1. `WriteFileAtomic` for every config-file write

```
WriteFileAtomic(path, data, mode):
    tmp = path + ".<pid>.<rand>.tmp"
    write(tmp, data); fsync(tmp); close(tmp)
    rename(tmp, path)
```

`rename(2)` is atomic on Linux/Unix within a single filesystem (and on
Windows since NTFS journaling), so a reader either sees the previous
file in full or the new file in full — never a half-written
intermediate. `fsync` before rename means a power loss between write
and rename doesn't leave an empty or truncated file (an ext4 with
`data=writeback` foot-gun).

Two-stage cleanup: on any error (write, fsync, close), the temp file
is removed before the function returns, so a crashy environment doesn't
accumulate `.tmp` orphans next to the target.

Temp name ends in `.tmp` (not `.tmp.<...>`) so dotkeeper's default
Syncthing ignore pattern `*.tmp` catches it before Syncthing can
propagate the transient to peers. This matters for in-repo files like
`.dotkeeper.toml` and `.stignore`; for tool-owned files outside any
sync folder it's a no-op.

Applied to: `state.toml`, `machine.toml`, `.dotkeeper.toml`, `.stignore`,
`.git/info/exclude`, and merged conflict files.

### 2. `MutateStateV2` for state.toml read-modify-write

`WriteFileAtomic` alone does not prevent lost updates. For that, the
load-mutate-write cycle must serialise:

```
MutateStateV2(mutate func(*StateV2) error):
    open <state-dir>/state.toml.lock
    flock(LOCK_EX)            # blocks until exclusive
    state = LoadStateV2()     # nil ⇒ zero-value StateV2
    mutate(state)             # caller's closure
    WriteStateV2(state)       # WriteFileAtomic
    # lock released on file close
```

`flock(2)` is process-scoped, blocking, and self-cleaning (the kernel
releases on process exit). Cross-platform via a thin shim:

- `lock_unix.go` (`!windows`): `unix.Flock(LOCK_EX)`
- `lock_windows.go` (`windows`): `windows.LockFileEx(LOCKFILE_EXCLUSIVE_LOCK)`

Both have identical semantics: blocking exclusive acquisition,
released on close. The shim keeps callers oblivious to the platform.

Every callsite that previously did
`LoadStateV2() → mutate → WriteStateV2()` was rewritten as
`MutateStateV2(func(s *StateV2) error { ... })`. There are no
exceptions in the codebase — searching for `WriteStateV2(` outside
the implementation should turn up nothing.

## Alternatives considered

1. **Atomic writes only (no flock).** Solves byte-level corruption
   but not lost updates. Two concurrent `peer add` calls would each
   write a valid file with only their own peer recorded. Rejected.

2. **Single-writer daemon model.** Force all writes through a long-
   lived `dotkeeper start` daemon. Solves the problem cleanly but
   breaks every non-daemon usage (CLI invocations on machines without
   a running daemon, including the install-time `init`). Rejected.

3. **Database (SQLite) instead of TOML.** SQLite gives transactions
   for free. Trades human-readability and Nix-friendliness for
   complexity. Rejected — TOML's whole point in ADR 0002 was that
   `machine.toml` is hand-editable; consistency between formats is
   worth preserving.

4. **`github.com/gofrs/flock` library.** Mature cross-platform flock
   wrapper. Adds a third-party dependency for ~30 lines of shim code.
   Rejected on dependency-budget grounds.

## Consequences

**Positive:**

- Concurrent `dotkeeper` invocations are safe. The test suite's
  `TestConcurrentTrackUntrack` (20-way race) and
  `TestMutateStateV2_{InProcess,CrossProcess}Concurrency` (50-goroutine
  and 20-fork respectively) prove this.
- Crash-safety on every config file: a `SIGKILL` between write and
  rename leaves the previous file intact.
- The locking shim works on Linux, macOS, and Windows; the CI build
  step cross-compiles all three to lock in the contract.

**Negative:**

- `MutateStateV2` is blocking. A long-held lock (only possible from a
  bug in the mutation closure) would stall every other dotkeeper
  invocation against the same machine. Mitigation: keep mutation
  closures short — they should mostly be `state.Foo = bar`-shaped.
- One extra file per machine (`state.toml.lock`). It is created on
  demand and never deleted; lock inheritance across CLI invocations
  requires this. Mode 0o600.

**Neutral:**

- `dotkeeper doctor` now offers actionable recovery hints for the case
  where a user upgrades from a pre-fix dotkeeper with already-corrupt
  `state.toml` (see `internal/doctor/checks.go: ConfigCheck`).

## Tests that lock the contract

- `internal/config/state_concurrency_test.go`:
  - `TestMutateStateV2_InProcessConcurrency` (50 goroutines, in-process flock)
  - `TestMutateStateV2_CrossProcessConcurrency` (20 forked test binaries,
    real flock semantics)
- `internal/config/atomic_test.go`:
  - `TestWriteFileAtomic_AllOrNothing` (tight reader vs alternating writer)
  - `TestWriteFileAtomic_NoOrphanTempOnError` (no `.tmp` leftovers)
  - `TestWriteFileAtomic_OverwriteReadOnlyTarget` (rename ignores target mode)
  - `TestWriteFileAtomic_TempNameMatchesSyncthingIgnore` (regression gate on
    the `.tmp` suffix convention)
- `internal/config/state_corruption_test.go`:
  - `TestLoadStateV2_CorruptFile` (clear error on bad TOML)
  - `TestMutateStateV2_RefusesToOverwriteCorruptState` (never silently
    overwrite corruption)
- `tests/multipeer/adversarial_test.go: TestConcurrentTrackUntrack`
  (the original adversarial reproducer; now the high-level regression gate)

Promoting `TestConcurrentTrackUntrack` from `t.Logf` (PR #7's
"ADVERSARIAL FINDING:") to `t.Fatalf` was deliberate: the bug is now
gone and the test should fail loudly if it ever comes back.

## Related

- ADR 0002 — Machine-local state split (defines `state.toml`'s role).
- PR #7 — multipeer e2e suite that surfaced the bug.
- PR #8 — atomic writes for all config files, `-race` in CI.
- PR #13 — cross-platform locking primitive.
