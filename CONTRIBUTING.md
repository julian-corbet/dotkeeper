# Contributing to dotkeeper

Contributions are welcome. Bug reports, feature requests, and pull requests all help.

## Contributor License Agreement (CLA)

By submitting a pull request, you agree to the following:

1. You license your contribution under the same license as this project (AGPL-3.0).
2. You grant the project maintainer a perpetual, worldwide, non-exclusive, royalty-free, irrevocable license to use, modify, sublicense, and relicense your contribution under any license, including proprietary licenses.
3. You confirm that you have the right to make this grant (i.e., the contribution is your original work, or you have the necessary rights from your employer or other rights holder).

This CLA ensures the maintainer can adapt the project's licensing as needed while keeping the open-source version available under AGPL-3.0.

## Development

### Building

```bash
make build
```

Requires Go ≥ 1.26 and `git`. The `-tags noassets` flag is handled by the Makefile.

### Testing

```bash
make test
```

Go's built-in test runner. The suite covers unit, integration, and end-to-end flows plus fuzz targets under `internal/`.

### Linting

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4
golangci-lint run --build-tags noassets ./...
```

The CI workflow gates PRs on this; locally it's optional but recommended before pushing.

### Running the binary locally

```bash
make install    # installs to ~/.local/bin/dotkeeper
dotkeeper status
```

## Pull request workflow

**All non-trivial changes go through a pull request.** `main` is protected — direct pushes are rejected.

1. **Branch.** `git checkout -b <topic>/<short-name>` (e.g. `fix/timer-race`, `docs/setup-guide`).
2. **Commit.** Small, focused commits with clear messages. No AI-generated attribution lines.
3. **Open a PR.** `gh pr create` or the GitHub UI.
4. **Wait for CI.** Required checks: `build` and `lint`. Both must pass.
5. **Merge.** Squash-merge is the convention (keeps history linear). Delete the branch after merge.

### PR size

Prefer small, reviewable PRs over sweeping changes. If a feature naturally splits into independent pieces, split the PRs. Aim for under ~300 lines of diff where reasonable.

### When tests are required

- **New feature** → must include tests exercising the new behaviour. If the feature is inherently hard to test in-process (e.g. integration with systemd), call that out in the PR description.
- **Bug fix** → must include a regression test that fails on the parent commit and passes on the fix.
- **Refactor** → existing tests should continue to pass; add tests only if the refactor uncovers behaviour that wasn't previously covered.
- **Docs / CI / packaging** → no tests required.

### Commit message conventions

Follow conventional-ish prefixes where they help:
- `feat:` new functionality
- `fix:` bug fix
- `docs:` documentation only
- `ci:` CI / workflow changes
- `refactor:` code restructure, no behaviour change
- `test:` test-only changes
- `deps:` dependency updates (Dependabot uses this)

Commit message bodies should explain the *why*, not the *what*. Read a few recent `git log` entries for the local style.

## Code style

- Standard Go conventions (`gofmt`, `go vet`). CI enforces both.
- `golangci-lint` baseline is **zero findings**. If you add code that flags a new finding, either fix it or justify in the PR.
- Prefer clarity over cleverness. dotkeeper is maintained by few people — code should read easily on a fresh mind six months later.
- Comments explain *why*, not *what*. Don't describe what the code does if the code is clear; do describe non-obvious invariants, platform caveats, or upstream bugs you're working around.
- No copyright headers in new files beyond the standard `// Copyright (C) 2026 Julian Corbet\n// SPDX-License-Identifier: AGPL-3.0-only` preamble.

## Reporting issues

### Bug reports

Open a GitHub issue at <https://github.com/julian-corbet/dotkeeper/issues>. Include:

- What you expected to happen
- What actually happened
- Your OS and Go version (`go version`)
- dotkeeper version (`dotkeeper version`)
- Relevant output from `dotkeeper status`
- A minimal reproduction if possible

### Security issues

**Do not open public issues for security vulnerabilities.** See [SECURITY.md](SECURITY.md) for the private disclosure channel and response SLA.

### Feature requests

Open a GitHub issue describing the use case, the proposed behaviour, and any alternatives you considered. Feature requests that match the project's scope get triaged; ones outside scope get politely declined with rationale.

## Review expectations

- **Reviewer turnaround:** best-effort within a few business days for most PRs. Security fixes get priority.
- **CI must be green** before merge. No exceptions.
- **Breaking changes** to the CLI, config file format, or on-disk layout require a major version bump and a migration path documented in the release notes.
- **Large PRs** (>500 lines) are likely to be sent back for splitting. Splitting is healthy — it gives each change its own review pass.

## Governance

dotkeeper is currently maintained by [@julian-corbet](https://github.com/julian-corbet). Decisions about scope, direction, and releases are the maintainer's call. As the project matures, governance may move to a multi-maintainer model; that change will be announced in the repo.
