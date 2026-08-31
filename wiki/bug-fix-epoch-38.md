> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# W5 Worktree Drain + D9-C Grounded Tool-Round Cap (Quad-Blind Design Lock v2)

## Context
- Incident: K3 grounded review leg died 5× on `moa: tool loop exceeded 16 rounds` (issues #118 ×2, #120 ×3) while GLM/DSF legs accepted the same diffs. The round cap lagged the diff-size ladder (32K→256K) by two full rungs; precedent 8→16 on 2026-08-29.
- Quad-blind design review: 4/4 NEEDS_FIXES on v1, consolidated into lock v2; implemented exactly as locked — no additions, no omissions.

## Task 1 — W5 sibling worktree drain (completed)
- Completed, suite-validated W5 work (9 files, +2371/−31, full suite EXIT=0, ipc 543.3s, `/tmp/w119-final-suite.log`) was staged in the sibling worktree `.odo/worktrees/6a9514e5-6170c9a601f9`, including the `TestDistillStagesCandidateEndToEnd` flake fix (~12% load-dependent flake in the W4-restore base, not a W5 regression).
- Procedure: `git -C <sibling> diff --cached HEAD > /tmp/w5-final.patch`, then `git apply --index /tmp/w5-final.patch` in the canonical run worktree; `git status --short` file count matched the sibling's (9). Reply "done", no edits/gates.
- Standing rule applied: own fresh worktree is the only canonical checkout; sibling `.odo/worktrees/` dirs are read-only reference (pitfall #36).
- Afterward, auto-land attempts were blocked 3× (verdict needs_fixes, reason panel_infra), then resolved via accept + superseded.

## Task 2 — D9-C implementation (all 7 locked items landed)

Locked decisions and where they landed:
1. **Round cap 16→40**: `maxToolRounds` in `internal/moa/client.go:71/78`; `defaultToolRounds` stays 16, so callers passing 0 (`design_moa.go:289`, `server.go:4418`) are unchanged — pinned by `TestQueryWithToolsDefaultRoundCap`.
2. **Parameterized server cap, active by default**: `groundedMaxRounds` constant replaced by Server field `groundedToolRounds`, default 40 (fix ships ACTIVE; 4/4 legs held that a 16 default ships the incident unfixed — env/prefs are the escape hatch, not activation). Resolver `groundedToolRoundsCap()`: field > env `ODO_GROUNDED_TOOL_ROUNDS` > prefs `grounded_tool_rounds:` > 40; values >40 clamped to ceiling. Both call sites (grounded.go review leg, loop_audit.go audit leg) resolve through `planGrounded`/`plan.rounds` — single resolution point, shared budget. Header comment links the three budgets (rounds / 256KB fail-soft bytes / wall-clock) and the diff-cap ladder.
3. **Wall-clock interlock**: `groundedLegDeadline()` scales the grounded leg's outer deadline ×rounds/16 when rounds>16 (review base `s.legTimeout`, audit base `moa.TimeoutForModel`), so the typed infra death stays "round capacity", not a misleading timeout.
4. **Fail-visible accounting**: `ToolRoundsUsed` (from `len(calls)` BEFORE `capToolAudits` truncation) journaled on EVERY grounded leg row in `ReviewResult`/`auditLegResult`; round-cap death rows carry call names/args (in `ToolCalls`) and a `tool round-cap death (fail-hard)` comment, visually distinct from fail-soft `tool_budget_exhausted`.
5. **`groundedToolCallsCap` 64→96** (grounded.go:98) so the audit trail survives 40 rounds × ≥2 calls/round.
6. **Tests**: clamp table (0→16, 16→16, 40→40, 50→40); `TestGroundedToolRoundsResolution` (default 40, env round-trip, prefs, field precedence, garbage values); 0→16 no-op pin; `TestGroundedRoundCapIncident` (25-round glob→grep→read chain completes with a verdict under 40, fail-hard death under 16); `TestGroundedByteBudgetGraceful` (17-round loop hitting 256KB degrades to a verdict, not an infra error); `client_test.go:443-461` `default == ceiling` invariant intentionally retired, replaced with a default(16) < ceiling(40) pin; plus `TestGroundedLegDeadlineInterlock`, `TestAuditLegGroundedRoundsUsed`.
7. **Comment identity fix** at server.go:~4402: that call site is the user-facing `/panel` consult (unscoped home-root executor), not a design/audit leg; behavior untouched.

Explicitly untouched (per lock): consensus math, verdict semantics, Infra classification, revise ladder, and the 256KB fail-soft byte budget.

## Verification
- Focus gate (`-count=1`): `ok internal/moa 0.977s` (QueryWithTools), `ok internal/ipc 4.762s` (Grounded|LoopAudit|ToolRounds).
- Full suite: EXIT=0, 0 FAIL, ipc 527.4s (baseline-conformant), `/tmp/d9c-full-suite.log`.
- Same-class audit: all `QueryWithTools` call sites re-checked (0→16 default semantics unchanged); zero `groundedMaxRounds` residue; gofmt clean; build + vet green.
- 7 files, +477/−60 staged in the canonical run worktree, intentionally left dirty (uncommitted) — `grounded.go`/`loop_audit.go` are protected gate files, so the diff lands via the human Accept queue (expected, not avoided).

## Risk
- Only behavioral spillover: default 40 rounds raises the grounded leg's worst-case wall clock ~31min → ~78min (interlock ×2.5), lengthening worst-case panel_infra dwell. This is the lock's intent (capacity death > misleading timeout); `ODO_GROUNDED_TOOL_ROUNDS=16` or prefs `grounded_tool_rounds: 16` reverts to the old cap without redeploy.

## Open loops
- D9-C diff (7 files, +477/−60) is staged but uncommitted; it awaits human Accept-queue review because `grounded.go`/`loop_audit.go` are protected gate files.
- Post-ship monitoring: watch panel_infra dwell times under the new 40-round default; if worst-case wall clock (~78min/leg) hurts, revert via env `ODO_GROUNDED_TOOL_ROUNDS` or prefs `grounded_tool_rounds:` rather than code change.
- Confirm the W5 diff actually landed after the accept + superseded sequence (auto-land was blocked 3× with needs_fixes/panel_infra before that).