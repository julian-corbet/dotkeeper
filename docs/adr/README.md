# Architecture Decision Records

This directory holds ADRs: short records of architecturally-significant choices
we've made, why we made them, and what we considered alternative to.

An ADR is written once, reviewed in a PR, and from that point on is
immutable except to mark it superseded. If a decision gets revisited, the new
decision gets its own numbered ADR that references (and supersedes) the old
one. Never edit a merged ADR's content — the history is the point.

## Format

Each ADR follows a light structure:

- **Context** — what situation forces a decision
- **Decision** — what we're doing
- **Rationale** — why this choice over alternatives
- **Consequences** — what flows from the decision, good and bad
- **Alternatives considered** — what we explicitly looked at and rejected

Files are named `NNNN-kebab-case-title.md` with zero-padded sequence numbers.

## Index

| # | Title | Status |
|---|---|---|
| [0001](0001-per-repo-config.md) | Per-repo `dotkeeper.toml` as authoritative config | Accepted |
| [0002](0002-machine-state-split.md) | Machine-local state split: declarative vs tool-owned | Accepted |
| [0003](0003-reconciler-loop.md) | Reconciler model: pure function over observed+desired | Accepted |
| [0004](0004-scan-root-discovery.md) | Scan-root–based repo discovery | Accepted |

## Status values

- **Accepted** — current decision, in effect (or planned for the next release that implements it)
- **Superseded** — replaced by a later ADR; kept for history
- **Deprecated** — no longer current, nothing replaces it yet

## Note on timing

ADRs can describe decisions that are **accepted but not yet implemented**.
That's the point — the record precedes the code. ADRs 0001–0004 together
describe the architecture planned for v0.5.
