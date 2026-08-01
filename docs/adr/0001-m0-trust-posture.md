# ADR-0001: M0 Trust Posture — Human Review Only

## Status

Accepted — 2026-08-01

## Context

The predecessor project (Ananke) spent 95% of its effort on cryptographic
attestation, sandbox containment, and frozen contract pipelines — none of which
served the user's actual pain points. The attestation layer signed placeholder
constants in the integrated path, and the Accept button never modified the
user's repository (diffs were discarded).

Odo's M0 milestone deliberately omits all trust infrastructure. The only
verification mechanism is human eyeballs reviewing the diff before Accept.

This ADR records that decision explicitly, so the absence of attestation is
a deliberate choice — not an oversight to be "fixed" later.

## Decision

M0 has no attestation, no sandbox, no verifier, and no frozen contracts.
Trust is established by:

1. **Human reviews the diff** in the GUI before clicking Accept
2. **Accept applies to the working tree** via `git apply` (the value loop closes)
3. **All actions are journaled** in SQLite (what happened, including failures)
4. **Git is the safety net** — `git checkout` or `git stash` reverses any mistake

## Alternatives

### Reuse Ananke's attestation layer

Rejected. The attestation layer (22K LOC) was the primary source of divergence.
It signed placeholders in the integrated path and never closed the value loop.
Reintroducing it would repeat the exact failure that motivated the restart.

### Build a lightweight attestation

Rejected for M0. Any attestation, however lightweight, introduces a trust
boundary that competes with the visible loop for development attention.
Defer to M1+ only if the user explicitly requests it after M0 is in daily use.

## Consequences

- M0 Accept = human-eyeballs-only, no cryptographic verification
- The journal records the diff, the verdict, and the model routing, but does
  not sign them
- If attestation is needed later, it will be added as a separate layer (M1+)
  behind an ADR that justifies it against a demonstrated pain point
- This ADR exists to prevent the silent erosion of this decision by default
