# ADR 0006 — Syncthing v2 migration

**Status:** Accepted
**Date:** 2026-05-16

## Context

dotkeeper embeds Syncthing as a library and drives it through
`internal/stengine`. From v0.1 through v0.7 the embedded version was
Syncthing v1.30.0.

Two unrelated pressures pushed v1.30 toward end-of-line for us:

1. **quic-go incompatibility.** The dependabot group `go-security`
   (PR #17) wanted to bump `github.com/quic-go/quic-go` from v0.52.0
   to v0.57.0 to clear CVE-2025-64702 (HTTP/3 header parsing) and a
   handful of related advisories. quic-go v0.56 removed the
   `quic-go/logging` package; Syncthing v1.30's
   `lib/connections/quic_misc.go` still imported it. The PR could not
   be merged without breaking the build. `SECURITY.md` documented the
   blockers GO-2025-4017 (called) and GO-2025-4233 (module-only).

2. **Stale upstream surface.** Syncthing's `lib/logger` was scheduled
   for removal; the v2 line dropped it. Continuing on v1.30 meant
   staying on a code base that no longer received bug fixes other
   than backports.

Syncthing v2.0 (released earlier this year) and v2.1.0 (2026-05-12)
addressed both pressures. v2.1 ships with quic-go v0.59.0 and a
modern slog-based logging story.

## Decision

dotkeeper v0.8.0 embeds Syncthing v2.1.0, replacing the v1.30 line.

### Module pin

Syncthing v2.x is, technically, a Go semantic-import-versioning (SIV)
violation: the v2.1.0 tag's `go.mod` still declares
`module github.com/syncthing/syncthing`, with no `/v2` suffix. The Go
proxy therefore refuses `v2.1.0+incompatible`, on the grounds that a
module with a `go.mod` cannot use the `+incompatible` escape hatch.

dotkeeper works around this by pinning the v2.1.0 commit through a
pseudo-version:

```
github.com/syncthing/syncthing v1.30.0-rc.1.0.20260512055947-c0c401efebf2
```

The version string is cosmetically v1.30.x — the SHA `c0c401efebf2` is
the v2.1.0 tag's commit. The pulled code is identical to a tag-based
v2.1.0 checkout, including quic-go v0.59.0. If upstream ever republishes
under `github.com/syncthing/syncthing/v2`, dotkeeper migrates by a
straight import-path rewrite.

### API surface adjustments

A single dotkeeper file imports Syncthing (`internal/stengine/engine.go`).
The v2 changes that bit us:

| v1.30 call                                              | v2.1 replacement                          |
| ------------------------------------------------------- | ----------------------------------------- |
| `import "lib/logger"` + `logger.DefaultLogger`          | removed (Syncthing uses `log/slog`)       |
| `svcutil.SpecWithDebugLogger(logger.DefaultLogger)`     | `svcutil.SpecWithDebugLogger()` (no arg)  |
| `LoadConfigAtStartup(path, cert, ev, b1, b2, b3)`       | `LoadConfigAtStartup(path, cert, ev, b1, b2)` — third bool dropped |
| `OpenDBBackend(dbFile, cfgWrapper.Options().DatabaseTuning)` | `TryMigrateDatabase(ctx, retention)` + `OpenDatabase(dbFile, retention)` |

`OpenDatabase` is the only structurally meaningful change. The old
`OpenDBBackend` returned a LevelDB-backed `db.Lowlevel`; in v2 it
returns an SQLite-backed `db.DB`. We pass `retention = 0` to disable
auto-pruning of deleted-item history — Syncthing v2's default is
~15 months, but dotkeeper's typical 1–3 device fleet and small dotfile
trees do not benefit from pruning, and silent expiry of delete records
is a class of surprise we explicitly avoid.

`TryMigrateDatabase` is a no-op when no legacy LevelDB directory exists
(fresh installs) and a one-shot LevelDB → SQLite copy on upgrades from
v0.7 or earlier. It runs before `OpenDatabase` so the first launch on
v0.8 is the only one that pays the migration cost.

### SQLite driver

Syncthing v2 ships two SQLite drivers gated by build tags:

- `mattn/go-sqlite3` for CGO builds
- `modernc.org/sqlite` (pure Go) for `CGO_ENABLED=0`

dotkeeper builds with `CGO_ENABLED=0` in CI and in the release pipeline
(see `Makefile`, `nfpm.yml`, the cross-compile sanity step in
`.github/workflows/ci.yml`). The pure-Go driver is the one we ship; the
CGO variant is downloaded into `go.sum` only because Go's module graph
requires checksums for unconditional dependencies, and the file is never
compiled into the binary.

### What does *not* change

- The `config.xml` layout dotkeeper writes is the v1-era format. Syncthing
  v2 auto-archives it to `config.xml.v<n>` and upgrades in place on first
  load — no manual user action.
- QUIC remains disabled by default. The historical rationale (CVE
  mitigation + quic-go v0.52.0 startup panic) is no longer load-bearing,
  but flipping QUIC on has firewall and observability implications and is
  out of scope for v0.8. A future ADR may revisit.
- The `--noassets` build tag is still passed. Syncthing's web GUI assets
  remain stripped; dotkeeper drives the embedded instance through the
  REST API only.
- The systemd / launchd / Windows service contracts are unchanged.

## Consequences

**Positive**

- Two long-standing `govulncheck` advisories (GO-2025-4017,
  GO-2025-4233) clear. dotkeeper's `SECURITY.md` is no longer carrying
  an "unresolved upstream" disclosure.
- We are back on an actively maintained Syncthing line. Future
  Syncthing fixes (sync bugs, BEP improvements) reach dotkeeper through
  normal dependency updates.
- The embedded slog story unifies dotkeeper's own logging future plans
  with Syncthing's — both speak `log/slog`.

**Negative / costs**

- First launch on v0.8 runs a one-shot LevelDB → SQLite database
  migration. For dotkeeper's typical small dotfile trees this is a
  sub-second operation, but it is not free.
- The pseudo-version pin is awkward to read. Anyone scanning `go.mod`
  for "what version of Syncthing is this" sees a v1.30.0-rc string.
  This ADR is the authoritative answer.
- Syncthing v2's default delete-retention semantics differ from v1's
  "kept forever". We override to `0` to preserve v1 behaviour, but the
  upstream default would have been a footgun for users with long-lived
  deletes.

**Reversibility**

The migration is one-way. Downgrading to v1.x would require a
re-migration of the SQLite database back to LevelDB, which Syncthing
upstream does not provide. dotkeeper users who hit a regression should
report and stay on v0.8; rolling back to v0.7 will refuse to read the
new database.
