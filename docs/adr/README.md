# Architecture Decision Records

This directory holds ADRs: short records of architecturally-significant choices
we've made, why we made them, and what we considered alternative to.

An ADR should stay stable once released. If a decision gets revisited, prefer a
new numbered ADR or an explicit "revised" status note that states the newer
contract.

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
| [0001](0001-per-repo-config.md) | Local per-repo `.dotkeeper.toml` as authoritative config | Accepted, revised |
| [0002](0002-machine-state-split.md) | Machine-local state split: declarative vs tool-owned | Accepted |
| [0003](0003-reconciler-loop.md) | Reconciler model: pure function over observed+desired | Accepted |
| [0004](0004-scan-root-discovery.md) | Scan-root–based repo discovery | Accepted |

## Status values

- **Accepted** — current decision, in effect (or planned for the next release that implements it)
- **Superseded** — replaced by a later ADR; kept for history
- **Deprecated** — no longer current, nothing replaces it yet

## Note on timing

ADRs can describe decisions that are **accepted but not yet implemented**.
That's the point — the record can precede the code. ADRs 0001-0004 describe
the reconciler architecture; ADRs 0001 and 0004 were revised for v0.6's
local-only repo metadata contract.
