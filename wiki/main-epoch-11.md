# Epoch: Memory/Wiki Panel Auto-Adjudication

## User directives
1. Commit the accumulated wiki backlog in the main checkout.
2. **Replace human review with panel auto-review for memory/wiki proposals** — the panel auto-accepts or rejects; the user only pulls up the MemoryPanel to look when needed.

## Decisions
- `distillCore` is the single funnel: gate + propose run before the marker (unchanged), auto-apply moved after the marker so `unownedFoldGrowth` needed zero changes.
- Wiki changes auto-commit at distill end (and at standalone curate end); the commit emits a `memory_update{layer:wiki}` event. Accept-path safety confirmed: `checkAndRefreshBase` 3-way refresh tolerates HEAD movement from wiki commits on disjoint paths.
- **Empty-window guard semantically redefined**: `firstSeq,lastSeq=FoldWindow` hoisted; emptiness judged by `!unownedFoldGrowth(events, firstSeq-1)`, sharing the "fold self-authored line" classification with the supersession probe. Pure bookkeeping (wiki commit telemetry) windows = empty windows. Other window consumers audited (auto.go ×3, cmd_journal) — unaffected.
- Test-fix convention: fixes fold into the same snapshot (epoch-10 convention) rather than a new diff.
- Git identity assumption unchanged: `initRepo` already depends on environment git identity; wiki auto-commit inherits it.

## Code changes (diff #28, 15 files, base `0f4a718`)
- **Go daemon**
  - `skills_gate.go` — rewritten: generalized fan-out, rules review prompt, `panelAccepts`.
  - `server.go` — extracted `applyResolvedBatch`; `handleMemoryProposals` returns consumed-state outcomes; distillCore: models hoisted + sweep, non-skill gating, marker-then auto-apply + wiki commit; empty-window guard at server.go:3605-3616.
  - `memory_autogate.go` (new), `wiki_commit.go` (new); `curateCore` hook added; owned-set extended in the sweep.
  - Removed duplicate `StagePaths` (existing `git add --` semantics sufficient) and duplicate `gitOut` helper in tests.
- **GUI** — `types.ts` + MemoryPanel four-part surgery: verdict + outcome chips, batch fill via `refreshBatch` consumed-state. tsc clean, 110 vitest green.
- **Tests** — new `memory_autogate_test.go` (+ auto/server test files); stale assertions updated to consumed-view contract; `TestDistill`/`TestDistillViaMoa` tail assertions repointed (wiki row pinned as new contract); `TestAutoUrgentFiresImmediately` gained a drain barrier polling `server.distilling[convID]` against TempDir teardown race.
- **Validation** — 4 target tests ×2: 89.8s ok; full ipc suite: 450s, 0 FAIL; git/adapter/store/modelspec/moa/root packages ok. Regenerated snapshot byte-identical to worktree staging; `git apply --check` clean vs base.

## Failure root causes fixed (from interrupted run at seq 5611)

| Test | Nature | Fix |
|---|---|---|
| TestDistillEmptyWindow | Real regression: wiki-commit self-event fooled nothing-new probe | Guard semantic change (above) |
| TestDistill | Stale assertion (extra telemetry tail row) | Expectation repointed, index -1→-2/-3 |
| TestDistillViaMoa/receipts | Same, last-event assertion | Receipt check moved to `events[len-2]` |
| TestAutoUrgentFiresImmediately | Test-side race: marker-return vs pipeline tail/TempDir RemoveAll | Drain barrier |

## Open loops
- Diff #28 (memory/wiki panel auto-adjudication, 15 files) is pending — accept in GUI; once landed, next distill activates the auto-gate.
- Untracked `package-lock.json` at repo root (pre-existing, not from this session) — decide whether to track or delete.
- `switch-cache.spec.ts` Playwright e2e — decision moot (#22 landed with no regression observed); needs marking as won't-run, or a real Tauri environment if re-verification is wanted.