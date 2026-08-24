> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# odo Review Remediation — P2 Quick-Fix Pack Completed (2026-08-24)

## Context

Continuation of the 2026-08-22 `/panel` four-leg full-repo review remediation. Prior session state: P0 (#1–#4) and P1 (#5–#14) already fixed; P2 quick-fix pack had never been scheduled. This session executed the full P2 pack end-to-end.

## Decisions

- **package-lock.json**: stray root copy already deleted before this session; only `gui/package-lock.json` is tracked. No commit needed.
- **Daemon restart verified**: PID 96093 running binary revision `da9f506` = main HEAD; deploy order (file mtime 10:25:35 → daemon start 10:25:56) conforms to "write-then-kill" protocol; live `journal.sqlite` shows `schema_version=3` with `diffs.goal` present. The epoch-22 "staleness warning + restart timing" open item is closed.
- **ListEvents narrowing + legacy markers**: initial narrowing by FoldWindow rule (no pin → `marker.seq+1`) instantly broke `TestDistillSweepSkipsRefusedBatch` — the sweep's entire purpose is rescuing pre-pin legacy batches. Final semantic: no-pin marker → `firstSeq=0` full-history fallback (bounded in practice since no fold since pin schema); pinned logs get the narrowed window.
- **stuck "~61min" figure not copied verbatim**: audit showed it was the pre-P1#9 16K→32K→64K escalation-stack residue. With leg deadlines (P1#9), the per-leg outer ceiling is `TimeoutForModel` = 1446s. Docs record the current mechanical bound (~35min/phase) and mark the review number superseded — avoids writing a second stale contract sentence.
- **Unanimity single-sourcing**: `consensusVerdict` accept-branch delegates to `panelAccepts` (one rule); reject short-circuit unchanged.

## Code changes (one diff; 20 files, +683/−204; build + vet + all package tests green, ipc 516s)

| Item | Implementation | Pin |
|---|---|---|
| Fold commit atomicity | `store.CommitFold`: epoch bump + marker in one transaction (`conversations.go`); distillCore uses builder closure | `TestCommitFoldAtomic` |
| `SearchEvents` escape + id order | `ESCAPE '\'` + `likeEscape`; `ORDER BY e.id DESC` (created_at second-resolution can tie) | `TestSearchEventsEscapesLike` |
| markers LIKE colon-space | `jsonPairMatch/jsonPairNegate` dual-form OR; all 9 value-bound patterns converted | `TestMarkerQueriesColonSpace` |
| Read-path narrowing | `store.LatestFoldBoundary` single-row probe → `listFoldWindowEvents`; distillCore probe `ListEvents(lastSeq)`; auto.go ×3 + autogate ×2 switched to windowed reads | sweep cases + previously failing group green |
| loop fold single decode | `loopRowPayload` one decode per row (was 2–4 per-key decodes) | `TestLoop*` 13.5s green |
| contradiction vs banner | `runContradictionPass` skips old notes with `supersededBanner` prefix | `TestContradictionPassSkipsSupersededBanner` |
| unanimity dedup | `consensusVerdict` delegates accept path to `panelAccepts` | +2 pins: infra-accept ≠ accept; reject overrides infra |
| risk: `os.Environ(` | `riskEnvDumpTokens` — dump forms hit credential_probe without suffix pairing | `TestClassifyRiskEnvDump` |
| risk: split rm | `riskRmSplit`: FieldsFunc punctuation tokenization catches `rm -r -f`, `rm -Rf`, multi-space, `--recursive --force` | `TestClassifyRiskSplitRm` |
| risk: CI workflow | `autoLandSupplyChainPath` SSOT predicate (`.github/workflows/` + `.gitlab-ci.yml`) shared by gate + classifier | `TestAutoLandCheck` +3, `TestClassifyRiskCIWorkflow` |
| 87K breaker CJK margin | `estimatePromptTokens`: ASCII/4 + 1 token per non-ASCII rune (old len/4 underestimated ~3×) | `TestEstimatePromptTokensCJK` + 110K CJK e2e pin (85K old-pass/110K new-block) |
| escalation input ledger | `Escalation.InputTokens`: records re-paid prompt cost of abandoned requests on each bump | `TestQueryEscalatesOnMaxTokens` updated |
| ADR-0002 amendment | Appendix: v1→v3 delta table (schema_version table, workstreams minus git columns, diffs +worktree_path/+goal) | follows ADR-0003 amendment convention |
| stuck definition doc | gui-visibility.md: 900s = floor, outer ceiling `TimeoutForModel` = 1446s, true stuck ≈ 35min/phase | — |

## Prior status confirmed this session

- **P0 (4/4)**: fixed via diff #32 → `ba68fb5` (gate-source majority valve, C11 liveness drain, contradiction advisory-only, memory auto-gate journal + startup staleness self-check).
- **P1 (10/10)**: diff #34 (strict superset of never-landed #33) auto-rejected on a false "objective mismatch"; orphan commit cherry-picked to main in epoch-20 as `4345f24`; panel objective anchoring fixed by `954ff22` (schema v3).
- **Review-adjudicated "won't do"** items (7: embedding recall, memory review queue, etc.) remain deliberate non-goals.

## Open loops

- P2 diff blocked at auto-land with `protected_path` (touches `autoland.go` + `wiki/`); awaits human accept — not yet landed on main.
- loop spawn `prompt_tokens_est` still uses chars/4 (same underestimate family as the 87K breaker; outside review scope) — needs a separate diff if the margin rule should be unified.
- hermes-agent project daemon (`~/.odo/bin/odo`, PID 85939, since Aug 20) still runs an old binary; restarting it would kill its sessions — user decision pending.
- `.odo/odo.db` 0-byte leftover from 08-15 remains in place (harmless; daemon actually uses `journal.sqlite`).
- Diff #33 remains permanently pending in the append-only archive (superseded by #34; no action required unless archive hygiene is desired).