# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x   | Yes       |

## Reporting a Vulnerability

If you discover a security vulnerability in dotkeeper, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, email **julian.corbet@gmail.com** with:

- A description of the vulnerability
- Steps to reproduce
- The affected version(s)
- Any suggested fix (optional)

You should receive a response within 48 hours. We will work with you to understand the issue and coordinate a fix before any public disclosure.

## Scope

dotkeeper embeds Syncthing and manages git repositories. Security-relevant areas include:

- **Config file permissions** (`machine.toml`, `config.toml`, `config.xml`) — these contain API keys and machine identity
- **Syncthing API** — listens on `127.0.0.1:18384`, authenticated via API key
- **Git operations** — auto-commit and push to configured remotes
- **Service installation** — systemd units, launchd plists, cron entries, Windows scheduled tasks
- **PID file handling** — validated against injection via regex

## Dependency Security

dotkeeper tracks known vulnerabilities in its dependency tree via `govulncheck`. The embedded Syncthing library is the largest dependency surface. We update to the latest Syncthing release with each dotkeeper release.

### Known unresolved advisories

As of v0.1.2, `govulncheck -tags noassets ./...` reports two outstanding advisories, both in `github.com/quic-go/quic-go`:

| Advisory | Status | Notes |
|----------|--------|-------|
| [GO-2025-4017](https://pkg.go.dev/vuln/GO-2025-4017) | **Called** (reachable) | Tracked; see below |
| [GO-2025-4233](https://pkg.go.dev/vuln/GO-2025-4233) | Module-only (not reached by dotkeeper code paths) | Tracked |

**Why not fixed:** Upstream fixes exist in quic-go v0.54.1 / v0.57.0, but Syncthing v1.30.0 (the version dotkeeper embeds) has API incompatibilities with those quic-go versions — `quic-go/logging` was removed, and `quic.Connection` / `quic.Stream` changed from value types to pointer types. Bumping quic-go without a matching Syncthing release breaks the build.

**Tracking plan:** monitor the [Syncthing release feed](https://github.com/syncthing/syncthing/releases) for a version that bumps quic-go past v0.54.1. At that point, a dotkeeper patch release will clear both advisories.

**Impact assessment for dotkeeper operators:** both advisories are in `quic-go`, which is only reached when the QUIC listener is enabled. **dotkeeper v0.4.0+ disables QUIC by default** and listens only on TCP at `:12000`, making both advisories unreachable code on a default install. (The disable also sidesteps a quic-go v0.52.0 startup-panic bug that produced a systemd restart loop on some systems.) If you had an older dotkeeper install with QUIC enabled, you can either upgrade or manually remove the `quic://` entry from your `~/.local/share/dotkeeper/syncthing/config.xml`.
