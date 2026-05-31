# AGENTS.md

Guidance for AI coding agents (and humans) contributing to **dotkeeper**.
dotkeeper is a single Go binary that does real-time peer-to-peer sync of code,
dotfiles, and notes across machines (embedded Syncthing) with a staggered git
auto-backup underneath. This file is the contributor-facing quick reference;
[`CONTRIBUTING.md`](CONTRIBUTING.md) is the authoritative source for anything
not covered here.

## Prerequisites

- **Go 1.26+** (see `go 1.26.3` in [`go.mod`](go.mod); build only).
- **git** on `PATH`.
- For running the daemon: a service manager — systemd (Linux), launchd (macOS),
  or cron (BSD/fallback). Not needed just to build and test.

## Build

Always build through the Makefile, **not** plain `go build ./...`:

```bash
make build       # build ./cmd/dotkeeper with version + commit baked in
make install     # build, then copy to ~/.local/bin/dotkeeper
make build-debug # no ldflag stripping, for delve
```

Why: dotkeeper embeds Syncthing as a library. Syncthing's `lib/api` package
expects generated web-GUI assets that dotkeeper does not ship, so a plain
`go build ./cmd/dotkeeper` fails with `undefined: auto.Assets`. The fix is the
`-tags noassets` build tag, which the Makefile, Dockerfile, and CI set
automatically. If you must invoke Go directly:

```bash
go build -tags noassets ./cmd/dotkeeper
```

If your IDE/linter shows the `auto.Assets` error, configure gopls to pass
`-tags noassets`.

## Test

```bash
make test       # go test -tags noassets ./...
make cover      # tests + coverage.out + coverage.html
```

The suite covers unit, integration, and end-to-end flows plus fuzz targets,
under `internal/` and `cmd/`. Direct Go invocation must include the tag:

```bash
go test -tags noassets ./...
```

**Test policy** (enforced in review):

- New feature → must include tests for the new behaviour.
- Bug fix → must include a regression test that fails on the parent commit and
  passes on the fix.
- Refactor → existing tests must keep passing.
- Docs / CI / packaging → no tests required.

## Lint & format

```bash
gofmt -l .                                     # must report nothing
go vet -tags noassets ./...
golangci-lint run --build-tags noassets ./...  # baseline: zero findings
```

Install the linter at the pinned version used by CI:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4
```

CI gates PRs on `build` and `lint`; running these locally before pushing avoids
round-trips.

## Project layout

```
cmd/dotkeeper/    CLI entrypoint + cobra command wiring (main.go) and CLI tests
internal/         all implementation packages (not a public API surface):
  config/           read/write machine.toml, state.toml, .dotkeeper.toml
  discovery/        scan-root discovery of managed repos
  reconcile/        desired/observed model, pure Diff, action types, applier
  stengine/         embedded Syncthing lifecycle
  stclient/         Syncthing REST API client
  conflict/         sync-conflict detection and resolution
  doctor/           dotkeeper doctor health checks
  gitsync/          staggered git auto-commit + push backup
  gitident/         canonical git-URL identity (subscription matching)
  subscribe/        declarative subscriptions + offer matching
  transport/        multi-transport routing + cost model
  benchmarker/      background transport benchmarking
  activity/ procnice/ service/ watchhealth/  supporting subsystems
docs/             architecture.md, adr/ (decision records), examples/
site/             static homepage assets served at dotkeeper.corbet.ch
```

To add a reconcilable property: add it to the config model, add an action type
in `internal/reconcile/`, implement the idempotent apply path, and pin the
behaviour with focused tests. See [`docs/architecture.md`](docs/architecture.md)
for the full narrative and [`docs/adr/`](docs/adr/README.md) for the durable
decision record.

## Contribution workflow (PR-based)

**`main` is protected — direct pushes are rejected. All non-trivial changes go
through a pull request.**

1. **Branch off `main`:** `git checkout -b <type>/<short-name>`
   (e.g. `fix/timer-race`, `docs/setup-guide`).
2. **Commit** in small, focused steps. Follow
   [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
   for the subject (`<type>(<scope>): <imperative summary>`, ≤ 72 chars) and the
   Tim Pope rules for the body (blank line after subject, wrap at 72, explain
   *why* not *what*). Types: `feat`, `fix`, `docs`, `refactor`, `test`, `ci`,
   `build`, `chore`, `deps`, `perf`, `style`.
3. **No tool-generated attribution.** Do not add `Co-Authored-By:` or other
   coding-assistant credit lines; they are removed before merge.
4. **Open a PR:** `gh pr create` or the GitHub UI.
5. **Green CI:** required checks `build` and `lint` must pass.
6. **Merge:** squash-merge is the convention (keeps history linear); rewrite the
   squash body into one coherent message rather than a bullet list of the branch
   commits. Delete the branch after merge.

### PR size

Prefer small, reviewable PRs (aim for under ~300 lines of diff). PRs over ~500
lines are likely to be sent back for splitting. If a feature splits into
independent pieces, split the PRs.

## Reporting issues

- Bugs / features: <https://github.com/julian-corbet/dotkeeper/issues>. For bugs,
  attach `dotkeeper version`, `go version`, and the output of
  `dotkeeper doctor --json`.
- **Security vulnerabilities:** do not open a public issue — use the private
  channel in [`SECURITY.md`](SECURITY.md).

## License

dotkeeper is AGPL-3.0. By submitting a PR you agree to the CLA in
[`CONTRIBUTING.md`](CONTRIBUTING.md#contributor-license-agreement-cla). New
source files carry the standard preamble:

```go
// Copyright (C) 2026 Julian Corbet
// SPDX-License-Identifier: AGPL-3.0-only
```
