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
