# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.8.x   | Yes       |
| < 0.8   | No        |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Use GitHub's private security advisory channel:

> **[Report a vulnerability](https://github.com/julian-corbet/dotkeeper/security/advisories/new)**

The form encrypts the report end-to-end, gives the maintainer a private workspace
to coordinate a fix with you, and supports issuing a CVE on disclosure. Please
include:

- A description of the vulnerability
- Steps to reproduce
- The affected version(s)
- Any suggested fix (optional)

You should receive an acknowledgement within 48 hours.

If GitHub Security Advisories is unavailable to you, contact the maintainer
through the [GitHub profile](https://github.com/julian-corbet).

## Scope

dotkeeper embeds Syncthing and manages git repositories. Security-relevant areas include:

- **Config/state file permissions** (`machine.toml`, `state.toml`, `config.xml`) — `state.toml` and Syncthing's `config.xml` contain runtime identity material and API keys
- **Syncthing API** — listens on `127.0.0.1:18384`, authenticated via API key
- **Git operations** — auto-commit and push to configured remotes
- **Service installation** — systemd units, launchd plists, cron entries, Windows scheduled tasks
- **PID file handling** — validated against injection via regex

## Dependency Security

dotkeeper tracks known vulnerabilities in its dependency tree via `govulncheck`. The embedded Syncthing library is the largest dependency surface. We update to the latest Syncthing release with each dotkeeper release.

### Cleared advisories

**v0.8.0 (2026-05)** bumped the embedded Syncthing to v2.1.0, which brings quic-go v0.59.0 and clears the two long-standing advisories that v0.5–0.7 documented as unresolved:

| Advisory | Resolution |
|----------|------------|
| [GO-2025-4017](https://pkg.go.dev/vuln/GO-2025-4017) | Fixed upstream in quic-go v0.54.1; reached via Syncthing v2.1.0. |
| [GO-2025-4233](https://pkg.go.dev/vuln/GO-2025-4233) | Fixed upstream in quic-go v0.57.0; reached via Syncthing v2.1.0. |

QUIC remains disabled by default in dotkeeper's generated `config.xml` (TCP-only listen on `:12000`). The CVE-mitigation rationale is now historical — re-enabling QUIC for users who explicitly want it is tracked for a future release.

### Current known advisories

**None.** `govulncheck -tags noassets ./...` against v0.8.2 reports
"No vulnerabilities found." The stdlib advisories that v0.8.0 carried
(`net`, `net/http`, `net/mail`, `html/template`, …) cleared with the
Go 1.26.3 toolchain bump in v0.8.2.
