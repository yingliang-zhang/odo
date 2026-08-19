# `/loop audit` subject cap amendment — v1.2 — 2026-08-19

Scope decision (user direction 2026-08-19): keep `/loop audit` EXACTLY as-is —
no tiers, no flags, no new journal fields — and raise only the subject cap.
A fuller two-tier design (models=single|panel flag, per-tier caps,
findings_too_dense cause) was explored in a tri-model proposal round
(K3/GLM-5.2/DSF-v4-flash, blind, sealed, same brief) and explicitly shelved by
the user as unnecessary complexity. The three proposals + full tier DESIGN LOCK
draft are archived at /tmp/odo-loop-audit-moa/ (provenance: k3-output.md,
glm-output.md, dsf-output.md, loop-audit-tiers-v1.2-full-archive.md).

## The one change

The Mode A subject breaker (runAuditRound) reads a new loop-owned constant
`loopAuditSubjectCapBytes = 256 * 1024` (262,144B) instead of
`settleDiffCapBytes` (65,536B). settle.go's constants are untouched (shared
with the settle repair path, pinned by settle tests).

## Why 256KB

- Real squashed-land shapes measured 2026-08-19: M19 impl commit 042ab4b =
  233,533B of `git diff base..HEAD`; M19 GUI wave commit da9923a = 80,889B.
  Both now fit. Auto-land squashes each diff into ONE commit, so no
  intermediate base exists inside a feature — the cap must cover a feature.
- All three review models independently chose 256KB as a cap value (in
  different tier slots).
- Worst-case economics: ~64K tokens/leg × 3 legs (+ closure-pass carry) × 10
  rounds ≈ budget-breaker territory near round 8–9 — the 2M budget projection,
  round cap, C5 stall, and the 16KB findings-feed wall all stay armed. A 500KB
  subject remains physically inadmissible (hard wall, zero tokens spent).

## What stays (unchanged guards)

Findings-feed cap 16KB (loopSpawnFix, on settleCommentsCapBytes) — the true
convergence guard: dense subjects suspend honestly with advice to land/split
or use Mode B. Severity gate, infra-is-never-a-verdict, stall detection,
closure-pass, BYOF, one-loop-per-conversation, budget breakers,
land-each-round — all untouched.

## Dogfood criterion (m19-loop.md review class)

The milestone self-audit is now possible: `/loop audit base=<m19_base>` on the
233,533B M19 diff. PASS = the loop audits its own code at all (a fix verdict
driving convergence, or an honest subject/feed suspend with actionable
detail) — NOT necessarily a clean verdict.
